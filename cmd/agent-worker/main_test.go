package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/eventbus"
)

type toolRunWakerFunc func(context.Context, string, string, int) ([]agent.Run, error)

func (f toolRunWakerFunc) WakeForTool(ctx context.Context, userID, taskID string, limit int) ([]agent.Run, error) {
	return f(ctx, userID, taskID, limit)
}

type dispatchQueueFunc func(context.Context, string) error

func (f dispatchQueueFunc) Dispatch(ctx context.Context, runID string) error {
	return f(ctx, runID)
}

func TestWarmupBlocksReadinessForRuntimeContractFailure(t *testing.T) {
	if !warmupBlocksReadiness(&agent.ExecutorError{
		ErrorCode: "runtime_contract", ErrorMessage: "graph version mismatch",
		ShouldRetry: false,
	}) {
		t.Fatal("runtime contract mismatch must block readiness")
	}
	if warmupBlocksReadiness(&agent.ExecutorError{
		ErrorCode: "runtime_startup", ErrorMessage: "temporary startup failure",
		ShouldRetry: true,
	}) {
		t.Fatal("retryable warmup failure should retain lazy-start recovery")
	}
	if warmupBlocksReadiness(errors.New("unclassified warmup failure")) {
		t.Fatal("unclassified warmup failure should retain existing recovery behavior")
	}
}

func TestAgentEventProcessorDispatchesAgentRun(t *testing.T) {
	dispatched := ""
	canaryRun := ""
	processor := newAgentEventProcessor(
		toolRunWakerFunc(func(context.Context, string, string, int) ([]agent.Run, error) {
			t.Fatal("tool waker should not be called")
			return nil, nil
		}),
		dispatchQueueFunc(func(_ context.Context, runID string) error {
			dispatched = runID
			return nil
		}),
		nil,
		func(runID string, active bool) {
			if active {
				canaryRun = runID
			}
		},
	)
	err := processor.Process(context.Background(), eventbus.Event{
		Type: "agent.run.requested.v1", Payload: json.RawMessage(`{"run_id":"run-1","canary":true}`),
	})
	if err != nil || dispatched != "run-1" || canaryRun != "run-1" {
		t.Fatalf("Process() = dispatched:%q canary:%q, %v", dispatched, canaryRun, err)
	}
}

func TestAgentEventProcessorUnregistersCanaryWhenDispatchFails(t *testing.T) {
	want := errors.New("queue stopped")
	states := make([]bool, 0, 2)
	processor := newAgentEventProcessor(
		toolRunWakerFunc(func(context.Context, string, string, int) ([]agent.Run, error) { return nil, nil }),
		dispatchQueueFunc(func(context.Context, string) error { return want }),
		nil,
		func(_ string, active bool) { states = append(states, active) },
	)
	err := processor.Process(context.Background(), eventbus.Event{
		Type: "agent.run.requested.v1", Payload: json.RawMessage(`{"run_id":"run-1","canary":true}`),
	})
	if !errors.Is(err, want) || len(states) != 2 || !states[0] || states[1] {
		t.Fatalf("Process() states = %#v, %v", states, err)
	}
}

func TestAgentEventProcessorWakesAndDispatchesTerminalToolRun(t *testing.T) {
	var observation toolWakeObservation
	dispatched := make([]string, 0, 2)
	processor := newAgentEventProcessor(
		toolRunWakerFunc(func(_ context.Context, userID, taskID string, limit int) ([]agent.Run, error) {
			if userID != "user-1" || taskID != "task-1" || limit != 100 {
				t.Fatalf("wake input = %q/%q/%d", userID, taskID, limit)
			}
			return []agent.Run{{ID: "run-1"}, {ID: "run-2"}}, nil
		}),
		dispatchQueueFunc(func(_ context.Context, runID string) error {
			dispatched = append(dispatched, runID)
			return nil
		}),
		func(value toolWakeObservation) { observation = value },
		nil,
	)
	err := processor.Process(context.Background(), eventbus.Event{
		Type: "skill.run.failed.v1", OccurredAt: time.Now().Add(-time.Second),
		Payload: json.RawMessage(`{"run_id":"task-1","user_id":"user-1","canary":true}`),
	})
	if err != nil || len(dispatched) != 2 || dispatched[0] != "run-1" || dispatched[1] != "run-2" {
		t.Fatalf("Process() dispatched = %#v, %v", dispatched, err)
	}
	if observation.TaskID != "task-1" || observation.EventType != "skill.run.failed.v1" ||
		observation.Outcome != "awakened" || !observation.Canary || observation.AwakenedRuns != 2 || observation.EventAge < time.Second {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestAgentEventProcessorRejectsWakeFailure(t *testing.T) {
	want := errors.New("database unavailable")
	var observation toolWakeObservation
	processor := newAgentEventProcessor(
		toolRunWakerFunc(func(context.Context, string, string, int) ([]agent.Run, error) {
			return nil, want
		}),
		dispatchQueueFunc(func(context.Context, string) error { return nil }),
		func(value toolWakeObservation) { observation = value },
		nil,
	)
	err := processor.Process(context.Background(), eventbus.Event{
		Type:    "skill.run.succeeded.v1",
		Payload: json.RawMessage(`{"run_id":"task-1","user_id":"user-1"}`),
	})
	if !errors.Is(err, want) {
		t.Fatalf("Process() error = %v", err)
	}
	if observation.Outcome != "error" || observation.AwakenedRuns != 0 {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestAgentEventProcessorObservesNoMatchingToolWake(t *testing.T) {
	var observation toolWakeObservation
	processor := newAgentEventProcessor(
		toolRunWakerFunc(func(context.Context, string, string, int) ([]agent.Run, error) {
			return nil, nil
		}),
		dispatchQueueFunc(func(context.Context, string) error {
			t.Fatal("dispatcher should not be called")
			return nil
		}),
		func(value toolWakeObservation) { observation = value },
		nil,
	)
	if err := processor.Process(context.Background(), eventbus.Event{
		Type: "skill.run.cancelled.v1", Payload: json.RawMessage(`{"run_id":"task-1","user_id":"user-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if observation.Outcome != "no_match" || observation.AwakenedRuns != 0 {
		t.Fatalf("observation = %#v", observation)
	}
}
