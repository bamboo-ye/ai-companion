package skill

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Runner struct {
	service       *Service
	workerID      string
	lease         time.Duration
	renewInterval time.Duration
	slots         chan struct{}
	parseSlots    chan struct{}
	renderSlots   chan struct{}
}

func NewRunner(service *Service, workerID string, lease, renewInterval time.Duration) *Runner {
	return &Runner{service: service, workerID: workerID, lease: lease, renewInterval: renewInterval, slots: make(chan struct{}, 1)}
}

// SetMaxConcurrency must be called before starting the runner. Polling and
// event-driven RunID calls share the same permits, acquired before claiming a
// lease so queued work cannot expire while waiting for execution capacity.
func (r *Runner) SetMaxConcurrency(limit int) {
	r.slots = make(chan struct{}, max(1, min(limit, 32)))
}

// Configure resource classes before Run. The overall execution permit still
// bounds every class; leases are renewed while waiting for a class permit.
func (r *Runner) SetResourceConcurrency(parsing, rendering int) {
	r.parseSlots = make(chan struct{}, max(1, min(parsing, 32)))
	r.renderSlots = make(chan struct{}, max(1, min(rendering, 32)))
}

func (r *Runner) acquireResource(ctx context.Context, name string) (func(), error) {
	var slots chan struct{}
	switch name {
	case "office.document_extract", "office.tabular_profile":
		slots = r.parseSlots
	case "office.pptx_generate", "office.pptx_outline", "office.pdf_translate", "office.docx_edit":
		slots = r.renderSlots
	}
	if slots == nil {
		return func() {}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Runner) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) RunOnce(ctx context.Context) (bool, error) {
	if r.lease <= 0 || r.renewInterval <= 0 || r.renewInterval >= r.lease {
		return false, ErrValidation
	}
	if err := r.acquire(ctx); err != nil {
		return false, err
	}
	defer func() { <-r.slots }()
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
	if err := r.acquire(ctx); err != nil {
		return false, err
	}
	defer func() { <-r.slots }()
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
	renewDone := make(chan struct{})
	go func() {
		defer close(renewDone)
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
	release, executeErr := r.acquireResource(executeCtx, run.SkillName)
	if executeErr == nil {
		_, executeErr = r.service.ExecuteClaimed(executeCtx, run, r.workerID)
		release()
	}
	close(done)
	cancel()
	<-renewDone
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
	var workers sync.WaitGroup
	for i := 0; i < cap(r.slots); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r.poll(ctx, pollInterval)
		}()
	}
	workers.Wait()
	return nil
}

func (r *Runner) poll(ctx context.Context, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		processed, _ := r.RunOnce(ctx)
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
