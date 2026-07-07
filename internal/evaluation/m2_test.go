package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/document"
)

type m2Baseline struct {
	Version      string             `json:"version"`
	MinimumCases int                `json:"minimum_cases"`
	Thresholds   map[string]float64 `json:"thresholds"`
	Subjects     []m2Subject        `json:"subjects"`
}

type m2Subject struct {
	ID               string   `json:"id"`
	DocumentName     string   `json:"document_name"`
	Content          string   `json:"content"`
	ExpectedContains string   `json:"expected_contains"`
	RelatedQueries   []string `json:"related_queries"`
	UnrelatedQueries []string `json:"unrelated_queries"`
}

type baselineParser struct{}

func (baselineParser) Parse(_ context.Context, _ string, data []byte) (document.ParseResult, error) {
	content := string(data)
	return document.ParseResult{
		ParserVersion: "m2-eval-parser-v1",
		Pages:         []document.Page{{PageNo: 1, Text: content, Quality: 1, ContentHash: "page"}},
		Chunks:        []document.Chunk{{Ordinal: 1, PageStart: 1, PageEnd: 1, Content: content, TokenCount: len([]rune(content)) / 2, ContentHash: "chunk"}},
	}, nil
}

type indexedHit struct {
	userID string
	hit    document.SearchHit
}

type baselineIndex struct{ hits []indexedHit }

func (*baselineIndex) Ensure(context.Context) error { return nil }
func (i *baselineIndex) Upsert(_ context.Context, item document.Document, chunks []document.Chunk) error {
	for _, chunk := range chunks {
		i.hits = append(i.hits, indexedHit{userID: item.UserID, hit: document.SearchHit{
			ChunkID: chunk.ID, DocumentID: item.ID, DocumentName: item.Name, PageStart: chunk.PageStart,
			PageEnd: chunk.PageEnd, SectionPath: chunk.SectionPath, Content: chunk.Content, Score: 1,
		}})
	}
	return nil
}
func (i *baselineIndex) Search(_ context.Context, userID, _ string, documentIDs []string, limit int) ([]document.SearchHit, error) {
	allowed := map[string]bool{}
	for _, id := range documentIDs {
		allowed[id] = true
	}
	result := make([]document.SearchHit, 0)
	for _, item := range i.hits {
		if item.userID == userID && allowed[item.hit.DocumentID] {
			result = append(result, item.hit)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}
func (*baselineIndex) DeleteDocument(context.Context, string, string) error { return nil }

func TestM2RAGBaseline(t *testing.T) {
	data, err := os.ReadFile("../../evals/m2/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline m2Baseline
	if err = json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	totalCases := 0
	relatedTotal, relatedCorrect := 0, 0
	unrelatedTotal, unrelatedCorrect := 0, 0
	totalCitations, correctCitations := 0, 0
	faithful := 0

	for subjectIndex, subject := range baseline.Subjects {
		store := document.NewMemoryStore()
		blobs := document.NewMemoryBlobStore()
		index := &baselineIndex{}
		service := document.NewService(store, blobs, 1<<20)
		service.SetVectorIndex(index)
		userID := fmt.Sprintf("eval-user-%d", subjectIndex)
		item, created, uploadErr := service.Upload(ctx, userID, subject.DocumentName, []byte(subject.Content))
		if uploadErr != nil || !created {
			t.Fatalf("%s upload = %v, created=%v", subject.ID, uploadErr, created)
		}
		ingestor := document.NewIngestor(store, blobs, baselineParser{}, index, "eval-worker")
		if processed, ingestErr := ingestor.RunOnce(ctx); ingestErr != nil || !processed {
			t.Fatalf("%s ingest = %v, processed=%v", subject.ID, ingestErr, processed)
		}

		for _, query := range subject.RelatedQueries {
			totalCases++
			relatedTotal++
			result, queryErr := service.Query(ctx, userID, document.QueryInput{Query: query})
			if queryErr != nil {
				t.Errorf("%s related query %q: %v", subject.ID, query, queryErr)
				continue
			}
			totalCitations += len(result.Citations)
			citationOK := len(result.Citations) > 0 && result.Citations[0].DocumentID == item.ID && result.Citations[0].PageStart == 1
			if citationOK {
				correctCitations++
			}
			faithfulAnswer := strings.Contains(result.Answer, subject.ExpectedContains) && len(result.Citations) > 0 && strings.Contains(subject.Content, result.Citations[0].Quote)
			if faithfulAnswer {
				faithful++
			}
			if result.Sufficient && citationOK && faithfulAnswer {
				relatedCorrect++
			}
		}
		for _, query := range subject.UnrelatedQueries {
			totalCases++
			unrelatedTotal++
			result, queryErr := service.Query(ctx, userID, document.QueryInput{Query: query})
			if queryErr != nil {
				t.Errorf("%s unrelated query %q: %v", subject.ID, query, queryErr)
				continue
			}
			if !result.Sufficient && len(result.Citations) == 0 {
				unrelatedCorrect++
			}
		}
	}

	if totalCases < baseline.MinimumCases {
		t.Fatalf("evaluation cases = %d, minimum = %d", totalCases, baseline.MinimumCases)
	}
	metrics := map[string]float64{
		"context_precision":     ratio(correctCitations, totalCitations),
		"context_recall":        ratio(relatedCorrect, relatedTotal),
		"citation_correctness":  ratio(correctCitations, relatedTotal),
		"faithfulness":          ratio(faithful, relatedTotal),
		"insufficient_accuracy": ratio(unrelatedCorrect, unrelatedTotal),
	}
	for name, threshold := range baseline.Thresholds {
		value, ok := metrics[name]
		if !ok {
			t.Errorf("threshold %s has no metric", name)
			continue
		}
		if value < threshold {
			t.Errorf("%s = %.3f, threshold = %.3f", name, value, threshold)
		}
	}
	t.Logf("M2 baseline %s: cases=%d precision=%.3f recall=%.3f citations=%.3f faithfulness=%.3f insufficient=%.3f",
		baseline.Version, totalCases, metrics["context_precision"], metrics["context_recall"], metrics["citation_correctness"], metrics["faithfulness"], metrics["insufficient_accuracy"])
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
