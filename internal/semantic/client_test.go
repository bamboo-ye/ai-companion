package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddingOrderAndProviderContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Error("unexpected path")
		}
		var input map[string]any
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input["model"] != "test-multilingual" || input["dimensions"] != float64(2) {
			t.Error("missing model configuration")
		}
		_, _ = w.Write([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}],"usage":{"total_tokens":12}}`))
	}))
	defer server.Close()
	c := New(Config{BaseURL: server.URL, EmbeddingModel: "test-multilingual", Dimensions: 2})
	vectors, err := c.Embed(context.Background(), []string{"香菜", "coriander"})
	if err != nil || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors %v %v", vectors, err)
	}
	if c.Metrics().Tokens != 12 {
		t.Fatal("usage missing")
	}
}
func TestEmbeddingRejectsPartialOrMalformedVectors(t *testing.T) {
	for _, body := range []string{`{"data":[]}`, `{"data":[{"index":0,"embedding":[0,0]}]}`, `{"data":[{"index":0,"embedding":[1]}]}`, `{"data":[{"index":-1,"embedding":[1,0]}]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			c := New(Config{BaseURL: server.URL, EmbeddingModel: "test", Dimensions: 2})
			if _, err := c.Embed(context.Background(), []string{"data"}); err == nil {
				t.Fatal("accepted malformed embedding")
			}
		})
	}
}
