package document

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestKnowledgeSearchIncludesSourcesBeyondUIWindow(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewService(store, NewMemoryBlobStore(), 0)
	index := &memoryIndex{deleted: map[string]bool{}}
	s.SetVectorIndex(index)
	for i := 0; i < 513; i++ {
		key := fmt.Sprintf("doc-%04d", i)
		doc := Document{ID: key, UserID: "alice", Name: key, Status: "ready", ChunkCount: 1, UpdatedAt: time.Now()}
		chunk := Chunk{ID: key + "-chunk", Content: "火星预算为一百元", PageStart: 1, PageEnd: 1}
		store.items[key] = doc
		store.chunks[key] = []Chunk{chunk}
		_ = index.Upsert(ctx, doc, []Chunk{chunk})
		if err := store.ShareDocumentWithWorkspace(ctx, "alice", "team", key, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for _, workspace := range []string{"", "team"} {
		items, err := s.KnowledgeDocuments(ctx, "alice", workspace)
		if err != nil || len(items) != 513 {
			t.Fatal("scope truncated", workspace, len(items), err)
		}
		query := QueryInput{Query: "火星预算", DocumentIDs: []string{"doc-0512"}, Limit: 1}
		var result QueryResult
		if workspace == "" {
			result, err = s.Query(ctx, "alice", query)
		} else {
			result, err = s.QueryWorkspace(ctx, workspace, query)
		}
		if err != nil || len(result.Citations) != 1 || result.Citations[0].DocumentID != "doc-0512" {
			t.Fatal("explicit older source lost", workspace, result, err)
		}
	}
	store.items["doc-0512"] = Document{ID: "doc-0512", UserID: "alice", Status: "deleted"}
	if _, _, err := s.KnowledgeChunks(ctx, "bob", "team", "doc-0512"); err == nil {
		t.Fatal("deleted shared source read")
	}
}

func TestWikiSearchRevisionUsesCompleteSource(t *testing.T) {
	s, _, ingestor := wikiFixture(t)
	ingestWiki(t, s, ingestor, "alice", "长文.txt", fmt.Sprintf("预算 %0600d", 1))
	res, err := s.SearchWiki(context.Background(), "alice", "", "预算", 8, 6000)
	if err != nil || len(res.Hits) == 0 {
		t.Fatal(err)
	}
	for _, h := range res.Hits {
		full, err := s.ReadWiki(context.Background(), "alice", "", h.Page.ID)
		if err != nil || h.Revision != WikiRevision(full) {
			t.Fatal("preview has invalid revision", err)
		}
	}
}

func TestWikiSemanticRecallBeyondTwoHundredPages(t *testing.T) {
	ctx := context.Background()
	s, store, ingestor := wikiFixture(t)
	doc := ingestWiki(t, s, ingestor, "alice", "源资料.txt", "用于核对的来源记录")
	for i := 0; i < 257; i++ {
		body := "普通笔记"
		if i == 256 {
			body = "喜欢冰岛极光摄影"
		}
		_, err := store.SaveWikiPage(ctx, WikiPage{ID: fmt.Sprintf("zz-page-%04d", i), OwnerID: "alice", Title: "资料", Body: body, Sources: []WikiSource{{DocumentID: doc.ID, Version: SourceVersion(doc)}}}, 0)
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var input struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		data := []map[string]any{}
		for i, text := range input.Input {
			v := []float32{0, 1}
			if strings.Contains(text, "冰岛") || strings.Contains(text, "Iceland") {
				v = []float32{1, 0}
			}
			data = append(data, map[string]any{"index": i, "embedding": v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer model.Close()
	c := semantic.New(semantic.Config{BaseURL: model.URL, EmbeddingModel: "fixture", Dimensions: 2})
	s.SetKnowledge(c, true)
	if err := s.IndexKnowledge(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	for {
		worked, err := c.ProcessProjection(ctx, func(context.Context, semantic.Projection) (bool, error) { return true, nil })
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	before := calls
	res, err := s.SearchWiki(ctx, "alice", "", "Iceland aurora", 8, 6000)
	if err != nil || len(res.Hits) != 1 || res.Hits[0].Page.ID != "zz-page-0256" || res.Degraded || calls != before+1 {
		t.Fatal("dense candidate tail lost or source embedded during query", res, err, calls-before)
	}
}
