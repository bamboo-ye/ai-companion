package document

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/windcry1/ai-companion/internal/semantic"
)

func TestWikiBatchesCoverEveryByteAndKeepUTF8(t *testing.T) {
	d := Document{ID: "doc"}
	chunks := []Chunk{{ID: "one", Content: strings.Repeat("中", 20001)}, {ID: "two", Content: "尾部决策"}}
	batches := wikiEvidenceBatches(d, chunks)
	joined := map[string]string{}
	for _, batch := range batches {
		size := 0
		for _, e := range batch {
			if !utf8.ValidString(e.Quote) {
				t.Fatal("split UTF8")
			}
			size += len(e.Quote)
			joined[e.ChunkID] += e.Quote
		}
		if size > wikiBatchBytes {
			t.Fatal("batch exceeded limit")
		}
	}
	for _, c := range chunks {
		if joined[c.ID] != c.Content {
			t.Fatal("lost source text")
		}
	}
}

func TestWikiParallelSynthesisCachesSuccessAndRejectsForeignCitations(t *testing.T) {
	var active, peak atomic.Int32
	var mu sync.Mutex
	calls := map[string]int{}
	barrier := make(chan struct{})
	var entered atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var evidence []WikiEvidence
		if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &evidence); err != nil {
			t.Error(err)
			return
		}
		id := evidence[0].ChunkID
		mu.Lock()
		calls[id]++
		attempt := calls[id]
		mu.Unlock()
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		if entered.Add(1) == 2 {
			close(barrier)
		}
		select {
		case <-barrier:
		case <-time.After(2 * time.Second):
			t.Error("no parallel execution")
		}
		if id == "tail" && attempt <= 2 {
			w.WriteHeader(503)
			return
		}
		out := wikiBatchOutput{Pages: []wikiGeneratedPage{
			{Kind: "decision", Title: "决定", Body: fmt.Sprintf("来自 %s [chunk:%s]", id, id), ChunkIDs: []string{id}},
			{Kind: "entity", Title: "伪造引用", Body: "伪造 [chunk:foreign]", ChunkIDs: []string{"foreign"}},
		}}
		data, _ := json.Marshal(out)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(data)}}}})
	}))
	defer server.Close()
	s, _, _ := wikiFixture(t)
	s.SetKnowledge(semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "test", WikiConcurrency: 2, Timeout: 3 * time.Second}), true)
	d := Document{ID: "doc", UserID: "owner", Name: "test", SHA256: "v1"}
	chunks := []Chunk{{ID: "head", Content: strings.Repeat("a", wikiBatchBytes)}, {ID: "tail", Content: "最终决策"}}
	source := WikiPage{ID: "source", Sources: []WikiSource{{DocumentID: d.ID, Version: SourceVersion(d)}}}
	pages := s.synthesizeWiki(context.Background(), d, chunks, &source)
	if peak.Load() != 2 || source.Synthesis.Completed != 1 || source.Synthesis.Degraded == "" {
		t.Fatalf("peak=%d coverage=%+v", peak.Load(), source.Synthesis)
	}
	if len(pages) != 1 || pages[0].Title != "决定" {
		t.Fatalf("untrusted pages: %+v", pages)
	}
	pages = s.synthesizeWiki(context.Background(), d, chunks, &source)
	mu.Lock()
	defer mu.Unlock()
	if calls["head"] != 1 || calls["tail"] != 3 || source.Synthesis.Completed != 2 || source.Synthesis.Degraded != "" {
		t.Fatalf("calls=%v coverage=%+v", calls, source.Synthesis)
	}
	if len(pages) != 1 || len(pages[0].Evidence) != 2 || !strings.Contains(pages[0].Body, "tail") {
		t.Fatalf("merge lost evidence: %+v", pages)
	}
}

func TestWikiSemanticBudgetDoesNotSummarizeOnlyPrefix(t *testing.T) {
	s, _, _ := wikiFixture(t)
	s.SetKnowledge(semantic.New(semantic.Config{SummaryModel: "test", WikiMaxBatches: 1}), true)
	source := WikiPage{}
	pages := s.synthesizeWiki(context.Background(), Document{ID: "doc"}, []Chunk{{ID: "c", Content: strings.Repeat("a", wikiBatchBytes+1)}}, &source)
	if len(pages) != 0 || source.Synthesis.Completed != 0 || source.Synthesis.Degraded != "semantic_batch_budget_exceeded" || s.semantic.Metrics().Calls != 0 {
		t.Fatal("partial prefix was synthesized")
	}
}

func TestWikiLargeSameTitleResultsSplitIntoStableUniquePages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := wikiBatchOutput{}
		for i := 0; i < 8; i++ {
			out.Pages = append(out.Pages, wikiGeneratedPage{Kind: "topic", Title: "主题", Body: fmt.Sprintf("陈述%d %s [chunk:c]", i, strings.Repeat("a", 15000)), ChunkIDs: []string{"c"}})
		}
		data, _ := json.Marshal(out)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(data)}}}})
	}))
	defer server.Close()
	s, _, _ := wikiFixture(t)
	s.SetKnowledge(semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "test"}), true)
	d := Document{ID: "doc", UserID: "owner", Name: "test"}
	source := WikiPage{ID: "source"}
	chunks := []Chunk{{ID: "c", Content: "来源"}}
	first := s.synthesizeWiki(context.Background(), d, chunks, &source)
	second := s.synthesizeWiki(context.Background(), d, chunks, &source)
	if len(first) != 2 || len(second) != 2 || first[0].ID == first[1].ID {
		t.Fatalf("expected two distinct pages, got %d and %d", len(first), len(second))
	}
	for i, page := range first {
		if len(page.Body) > 64000 || page.ID != second[i].ID || page.Body != second[i].Body {
			t.Fatal("merge exceeded its limit or changed on cache replay")
		}
	}
}
