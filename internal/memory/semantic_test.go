package memory

import (
	"context"
	"encoding/json"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSemanticRecallCorrectionAndDeletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		data := []map[string]any{}
		for i := range input.Input {
			data = append(data, map[string]any{"index": i, "embedding": []int{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	store := NewMemoryStore()
	s := NewService(store)
	client := semantic.New(semantic.Config{BaseURL: server.URL, EmbeddingModel: "test", Dimensions: 2})
	s.SetSemanticClient(client)
	ctx := context.Background()
	old, err := s.Create(ctx, "alice", "不吃香菜")
	if err != nil {
		t.Fatal(err)
	}
	index := func() {
		t.Helper()
		if err := s.IndexKnowledge(ctx, "alice"); err != nil {
			t.Fatal(err)
		}
		for {
			did, err := client.ProcessProjection(ctx, func(ctx context.Context, p semantic.Projection) (bool, error) {
				item, err := s.KnowledgeMemory(ctx, p.Owner, p.SourceID)
				return err == nil && KnowledgeRevision(item) == p.Revision, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !did {
				break
			}
		}
	}
	pending, _ := s.RecallContext(ctx, "alice", "coriander allergy", 8)
	if len(pending) != 0 {
		t.Fatal("unindexed content must not be embedded during query")
	}
	index()
	hits, err := s.RecallContext(ctx, "alice", "coriander allergy", 8)
	if err != nil || len(hits) != 1 {
		t.Fatalf("semantic recall %+v %v", hits, err)
	}
	next, err := s.Correct(ctx, "alice", old.ID, "现在可以吃香菜")
	if err != nil || next.SupersedesID != old.ID || next.ID == old.ID {
		t.Fatalf("correction %+v %v", next, err)
	}
	stored := store.items[old.ID]
	if stored.Status != "superseded" || stored.ValidTo == nil {
		t.Fatal("old interval not closed")
	}
	pending, _ = s.RecallContext(ctx, "alice", "coriander", 8)
	if len(pending) != 0 {
		t.Fatal("superseded projection reused for new revision")
	}
	index()
	hits, _ = s.RecallContext(ctx, "alice", "coriander", 8)
	if len(hits) != 1 || hits[0].Source.ID != next.ID {
		t.Fatal("old vector leaked")
	}
	if _, err = s.Correct(ctx, "bob", next.ID, "攻击修改"); err != ErrNotFound {
		t.Fatal("cross-user correction")
	}
	if err = s.Delete(ctx, "alice", next.ID); err != nil {
		t.Fatal(err)
	}
	hits, _ = s.RecallContext(ctx, "alice", "coriander", 8)
	if len(hits) != 0 {
		t.Fatal("deleted cached memory")
	}
}
