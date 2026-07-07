package document

import (
	"context"
	"errors"
	"time"
)

var ErrNoCleanupJob = errors.New("no document cleanup job")

type CleanupJob struct {
	ID         string
	DocumentID string
	UserID     string
	StorageKey string
	Attempts   int
}

type CleanupStore interface {
	ClaimCleanupJobByID(context.Context, string, string, time.Time, time.Duration) (CleanupJob, error)
	ClaimCleanupJob(context.Context, string, time.Time, time.Duration) (CleanupJob, error)
	CompleteCleanupJob(context.Context, CleanupJob, string, time.Time) error
	FailCleanupJob(context.Context, CleanupJob, string, error, time.Time) error
}

type Cleaner struct {
	store    CleanupStore
	blobs    BlobStore
	index    VectorIndex
	workerID string
	now      func() time.Time
}

func NewCleaner(store CleanupStore, blobs BlobStore, index VectorIndex, workerID string) *Cleaner {
	return &Cleaner{store: store, blobs: blobs, index: index, workerID: workerID, now: time.Now}
}

func (c *Cleaner) RunJob(ctx context.Context, jobID string) (bool, error) {
	job, err := c.store.ClaimCleanupJobByID(ctx, jobID, c.workerID, c.now().UTC(), 2*time.Minute)
	if errors.Is(err, ErrNoCleanupJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.process(ctx, job)
}

func (c *Cleaner) RunOnce(ctx context.Context) (bool, error) {
	job, err := c.store.ClaimCleanupJob(ctx, c.workerID, c.now().UTC(), 2*time.Minute)
	if errors.Is(err, ErrNoCleanupJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.process(ctx, job)
}

func (c *Cleaner) process(ctx context.Context, job CleanupJob) (bool, error) {
	if err := c.index.DeleteDocument(ctx, job.UserID, job.DocumentID); err != nil {
		_ = c.store.FailCleanupJob(ctx, job, c.workerID, err, c.now().UTC())
		return true, err
	}
	if err := c.blobs.Delete(ctx, job.StorageKey); err != nil {
		_ = c.store.FailCleanupJob(ctx, job, c.workerID, err, c.now().UTC())
		return true, err
	}
	return true, c.store.CompleteCleanupJob(ctx, job, c.workerID, c.now().UTC())
}

func (c *Cleaner) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, _ := c.RunOnce(ctx)
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
