package skill

import (
	"context"
	"errors"
	"time"
)

type Runner struct {
	service       *Service
	workerID      string
	lease         time.Duration
	renewInterval time.Duration
}

func NewRunner(service *Service, workerID string, lease, renewInterval time.Duration) *Runner {
	return &Runner{service: service, workerID: workerID, lease: lease, renewInterval: renewInterval}
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	if r.lease <= 0 || r.renewInterval <= 0 || r.renewInterval >= r.lease {
		return false, ErrValidation
	}
	run, err := r.service.Claim(ctx, r.workerID, r.lease)
	if errors.Is(err, ErrNoQueuedRun) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return r.execute(ctx, run)
}

func (r *Runner) RunID(ctx context.Context, runID string) (bool, error) {
	if r.lease <= 0 || r.renewInterval <= 0 || r.renewInterval >= r.lease {
		return false, ErrValidation
	}
	run, err := r.service.ClaimByID(ctx, runID, r.workerID, r.lease)
	if errors.Is(err, ErrNoQueuedRun) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return r.execute(ctx, run)
}

func (r *Runner) execute(ctx context.Context, run Run) (bool, error) {
	executeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	renewErrors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(r.renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-executeCtx.Done():
				return
			case <-ticker.C:
				if renewErr := r.service.RenewLease(executeCtx, run, r.workerID, r.lease); renewErr != nil {
					renewErrors <- renewErr
					cancel()
					return
				}
			}
		}
	}()
	_, executeErr := r.service.ExecuteClaimed(executeCtx, run, r.workerID)
	close(done)
	cancel()
	select {
	case renewErr := <-renewErrors:
		return true, renewErr
	default:
	}
	if ctx.Err() != nil {
		return true, nil
	}
	return true, executeErr
}

func (r *Runner) Run(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	if r.lease <= 0 || r.renewInterval <= 0 || r.renewInterval >= r.lease {
		return ErrValidation
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		processed, _ := r.RunOnce(ctx)
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
