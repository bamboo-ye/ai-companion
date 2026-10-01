package document

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/productknowledge"
)

func TestEvidenceValidationBindsOwnerVersionAndOriginalChunk(t *testing.T) {
	s, store, ingestor := wikiFixture(t)
	doc := ingestWiki(t, s, ingestor, "alice", "budget.txt", "预算：100元")
	ctx := context.Background()
	chunks, _ := store.ListDocumentChunks(ctx, "alice", doc.ID, 10)
	ref := EvidenceReference{SourceID: doc.ID, ChunkID: chunks[0].ID, Text: chunks[0].Content}
	versions, err := s.ValidateEvidence(ctx, "alice", []EvidenceReference{ref})
	if err != nil || versions[doc.ID] != SourceVersion(doc) {
		t.Fatalf("%v %v", versions, err)
	}
	ref.Version = versions[doc.ID]
	for _, mutate := range []func(*EvidenceReference){
		func(r *EvidenceReference) { r.Version = "old" },
		func(r *EvidenceReference) { r.Text = "伪造原文" },
		func(r *EvidenceReference) { r.ChunkID = "foreign" },
		func(r *EvidenceReference) { r.SourceID = "" },
	} {
		bad := ref
		mutate(&bad)
		if _, err := s.ValidateEvidence(ctx, "alice", []EvidenceReference{bad}); err == nil {
			t.Fatal("accepted invalid evidence")
		}
	}
	if _, err := s.ValidateEvidence(ctx, "bob", []EvidenceReference{ref}); err == nil {
		t.Fatal("cross-user evidence")
	}
	store.mu.Lock()
	changed := store.items[doc.ID]
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	store.items[doc.ID] = changed
	store.mu.Unlock()
	if _, err := s.ValidateEvidence(ctx, "alice", []EvidenceReference{ref}); err == nil {
		t.Fatal("stale evidence")
	}
	ref.Version = ""
	if err := s.Delete(ctx, "alice", doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateEvidence(ctx, "alice", []EvidenceReference{ref}); err == nil {
		t.Fatal("deleted evidence")
	}
}

func TestEvidenceValidationBoundsInputAndChecksBuiltinRevision(t *testing.T) {
	s, _, _ := wikiFixture(t)
	hits, err := productknowledge.Search("文档", 1)
	if err != nil || len(hits) == 0 {
		t.Fatalf("%v", err)
	}
	page, err := productknowledge.Read(hits[0].Page.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := EvidenceReference{SourceID: "product:" + page.ID, Version: page.Version, Text: page.Body}
	if _, err := s.ValidateEvidence(context.Background(), "alice", []EvidenceReference{ref}); err != nil {
		t.Fatal(err)
	}
	ref.Version = "outdated"
	if _, err := s.ValidateEvidence(context.Background(), "alice", []EvidenceReference{ref}); err == nil {
		t.Fatal("outdated builtin")
	}
	if _, err := s.ValidateEvidence(context.Background(), "alice", make([]EvidenceReference, 49)); err == nil {
		t.Fatal("unbounded references")
	}
	ref.Text = strings.Repeat("x", 96001)
	if _, err := s.ValidateEvidence(context.Background(), "alice", []EvidenceReference{ref}); err == nil {
		t.Fatal("unbounded text")
	}
}
