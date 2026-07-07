package document

import (
	"context"
	"os"
	"testing"
)

type fixedParser struct{}

func (fixedParser) Parse(_ context.Context, mediaType string, data []byte) (ParseResult, error) {
	content := string(data)
	return ParseResult{
		ParserVersion: "test-parser-v1",
		Pages:         []Page{{PageNo: 1, Text: content, Quality: 1, ContentHash: "page-hash"}},
		Chunks:        []Chunk{{Ordinal: 1, PageStart: 1, PageEnd: 1, SectionPath: "结论", Content: content, TokenCount: 8, ContentHash: "chunk-hash"}},
	}, nil
}

type memoryIndex struct {
	hits    []SearchHit
	deleted map[string]bool
}

func (m *memoryIndex) Ensure(context.Context) error { return nil }
func (m *memoryIndex) Upsert(_ context.Context, item Document, chunks []Chunk) error {
	for _, chunk := range chunks {
		m.hits = append(m.hits, SearchHit{
			ChunkID: chunk.ID, DocumentID: item.ID, DocumentName: item.Name,
			PageStart: chunk.PageStart, PageEnd: chunk.PageEnd, SectionPath: chunk.SectionPath,
			Content: chunk.Content, Score: 1,
		})
	}
	return nil
}
func (m *memoryIndex) Search(_ context.Context, userID, query string, documentIDs []string, limit int) ([]SearchHit, error) {
	result := make([]SearchHit, 0)
	for _, hit := range m.hits {
		if !m.deleted[hit.DocumentID] {
			result = append(result, hit)
		}
	}
	return result, nil
}
func (m *memoryIndex) DeleteDocument(_ context.Context, userID, documentID string) error {
	m.deleted[documentID] = true
	return nil
}

func TestIngestQueryAndDeleteLifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	index := &memoryIndex{deleted: map[string]bool{}}
	service := NewService(store, blobs, 1024)
	service.SetVectorIndex(index)
	item, created, err := service.Upload(ctx, "user-1", "evidence.txt", []byte("项目结论：火星计划在四月启动。"))
	if err != nil || !created {
		t.Fatalf("upload = %#v, %v, %v", item, created, err)
	}
	ingestor := NewIngestor(store, blobs, fixedParser{}, index, "worker-1")
	processed, err := ingestor.RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("ingest = %v, %v", processed, err)
	}
	ready, err := service.Get(ctx, "user-1", item.ID)
	if err != nil || ready.Status != "ready" || ready.PageCount != 1 || ready.ChunkCount != 1 {
		t.Fatalf("ready document = %#v, %v", ready, err)
	}
	answer, err := service.Query(ctx, "user-1", QueryInput{Query: "火星计划什么时候启动？"})
	if err != nil || !answer.Sufficient || len(answer.Citations) != 1 || answer.Citations[0].PageStart != 1 {
		t.Fatalf("answer = %#v, %v", answer, err)
	}
	missing, err := service.Query(ctx, "user-1", QueryInput{Query: "木星预算是多少？"})
	if err != nil || missing.Sufficient || len(missing.Citations) != 0 {
		t.Fatalf("insufficient answer = %#v, %v", missing, err)
	}
	if tokenOverlap("What is the Jupiter mission budget?", "Evidence lives on page one.") != 0 {
		t.Fatal("unrelated English stop words created a false overlap")
	}
	if err = service.Delete(ctx, "user-1", item.ID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := service.Query(ctx, "user-1", QueryInput{Query: "火星计划什么时候启动？"})
	if err != nil || afterDelete.Sufficient {
		t.Fatalf("deleted document answer = %#v, %v", afterDelete, err)
	}
}

func TestRunJobClaimsOnlyKafkaTarget(t *testing.T) {
	ctx := context.Background()
	store, blobs := NewMemoryStore(), NewMemoryBlobStore()
	index := &memoryIndex{deleted: map[string]bool{}}
	service := NewService(store, blobs, 1024)
	first, _, err := service.Upload(ctx, "user-1", "first.txt", []byte("first document"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.Upload(ctx, "user-1", "second.txt", []byte("second document"))
	if err != nil {
		t.Fatal(err)
	}
	ingestor := NewIngestor(store, blobs, fixedParser{}, index, "worker-kafka")
	processed, err := ingestor.RunJob(ctx, second.JobID)
	if err != nil || !processed {
		t.Fatalf("RunJob processed=%v err=%v", processed, err)
	}
	firstState, _ := service.Get(ctx, "user-1", first.ID)
	secondState, _ := service.Get(ctx, "user-1", second.ID)
	if firstState.Status != "queued" || secondState.Status != "ready" {
		t.Fatalf("first=%s second=%s", firstState.Status, secondState.Status)
	}
}

func TestVectorizeIsDeterministicAndSparseSorted(t *testing.T) {
	firstDense, firstIndices, firstValues := vectorize("火星计划 April")
	secondDense, secondIndices, secondValues := vectorize("火星计划 April")
	if len(firstDense) != 256 || len(firstIndices) == 0 || len(firstIndices) != len(firstValues) {
		t.Fatalf("vector sizes = %d %d %d", len(firstDense), len(firstIndices), len(firstValues))
	}
	for index := range firstDense {
		if firstDense[index] != secondDense[index] {
			t.Fatal("dense vector is not deterministic")
		}
	}
	for index := range firstIndices {
		if firstIndices[index] != secondIndices[index] || firstValues[index] != secondValues[index] {
			t.Fatal("sparse vector is not deterministic")
		}
		if index > 0 && firstIndices[index-1] >= firstIndices[index] {
			t.Fatal("sparse indices are not strictly sorted")
		}
	}
}

func TestPythonParserIntegration(t *testing.T) {
	executable := os.Getenv("AI_COMPANION_TEST_PYTHON")
	if executable == "" {
		t.Skip("AI_COMPANION_TEST_PYTHON is not set")
	}
	parser := PythonParser{Executable: executable, ModulePath: "../../workers/python/src"}
	result, err := parser.Parse(context.Background(), "text/plain", []byte("第一章\n\n真实 Python 解析器会保留页码和结构。"))
	if err != nil || len(result.Pages) != 1 || len(result.Chunks) != 1 || result.Pages[0].PageNo != 1 {
		t.Fatalf("python parser = %#v, %v", result, err)
	}
}
