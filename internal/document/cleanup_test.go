package document

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cleanupTestStore struct {
	jobs      map[string]CleanupJob
	completed map[string]bool
}

func (s *cleanupTestStore) ClaimCleanupJobByID(_ context.Context, jobID, _ string, _ time.Time, _ time.Duration) (CleanupJob, error) {
	job, ok := s.jobs[jobID]
	if !ok || s.completed[jobID] {
		return CleanupJob{}, ErrNoCleanupJob
	}
	return job, nil
}

func (s *cleanupTestStore) ClaimCleanupJob(context.Context, string, time.Time, time.Duration) (CleanupJob, error) {
	return CleanupJob{}, ErrNoCleanupJob
}

func (s *cleanupTestStore) CompleteCleanupJob(_ context.Context, job CleanupJob, _ string, _ time.Time) error {
	s.completed[job.ID] = true
	return nil
}

func (s *cleanupTestStore) FailCleanupJob(context.Context, CleanupJob, string, error, time.Time) error {
	return errors.New("unexpected cleanup failure")
}

func TestCleanerClaimsKafkaTargetAndDeletesExternalState(t *testing.T) {
	ctx := context.Background()
	store := &cleanupTestStore{jobs: map[string]CleanupJob{
		"cleanup-1": {ID: "cleanup-1", DocumentID: "document-1", UserID: "user-1", StorageKey: "user-1/one.txt"},
		"cleanup-2": {ID: "cleanup-2", DocumentID: "document-2", UserID: "user-1", StorageKey: "user-1/two.txt"},
	}, completed: map[string]bool{}}
	blobs := NewMemoryBlobStore()
	_, _ = blobs.Put(ctx, "user-1/one.txt", []byte("one"))
	_, _ = blobs.Put(ctx, "user-1/two.txt", []byte("two"))
	index := &memoryIndex{deleted: map[string]bool{}}
	cleaner := NewCleaner(store, blobs, index, "worker-1")
	processed, err := cleaner.RunJob(ctx, "cleanup-2")
	if err != nil || !processed {
		t.Fatalf("RunJob processed=%v err=%v", processed, err)
	}
	if store.completed["cleanup-1"] || !store.completed["cleanup-2"] || index.deleted["document-1"] || !index.deleted["document-2"] {
		t.Fatalf("completed=%v deleted=%v", store.completed, index.deleted)
	}
	if !blobs.Exists("user-1/one.txt") || blobs.Exists("user-1/two.txt") {
		t.Fatal("cleaner deleted the wrong blob")
	}
}
