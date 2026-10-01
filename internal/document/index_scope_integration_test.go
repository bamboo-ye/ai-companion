package document

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestVersionScopedQdrantAndRetiredCollectionCleanup(t *testing.T) {
	base := os.Getenv("QDRANT_TEST_URL")
	if base == "" {
		t.Skip("QDRANT_TEST_URL is not set")
	}
	ctx := context.Background()
	root := "scope_test_" + time.Now().Format("150405000000")
	active := NewQdrantIndex(base, root, "", 5*time.Second)
	retired := NewQdrantIndex(base, root+"_semantic_abcdef123456", "", 5*time.Second)
	unrelated := NewQdrantIndex(base, root+"_unrelated", "", 5*time.Second)
	for _, q := range []*QdrantIndex{active, retired, unrelated} {
		if err := q.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		defer func(q *QdrantIndex) { _, _, _ = q.do(ctx, http.MethodDelete, "/collections/"+q.collection, nil) }(q)
	}
	old := Document{ID: "document", UserID: "alice", Name: "预算", Status: "ready", ChunkCount: 1, UpdatedAt: time.Now().UTC()}
	chunk := Chunk{ID: "chunk", PointID: deterministicUUID("scope-point"), Content: "火星预算为一百元", PageStart: 1, PageEnd: 1}
	if err := active.Upsert(ctx, old, []Chunk{chunk}); err != nil {
		t.Fatal(err)
	}
	current := old
	current.UpdatedAt = current.UpdatedAt.Add(time.Second)
	chunk.Content = "火星预算为二百元"
	for _, q := range []*QdrantIndex{active, retired, unrelated} {
		if err := q.Upsert(ctx, current, []Chunk{chunk}); err != nil {
			t.Fatal(err)
		}
	}
	hits, err := active.SearchScoped(ctx, "alice", "火星预算", map[string]string{current.ID: SourceVersion(current)}, 8)
	if err != nil || len(hits) != 1 || hits[0].Content != chunk.Content || hits[0].SourceRevision != SourceVersion(current) {
		t.Fatal("revision filter", hits, err)
	}
	hits, err = active.SearchScoped(ctx, "bob", "火星预算", map[string]string{current.ID: SourceVersion(current)}, 8)
	if err != nil || len(hits) != 0 {
		t.Fatal("tenant filter", hits, err)
	}
	active.managedRoot = root
	if err = active.DeleteDocument(ctx, "alice", current.ID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []*QdrantIndex{active, retired} {
		hits, err = q.Search(ctx, "alice", "火星预算", []string{current.ID}, 8)
		if err != nil || len(hits) != 0 {
			t.Fatal("retired source remains", q.collection, hits, err)
		}
	}
	hits, err = unrelated.Search(ctx, "alice", "火星预算", []string{current.ID}, 8)
	if err != nil || len(hits) != 1 {
		t.Fatal("unrelated collection changed", hits, err)
	}
}
