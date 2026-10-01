package semantic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIndexIdentityPartitionsModelAndPreprocessing(t *testing.T) {
	base := Config{BaseURL: "https://user:secret@example.test/v1?key=secret", EmbeddingModel: "model", Dimensions: 2}
	c := New(base)
	serialized, _ := json.Marshal(c.IndexSpec())
	if strings.Contains(string(serialized), "secret") {
		t.Fatal("credential exposed")
	}
	for _, change := range []func(*Config){func(c *Config) { c.EmbeddingRevision = "r2" }, func(c *Config) { c.QueryInstruction = "retrieve" }, func(c *Config) { c.PreprocessingVersion = "v3" }, func(c *Config) { c.BaseURL = "https://other.test" }, func(c *Config) { c.Dimensions = 3 }} {
		next := base
		change(&next)
		if New(next).Version() == c.Version() {
			t.Fatal("index version collision")
		}
	}
	base.APIKey = "rotation"
	base.BaseURL = "https://example.test/v1"
	if New(base).Version() != c.Version() {
		t.Fatal("credential rotation invalidated same model")
	}
}
func TestProjectionsBackgroundOnlyAndFullSource(t *testing.T) {
	ctx := context.Background()
	inputs := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		inputs = append(inputs, in.Input...)
		data := []map[string]any{}
		for i := range in.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	c := New(Config{BaseURL: server.URL, EmbeddingModel: "fixture", Dimensions: 2, QueryInstruction: "查找证据"})
	body := strings.Repeat("中", 3100) + "尾部重要事实"
	items := c.SourceProjections("alice", "wiki", "page", "v1", body)
	if len(items) != 3 || !strings.HasSuffix(items[2].Text, "尾部重要事实") {
		t.Fatal("tail lost")
	}
	if _, pending, err := c.StoredScores(ctx, "query", items); err != nil || !pending || len(inputs) != 0 {
		t.Fatal("query embedded source", inputs, err)
	}
	if err := c.QueueSource(ctx, "alice", "wiki", "page", "v1", body); err != nil {
		t.Fatal(err)
	}
	for range items {
		if done, err := c.ProcessProjection(ctx, func(context.Context, Projection) (bool, error) { return true, nil }); !done || err != nil {
			t.Fatal(done, err)
		}
	}
	if len(inputs) != 3 {
		t.Fatal(inputs)
	}
	scores, pending, err := c.StoredScores(ctx, "query", items)
	if err != nil || pending || scores["page"] != 1 || len(inputs) != 4 || inputs[3] != "查找证据\nquery" {
		t.Fatal(scores, pending, err, inputs)
	}
	if err = c.QueueSource(ctx, "alice", "wiki", "page", "v1", body); err != nil {
		t.Fatal(err)
	}
	if done, err := c.ProcessProjection(ctx, func(context.Context, Projection) (bool, error) { return true, nil }); done || err != nil {
		t.Fatal("duplicate embedding")
	}
	changed := c.SourceProjections("alice", "wiki", "page", "v2", body)
	scores, pending, err = c.StoredScores(ctx, "query", changed)
	if err != nil || !pending || len(scores) != 0 || len(inputs) != 4 {
		t.Fatal("stale projection leaked")
	}
}
func TestProjectionLeaseFencingRetriesAndDeletion(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryProjectionStore()
	now := time.Now().UTC()
	p := Projection{Key: "key", Owner: "alice", Kind: "memory", SourceID: "id", Spec: "spec"}
	_ = s.Queue(ctx, []Projection{p}, now)
	first, ok, err := s.Claim(ctx, "spec", now)
	if !ok || err != nil {
		t.Fatal(err)
	}
	if _, ok, _ = s.Claim(ctx, "spec", now); ok {
		t.Fatal("double claim")
	}
	now = now.Add(3 * time.Minute)
	next, ok, err := s.Claim(ctx, "spec", now)
	if !ok || err != nil || next.Lease == first.Lease {
		t.Fatal("lease recovery", err)
	}
	if err = s.Finish(ctx, first, []float32{1, 0}, "", now); !errors.Is(err, ErrProjectionLease) {
		t.Fatal("stale worker published")
	}
	for {
		if err = s.Finish(ctx, next, nil, "unavailable", now); err != nil {
			t.Fatal(err)
		}
		if next.Attempts >= 5 {
			break
		}
		now = now.Add(time.Hour)
		next, ok, err = s.Claim(ctx, "spec", now)
		if !ok || err != nil {
			t.Fatal(err)
		}
	}
	stats, _ := s.Stats(ctx)
	if stats["failed"] != 1 {
		t.Fatal(stats)
	}
	n, err := s.Retry(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	now = now.Add(time.Hour)
	next, ok, err = s.Claim(ctx, "spec", now)
	if !ok || err != nil || next.Attempts != 1 {
		t.Fatal(next, err)
	}
	_ = s.DeleteSource(ctx, "memory", "bob", "id")
	if err = s.Finish(ctx, next, []float32{1, 0}, "", now); err != nil {
		t.Fatal(err)
	}
	_ = s.DeleteSource(ctx, "memory", "alice", "id")
	found, _ := s.Lookup(ctx, []string{"key"})
	if len(found) != 0 {
		t.Fatal("deleted projection remains")
	}
}
