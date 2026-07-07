package eventbus

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Relay struct {
	store       Store
	publisher   Publisher
	workerID    string
	lease       time.Duration
	batchSize   int
	maxAttempts int
	now         func() time.Time
	onError     func(error)
}

func (r *Relay) SetErrorHandler(handler func(error)) { r.onError = handler }

func NewRelay(store Store, publisher Publisher, workerID string, lease time.Duration, batchSize, maxAttempts int) *Relay {
	if batchSize <= 0 {
		batchSize = 50
	}
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	return &Relay{store: store, publisher: publisher, workerID: workerID, lease: lease, batchSize: batchSize, maxAttempts: maxAttempts, now: time.Now}
}

func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	events, err := r.store.ClaimOutboxEvents(ctx, r.workerID, r.now().UTC(), r.lease, r.batchSize)
	if errors.Is(err, ErrNoEvents) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var lastErr error
	for _, event := range events {
		ack, publishErr := r.publisher.Publish(ctx, event)
		now := r.now().UTC()
		if publishErr == nil {
			publishErr = r.store.MarkOutboxPublished(ctx, event.ID, r.workerID, ack, now)
		}
		if publishErr == nil {
			continue
		}
		lastErr = publishErr
		message := publishErr.Error()
		if len(message) > 1024 {
			message = message[:1024]
		}
		dead := event.Attempts >= r.maxAttempts
		_ = r.store.FailOutboxEvent(ctx, event.ID, r.workerID, strings.TrimSpace(message), now.Add(retryDelay(event.Attempts)), dead)
	}
	return len(events), lastErr
}

func (r *Relay) Run(ctx context.Context, pollInterval time.Duration) error {
	if r.store == nil || r.publisher == nil || strings.TrimSpace(r.workerID) == "" || r.lease <= 0 {
		return ErrConflict
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		processed, err := r.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && r.onError != nil {
			r.onError(err)
		}
		if processed > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		attempt = 9
	}
	delay := time.Second * time.Duration(1<<(attempt-1))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
