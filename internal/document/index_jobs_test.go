package document

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/semantic"
)

func TestDeferredDocumentIndexPublishesAfterSourceCommit(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	index := &memoryIndex{deleted: map[string]bool{}}
	s := NewService(store, blobs, 0)
	s.SetVectorIndex(index)
	s.SetKnowledge(semantic.New(semantic.Config{}), true)
	ingestor := NewIngestor(store, blobs, fixedParser{}, index, "fixture")
	ingestor.SetDeferredIndex(true)
	d, _, err := s.Upload(ctx, "alice", "资料.txt", []byte("火星预算为一百元"))
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := ingestor.RunJob(ctx, d.JobID); err != nil || !worked {
		t.Fatal(worked, err)
	}
	ready, _, err := s.KnowledgeChunks(ctx, "alice", "", d.ID)
	if err != nil || ready.Status != "ready" || len(index.hits) != 0 {
		t.Fatal("index precedes authoritative source commit", err)
	}
	if pending, err := s.DocumentIndexPending(ctx, "alice", "", nil); err != nil || !pending {
		t.Fatal(pending, err)
	}
	listed, err := s.List(ctx, "alice")
	if err != nil || listed[0].IndexState != "pending" {
		t.Fatal("parsed source shown as indexed", listed, err)
	}
	if err = s.QueueDocumentIndexes(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.ProcessDocumentIndex(ctx); err != nil || !worked {
		t.Fatal(worked, err)
	}
	if pending, err := s.DocumentIndexPending(ctx, "alice", "", nil); err != nil || pending {
		t.Fatal(pending, err)
	}
	listed, err = s.List(ctx, "alice")
	if err != nil || listed[0].IndexState != "ready" {
		t.Fatal("completed index state missing", listed, err)
	}
	result, err := s.Query(ctx, "alice", QueryInput{Query: "火星预算"})
	if err != nil || len(result.Citations) != 1 {
		t.Fatal(result, err)
	}
	if err = s.QueueDocumentIndexes(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.ProcessDocumentIndex(ctx); err != nil || worked {
		t.Fatal("completed index job repeated", worked, err)
	}
}

type deletingProjectionIndex struct {
	memoryIndex
	store *MemoryStore
}

func (i *deletingProjectionIndex) Upsert(ctx context.Context, d Document, chunks []Chunk) error {
	if err := i.memoryIndex.Upsert(ctx, d, chunks); err != nil {
		return err
	}
	return i.store.DeleteDocument(ctx, d.UserID, d.ID, time.Now().UTC())
}

func TestDeletionDuringPublicationCompensatesLateVector(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	index := &deletingProjectionIndex{memoryIndex: memoryIndex{deleted: map[string]bool{}}, store: store}
	s := NewService(store, blobs, 0)
	s.SetVectorIndex(index)
	s.SetKnowledge(semantic.New(semantic.Config{}), true)
	ingestor := NewIngestor(store, blobs, fixedParser{}, index, "fixture")
	ingestor.SetDeferredIndex(true)
	d, _, err := s.Upload(ctx, "alice", "资料.txt", []byte("火星预算"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ingestor.RunJob(ctx, d.JobID); err != nil {
		t.Fatal(err)
	}
	if err = s.QueueDocumentIndexes(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.ProcessDocumentIndex(ctx); err != nil || !worked || !index.deleted[d.ID] {
		t.Fatal("late write was not removed", worked, err)
	}
}

type failingProjectionIndex struct {
	memoryIndex
	fail bool
}

func (i *failingProjectionIndex) Upsert(ctx context.Context, d Document, chunks []Chunk) error {
	if i.fail {
		return errors.New("candidate offline")
	}
	return i.memoryIndex.Upsert(ctx, d, chunks)
}

func TestShadowCandidateFailureIsDurablyRetried(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	legacy := &memoryIndex{deleted: map[string]bool{}}
	candidate := &failingProjectionIndex{memoryIndex: memoryIndex{deleted: map[string]bool{}}, fail: true}
	index := &RolloutIndex{Legacy: legacy, Candidate: candidate, Mode: "shadow"}
	s := NewService(store, blobs, 0)
	s.SetVectorIndex(index)
	client := semantic.New(semantic.Config{})
	s.SetKnowledge(client, true)
	ingestor := NewIngestor(store, blobs, fixedParser{}, index, "fixture")
	ingestor.SetDeferredIndex(true)
	d, _, err := s.Upload(ctx, "alice", "资料.txt", []byte("火星预算"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ingestor.RunJob(ctx, d.JobID); err != nil {
		t.Fatal(err)
	}
	if err = s.QueueDocumentIndexes(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.ProcessDocumentIndex(ctx); !worked || err == nil {
		t.Fatal("shadow failure was swallowed", worked, err)
	}
	if pending, err := s.DocumentIndexPending(ctx, "alice", "", nil); err != nil || !pending {
		t.Fatal(pending, err)
	}
	candidate.fail = false
	if _, err = client.ProjectionStore().Rebuild(ctx, []string{s.DocumentIndexSpec()}); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.ProcessDocumentIndex(ctx); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if pending, err := s.DocumentIndexPending(ctx, "alice", "", nil); err != nil || pending {
		t.Fatal("candidate retry incomplete", pending, err)
	}
}

func TestDeletedDocumentCannotBeRepublishedByDeferredJob(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	index := &memoryIndex{deleted: map[string]bool{}}
	s := NewService(store, blobs, 0)
	s.SetVectorIndex(index)
	s.SetKnowledge(semantic.New(semantic.Config{}), true)
	ingestor := NewIngestor(store, blobs, fixedParser{}, index, "fixture")
	ingestor.SetDeferredIndex(true)
	d, _, err := s.Upload(ctx, "alice", "资料.txt", []byte("火星预算"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ingestor.RunJob(ctx, d.JobID); err != nil {
		t.Fatal(err)
	}
	if err = s.QueueDocumentIndexes(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "alice", d.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.ProcessDocumentIndex(ctx); err != nil || !worked || len(index.hits) != 0 {
		t.Fatal("deleted source published", worked, err)
	}
}
