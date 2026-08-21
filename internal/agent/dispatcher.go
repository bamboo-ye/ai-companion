package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// RunProcessor is the durable execution boundary used by RunDispatcher.
// ProcessRun must claim the persisted run before executing it, so duplicate
// queue entries remain harmless.
type RunProcessor interface {
	ProcessRun(context.Context, string) (bool, error)
}

type RunDispatcher struct {
	processor   RunProcessor
	concurrency int
	queue       chan dispatchedRun
	mu          sync.Mutex
	pending     map[string]*dispatchState
	now         func() time.Time
	onStart     func(string, time.Duration)
	onHint      func(RunDispatchHintObservation)
	onComplete  func(RunDispatchObservation)
	onError     func(string, error)
}

type dispatchState struct {
	inFlight bool
	replay   bool
}

type RunDispatchObservation struct {
	RunID             string
	QueueWait         time.Duration
	ExecutionDuration time.Duration
	Replay            bool
	Processed         bool
	Err               error
}

type RunDispatchHintOutcome string

const (
	RunDispatchHintEnqueued        RunDispatchHintOutcome = "enqueued"
	RunDispatchHintCoalesced       RunDispatchHintOutcome = "coalesced"
	RunDispatchHintReplayRequested RunDispatchHintOutcome = "replay_requested"
)

type RunDispatchHintObservation struct {
	RunID   string
	Outcome RunDispatchHintOutcome
}

type RunDispatcherStats struct {
	Queued         int
	Pending        int
	InFlight       int
	ReplaysPending int
}

type dispatchedRun struct {
	runID      string
	enqueuedAt time.Time
}

func NewRunDispatcher(processor RunProcessor, concurrency, queueSize int) *RunDispatcher {
	if concurrency < 1 {
		concurrency = 1
	}
	if queueSize < concurrency {
		queueSize = concurrency
	}
	return &RunDispatcher{
		processor: processor, concurrency: concurrency,
		queue:   make(chan dispatchedRun, queueSize),
		pending: make(map[string]*dispatchState), now: time.Now,
	}
}

func (d *RunDispatcher) SetErrorHandler(handler func(string, error)) { d.onError = handler }

func (d *RunDispatcher) SetStartHandler(handler func(string, time.Duration)) { d.onStart = handler }

func (d *RunDispatcher) SetHintHandler(handler func(RunDispatchHintObservation)) {
	d.onHint = handler
}

func (d *RunDispatcher) SetCompleteHandler(handler func(RunDispatchObservation)) {
	d.onComplete = handler
}

// Dispatch applies bounded backpressure. Once it returns nil the durable run
// may be acknowledged at the transport layer: a process crash can lose this
// in-memory hint, but the reconciler will still claim the persisted run.
func (d *RunDispatcher) Dispatch(ctx context.Context, runID string) error {
	if d == nil || d.processor == nil || strings.TrimSpace(runID) == "" {
		return ErrValidation
	}
	runID = strings.TrimSpace(runID)
	d.mu.Lock()
	if state, exists := d.pending[runID]; exists {
		// A duplicate queued hint is already represented by the original item.
		// A hint received while execution is in flight may represent a newer
		// durable transition, so retain one trailing replay instead of adding an
		// unbounded number of duplicate queue entries.
		outcome := RunDispatchHintCoalesced
		if state.inFlight && !state.replay {
			state.replay = true
			outcome = RunDispatchHintReplayRequested
		}
		d.mu.Unlock()
		d.observeHint(runID, outcome)
		return nil
	}
	d.pending[runID] = &dispatchState{}
	d.mu.Unlock()

	select {
	case d.queue <- dispatchedRun{runID: runID, enqueuedAt: d.now()}:
		d.observeHint(runID, RunDispatchHintEnqueued)
		return nil
	case <-ctx.Done():
		d.mu.Lock()
		delete(d.pending, runID)
		d.mu.Unlock()
		return ctx.Err()
	}
}

func (d *RunDispatcher) Stats() RunDispatcherStats {
	if d == nil {
		return RunDispatcherStats{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	stats := RunDispatcherStats{Queued: len(d.queue), Pending: len(d.pending)}
	for _, state := range d.pending {
		if state.inFlight {
			stats.InFlight++
		}
		if state.replay {
			stats.ReplaysPending++
		}
	}
	return stats
}

func (d *RunDispatcher) observeHint(runID string, outcome RunDispatchHintOutcome) {
	if d.onHint != nil {
		d.onHint(RunDispatchHintObservation{RunID: runID, Outcome: outcome})
	}
}

func (d *RunDispatcher) Run(ctx context.Context) error {
	if d == nil || d.processor == nil || d.concurrency < 1 || d.queue == nil {
		return ErrValidation
	}
	var workers sync.WaitGroup
	workers.Add(d.concurrency)
	for index := 0; index < d.concurrency; index++ {
		go func() {
			defer workers.Done()
			d.runWorker(ctx)
		}()
	}
	<-ctx.Done()
	workers.Wait()
	return nil
}

func (d *RunDispatcher) runWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-d.queue:
			d.processItem(ctx, item)
		}
	}
}

func (d *RunDispatcher) processItem(ctx context.Context, item dispatchedRun) {
	replay := false
	for {
		d.mu.Lock()
		state := d.pending[item.runID]
		if state == nil {
			state = &dispatchState{}
			d.pending[item.runID] = state
		}
		state.inFlight = true
		d.mu.Unlock()

		queueWait := d.now().Sub(item.enqueuedAt)
		if d.onStart != nil {
			d.onStart(item.runID, queueWait)
		}
		startedAt := d.now()
		processed, err := d.processor.ProcessRun(ctx, item.runID)
		if d.onComplete != nil {
			d.onComplete(RunDispatchObservation{
				RunID: item.runID, QueueWait: queueWait,
				ExecutionDuration: d.now().Sub(startedAt),
				Replay:            replay, Processed: processed, Err: err,
			})
		}
		if err != nil && !errors.Is(err, context.Canceled) && d.onError != nil {
			d.onError(item.runID, err)
		}

		d.mu.Lock()
		state = d.pending[item.runID]
		if state != nil && state.replay && ctx.Err() == nil {
			state.replay = false
			state.inFlight = false
			d.mu.Unlock()
			item.enqueuedAt = d.now()
			replay = true
			continue
		}
		delete(d.pending, item.runID)
		d.mu.Unlock()
		return
	}
}
