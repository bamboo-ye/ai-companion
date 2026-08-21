package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

type runProcessorFunc func(context.Context, string) (bool, error)

func (f runProcessorFunc) ProcessRun(ctx context.Context, runID string) (bool, error) {
	return f(ctx, runID)
}

func TestRunDispatcherAvoidsHeadOfLineBlocking(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	dispatcher := NewRunDispatcher(runProcessorFunc(func(ctx context.Context, runID string) (bool, error) {
		switch runID {
		case "run-slow":
			close(firstStarted)
			select {
			case <-releaseFirst:
				return true, nil
			case <-ctx.Done():
				return false, ctx.Err()
			}
		case "run-fast":
			close(secondStarted)
			return true, nil
		default:
			return false, ErrNotFound
		}
	}), 2, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()

	if err := dispatcher.Dispatch(ctx, "run-slow"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("slow run did not start")
	}
	if err := dispatcher.Dispatch(ctx, "run-fast"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fast run was blocked behind the slow run")
	}
	close(releaseFirst)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunDispatcherAppliesBoundedBackpressure(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	dispatcher := NewRunDispatcher(runProcessorFunc(func(ctx context.Context, _ string) (bool, error) {
		select {
		case <-started:
		default:
			close(started)
		}
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}), 1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	if err := dispatcher.Dispatch(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := dispatcher.Dispatch(ctx, "run-2"); err != nil {
		t.Fatal(err)
	}
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	err := dispatcher.Dispatch(deadlineCtx, "run-3")
	deadlineCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Dispatch() error = %v", err)
	}
	close(release)
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunDispatcherEmitsCompletionObservation(t *testing.T) {
	dispatcher := NewRunDispatcher(runProcessorFunc(func(context.Context, string) (bool, error) {
		time.Sleep(time.Millisecond)
		return true, nil
	}), 1, 1)
	observed := make(chan RunDispatchObservation, 1)
	dispatcher.SetCompleteHandler(func(observation RunDispatchObservation) {
		observed <- observation
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	if err := dispatcher.Dispatch(ctx, "observed-run"); err != nil {
		t.Fatal(err)
	}
	select {
	case observation := <-observed:
		if observation.RunID != "observed-run" || !observation.Processed || observation.Err != nil {
			t.Fatalf("completion observation = %#v", observation)
		}
		if observation.QueueWait < 0 || observation.ExecutionDuration < time.Millisecond {
			t.Fatalf("completion timing = %#v", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("completion observation was not emitted")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunDispatcherCoalescesDuplicatesWhileQueued(t *testing.T) {
	var calls int
	dispatcher := NewRunDispatcher(runProcessorFunc(func(context.Context, string) (bool, error) {
		calls++
		return true, nil
	}), 1, 2)
	hints := make([]RunDispatchHintOutcome, 0, 2)
	dispatcher.SetHintHandler(func(observation RunDispatchHintObservation) {
		hints = append(hints, observation.Outcome)
	})
	if err := dispatcher.Dispatch(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}

	completed := make(chan struct{}, 1)
	dispatcher.SetCompleteHandler(func(RunDispatchObservation) { completed <- struct{}{} })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("queued run did not complete")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("processor calls = %d, want 1", calls)
	}
	if len(hints) != 2 || hints[0] != RunDispatchHintEnqueued || hints[1] != RunDispatchHintCoalesced {
		t.Fatalf("hint outcomes = %#v", hints)
	}
}

func TestRunDispatcherRetainsOneReplayDuringInFlightExecution(t *testing.T) {
	started := make(chan int, 2)
	releaseFirst := make(chan struct{})
	calls := 0
	dispatcher := NewRunDispatcher(runProcessorFunc(func(ctx context.Context, _ string) (bool, error) {
		calls++
		started <- calls
		if calls == 1 {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		return true, nil
	}), 1, 1)
	hints := make([]RunDispatchHintOutcome, 0, 6)
	var observations []RunDispatchObservation
	dispatcher.SetHintHandler(func(observation RunDispatchHintObservation) {
		hints = append(hints, observation.Outcome)
	})
	dispatcher.SetCompleteHandler(func(observation RunDispatchObservation) {
		observations = append(observations, observation)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	if err := dispatcher.Dispatch(ctx, "run-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first execution did not start")
	}
	for index := 0; index < 5; index++ {
		if err := dispatcher.Dispatch(ctx, "run-1"); err != nil {
			t.Fatal(err)
		}
	}
	close(releaseFirst)
	select {
	case call := <-started:
		if call != 2 {
			t.Fatalf("replay call = %d", call)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight update did not trigger a replay")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("processor calls = %d, want 2", calls)
	}
	if len(hints) != 6 || hints[0] != RunDispatchHintEnqueued || hints[1] != RunDispatchHintReplayRequested {
		t.Fatalf("hint outcomes = %#v", hints)
	}
	for _, outcome := range hints[2:] {
		if outcome != RunDispatchHintCoalesced {
			t.Fatalf("trailing hint outcome = %q, want coalesced", outcome)
		}
	}
	if len(observations) != 2 || observations[0].Replay || !observations[1].Replay {
		t.Fatalf("completion observations = %#v", observations)
	}
}

func TestRunDispatcherStatsTrackQueueFlightAndReplay(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	dispatcher := NewRunDispatcher(runProcessorFunc(func(ctx context.Context, _ string) (bool, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}), 1, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dispatcher.Run(ctx) }()
	if err := dispatcher.Dispatch(ctx, "run-flight"); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := dispatcher.Dispatch(ctx, "run-flight"); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(ctx, "run-queued"); err != nil {
		t.Fatal(err)
	}
	stats := dispatcher.Stats()
	if stats.Queued != 1 || stats.Pending != 2 || stats.InFlight != 1 || stats.ReplaysPending != 1 {
		t.Fatalf("Stats() = %#v", stats)
	}
	close(release)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
