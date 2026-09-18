package document

import (
	"context"
	"encoding/json"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSemanticQdrantIntegration(t *testing.T) {
	base := os.Getenv("QDRANT_TEST_URL")
	if base == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		data := []map[string]any{}
		for i, text := range input.Input {
			v := []float32{1, 0}
			if strings.Contains(text, "跑步") {
				v = []float32{0, 1}
			}
			data = append(data, map[string]any{"index": i, "embedding": v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer model.Close()
	collection := "context_test_" + strings.ReplaceAll(time.Now().UTC().Format("150405.000000"), ".", "")
	index := NewQdrantIndex(base, collection, "", 5*time.Second)
	index.semantic = semantic.New(semantic.Config{BaseURL: model.URL, EmbeddingModel: "test-multilingual", Dimensions: 2})
	ctx := context.Background()
	defer func() { _, _, _ = index.do(ctx, http.MethodDelete, "/collections/"+collection, nil) }()
	if err := index.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"alice", "bob"} {
		chunk := Chunk{ID: deterministicUUID(user + ":chunk"), PointID: deterministicUUID(user + ":point"), PageStart: 1, PageEnd: 1, Content: "不吃香菜", EmbeddingVersion: EmbeddingVersion}
		if err := index.Upsert(ctx, Document{ID: user + "-doc", UserID: user, Name: "偏好"}, []Chunk{chunk}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := index.Search(ctx, "alice", "coriander restriction", []string{"alice-doc"}, 5)
	if err != nil || len(hits) != 1 || hits[0].DocumentID != "alice-doc" || !hits[0].Semantic {
		t.Fatalf("semantic hits=%+v err=%v", hits, err)
	}
	if err = index.DeleteDocument(ctx, "alice", "alice-doc"); err != nil {
		t.Fatal(err)
	}
	hits, err = index.Search(ctx, "alice", "coriander", []string{"alice-doc"}, 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("deleted hits=%+v %v", hits, err)
	}
}
