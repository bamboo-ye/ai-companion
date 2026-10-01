package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProjectionQdrantIntegration(t *testing.T) {
	base := os.Getenv("QDRANT_TEST_URL")
	if base == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		data := []map[string]any{}
		for i := range in.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer model.Close()
	ctx := context.Background()
	c := New(Config{BaseURL: model.URL, EmbeddingModel: "fixture", Dimensions: 2})
	q := NewQdrantProjectionIndex(base, "projection_test_"+digest(time.Now().String())[:12], "", 2)
	c.SetProjectionIndex(q)
	defer func() {
		_, _ = q.call(ctx, http.MethodDelete, "/collections/"+q.collection(c.ProjectionSpec()), nil, nil)
	}()
	valid := func(context.Context, Projection) (bool, error) { return true, nil }
	for _, source := range []struct{ owner, id, body string }{{"alice", "long", "完整资料" + strings.Repeat("内容", 1800)}, {"alice", "short", "另一条来源"}, {"bob", "private", "私有来源"}} {
		if err := c.QueueSource(ctx, source.owner, "wiki", source.id, "v1", source.body); err != nil {
			t.Fatal(err)
		}
	}
	for {
		worked, err := c.ProcessProjection(ctx, valid)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	items := append(c.SourceProjections("alice", "wiki", "long", "v1", "完整资料"+strings.Repeat("内容", 1800)), c.SourceProjections("alice", "wiki", "short", "v1", "另一条来源")...)
	scores, pending, err := c.StoredScores(ctx, "来源", items)
	if err != nil || pending || len(scores) != 2 || scores["private"] != 0 {
		t.Fatal("ANN scope/grouping", scores, pending, err)
	}
	changed := c.SourceProjections("alice", "wiki", "short", "v2", "另一条来源")
	scores, pending, err = c.StoredScores(ctx, "来源", changed)
	if err != nil || !pending || len(scores) != 0 {
		t.Fatal("stale revision entered ANN", scores, err)
	}
	obsolete := c.SourceProjections("alice", "memory", "obsolete", "v1", "待删除来源")
	if err = c.ProjectionStore().Queue(ctx, obsolete, time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if worked, err := c.ProcessProjection(ctx, valid); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if n, err := c.PruneProjections(ctx, time.Now().Add(-24*time.Hour)); err != nil || n != 1 {
		t.Fatal("prune", n, err)
	}
	left, err := q.Search(ctx, []float32{1, 0}, obsolete)
	if err != nil || len(left) != 0 {
		t.Fatal("orphan vector remains", left, err)
	}
	// Recover a lost collection without changing the source or model identity.
	if _, err = q.call(ctx, http.MethodDelete, "/collections/"+q.collection(c.ProjectionSpec()), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ProjectionStore().Rebuild(ctx, []string{c.ProjectionSpec()}); err != nil {
		t.Fatal(err)
	}
	if worked, err := c.ProcessProjection(ctx, valid); !worked || err == nil {
		t.Fatal("lost collection was not detected", worked, err)
	}
	if _, err = c.ProjectionStore().Rebuild(ctx, []string{c.ProjectionSpec()}); err != nil {
		t.Fatal(err)
	}
	for {
		worked, err := c.ProcessProjection(ctx, valid)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	scores, pending, err = c.StoredScores(ctx, "来源", items)
	if err != nil || pending || len(scores) != 2 {
		t.Fatal("rebuild did not recover ANN", scores, pending, err)
	}
}
