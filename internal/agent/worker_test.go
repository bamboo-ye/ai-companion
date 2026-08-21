package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type runtimeWorkerStore struct {
	run         Run
	claimError  error
	transition  string
	output      json.RawMessage
	reason      string
	available   time.Time
	timeoutCode string
	events      []Event
	defers      int
	completes   int
	failures    int
}

func (s *runtimeWorkerStore) CreateAgentRun(context.Context, Run) (Run, bool, error) {
	return Run{}, false, nil
}
func (s *runtimeWorkerStore) GetAgentRun(context.Context, string) (Run, error) {
	return s.run, nil
}
func (s *runtimeWorkerStore) ClaimAgentRun(_ context.Context, _, owner string, now time.Time, lease time.Duration) (Run, error) {
	if s.claimError != nil {
		return Run{}, s.claimError
	}
	s.run.Status = "running"
	s.run.LeaseOwner = owner
	expires := now.Add(lease)
	s.run.LeaseExpiresAt = &expires
	s.run.Revision++
	return s.run, nil
}
func (s *runtimeWorkerStore) ClaimNextAgentRun(ctx context.Context, owner string, now time.Time, lease time.Duration) (Run, error) {
	return s.ClaimAgentRun(ctx, s.run.ID, owner, now, lease)
}
func (s *runtimeWorkerStore) DeferAgentRun(_ context.Context, _, _ string, _ int, available time.Time, reason string, _ time.Time) (Run, error) {
	s.transition, s.reason = "queued", reason
	s.available = available
	s.run.Status = "queued"
	s.defers++
	s.events = append(s.events, Event{Type: "queued"})
	return s.run, nil
}
func (s *runtimeWorkerStore) PauseAgentRun(_ context.Context, _, _ string, _ int, output json.RawMessage, _ time.Time) (Run, error) {
	s.transition, s.output = "waiting_approval", output
	s.run.Status = "waiting_approval"
	return s.run, nil
}
func (s *runtimeWorkerStore) SuspendAgentRunForTool(_ context.Context, _, _ string, _ int, output json.RawMessage, available, _ time.Time) (Run, error) {
	s.transition, s.output = "waiting_tool", output
	s.available = available
	s.run.Status = "waiting_tool"
	return s.run, nil
}
func (s *runtimeWorkerStore) WakeAgentRunsForTool(context.Context, string, string, time.Time, int) ([]Run, error) {
	return nil, nil
}
func (s *runtimeWorkerStore) ResolveAgentRun(context.Context, string, string, bool, string, time.Time) (Run, bool, error) {
	return s.run, true, nil
}
func (s *runtimeWorkerStore) CancelAgentRun(context.Context, string, string, time.Time) (Run, bool, error) {
	s.run.Status = "cancelled"
	s.transition = "cancelled"
	return s.run, true, nil
}
func (s *runtimeWorkerStore) FinalizeAgentRunCancellation(context.Context, string, string, int, time.Time) (Run, error) {
	s.run.Status = "cancelled"
	s.transition = "cancelled"
	return s.run, nil
}
func (s *runtimeWorkerStore) TimeoutAgentRun(_ context.Context, _, _ string, _ int, code, _ string, _ time.Time) (Run, error) {
	s.run.Status = "timed_out"
	s.transition = "timed_out"
	s.timeoutCode = code
	return s.run, nil
}
func (s *runtimeWorkerStore) ExpireAgentRuns(context.Context, time.Time, int) (int, error) {
	return 0, nil
}
func (s *runtimeWorkerStore) CompleteAgentRun(_ context.Context, _, _ string, _ int, output json.RawMessage, _ time.Time) (Run, error) {
	s.transition, s.output = "completed", output
	s.run.Status = "completed"
	s.completes++
	return s.run, nil
}
func (s *runtimeWorkerStore) FailAgentRun(_ context.Context, _, _ string, _ int, _, _ string, _ time.Time) (Run, error) {
	s.transition = "failed"
	s.run.Status = "failed"
	s.failures++
	return s.run, nil
}
func (s *runtimeWorkerStore) ListAgentRunEvents(context.Context, string, int64, int) ([]Event, error) {
	return append([]Event(nil), s.events...), nil
}

type runtimeExecutorStub struct {
	result ExecutionResult
	err    error
	calls  int
}

type runtimeExecution struct {
	result ExecutionResult
	err    error
}

type sequenceRuntimeExecutor struct {
	executions []runtimeExecution
	calls      int
}

func (e *sequenceRuntimeExecutor) Execute(context.Context, Run) (ExecutionResult, error) {
	if e.calls >= len(e.executions) {
		return ExecutionResult{}, errors.New("unexpected extra execution")
	}
	execution := e.executions[e.calls]
	e.calls++
	return execution.result, execution.err
}

type toolReadinessFunc func(context.Context, string, string) (bool, error)

func (f toolReadinessFunc) Ready(ctx context.Context, userID, taskID string) (bool, error) {
	return f(ctx, userID, taskID)
}

type contextBlockingExecutor struct {
	cancelled chan struct{}
}

func (e *contextBlockingExecutor) Execute(ctx context.Context, _ Run) (ExecutionResult, error) {
	<-ctx.Done()
	close(e.cancelled)
	return ExecutionResult{}, ctx.Err()
}

type cancellingRuntimeStore struct {
	runtimeWorkerStore
	observed bool
}

func (s *cancellingRuntimeStore) GetAgentRun(_ context.Context, _ string) (Run, error) {
	if s.run.Status == "running" && !s.observed {
		s.observed = true
		s.run.Status = "cancel_requested"
		s.run.Revision++
	}
	return s.run, nil
}

func (e *runtimeExecutorStub) Execute(context.Context, Run) (ExecutionResult, error) {
	e.calls++
	return e.result, e.err
}

func newRuntimeWorkerTest(store Store, executor RuntimeExecutor) *RuntimeWorker {
	service := NewServiceWithClock(store, func() time.Time {
		return time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	})
	return NewRuntimeWorker(service, executor, "agent-worker-1", 10*time.Minute, 15*time.Second)
}

func TestRuntimeWorkerCompletesClaimedRun(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-1", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{result: ExecutionResult{
		Status: "completed",
		Output: map[string]any{"response": "今天没有计划。"},
	}}
	processed, err := newRuntimeWorkerTest(store, executor).ProcessRun(context.Background(), "run-1")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "completed" || executor.calls != 1 {
		t.Fatalf("transition=%q calls=%d", store.transition, executor.calls)
	}
	var output map[string]any
	if err = json.Unmarshal(store.output, &output); err != nil || output["response"] != "今天没有计划。" {
		t.Fatalf("output=%s err=%v", store.output, err)
	}
}

func TestRuntimeWorkerPausesForApproval(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-2", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{result: ExecutionResult{
		Status: "waiting_approval",
		Output: map[string]any{"summary": "餐饮支出 50 元"},
	}}
	processed, err := newRuntimeWorkerTest(store, executor).ProcessRun(context.Background(), "run-2")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "waiting_approval" {
		t.Fatalf("transition=%q", store.transition)
	}
}

func TestRuntimeWorkerSuspendsForExternalTool(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-tool", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{result: ExecutionResult{
		Status: "waiting_tool",
		Output: map[string]any{
			"interrupts": []any{map[string]any{
				"type": "tool_wait", "task_id": "task-1", "interrupt_id": "interrupt-1",
				"poll_after_ms": float64(2500),
			}},
		},
	}}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetToolPollInterval(time.Second)
	processed, err := worker.ProcessRun(context.Background(), "run-tool")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "waiting_tool" || store.run.Status != "waiting_tool" {
		t.Fatalf("transition/status = %q/%q", store.transition, store.run.Status)
	}
	wantAvailable := time.Date(2026, 7, 24, 12, 0, 2, 500_000_000, time.UTC)
	if !store.available.Equal(wantAvailable) {
		t.Fatalf("available_at = %v, want %v", store.available, wantAvailable)
	}
}

func TestRuntimeWorkerMakesCompletedToolImmediatelyClaimable(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{
		ID: "run-tool-race", UserID: "user-1", Status: "accepted", Revision: 1,
	}}
	executor := &runtimeExecutorStub{result: ExecutionResult{
		Status: "waiting_tool",
		Output: map[string]any{"interrupts": []any{map[string]any{
			"type": "tool_wait", "task_id": "task-1", "interrupt_id": "interrupt-1",
			"poll_after_ms": float64(5000),
		}}},
	}}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetToolReadinessProbe(toolReadinessFunc(func(_ context.Context, userID, taskID string) (bool, error) {
		if userID != "user-1" || taskID != "task-1" {
			t.Fatalf("probe identity = %q/%q", userID, taskID)
		}
		return true, nil
	}))
	processed, err := worker.ProcessRun(context.Background(), "run-tool-race")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	want := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	if store.transition != "waiting_tool" || !store.available.Equal(want) {
		t.Fatalf("transition/available_at = %q/%v, want waiting_tool/%v", store.transition, store.available, want)
	}
}

func TestRuntimeWorkerSkipsPythonWhileExternalToolIsPending(t *testing.T) {
	output := map[string]any{
		"interrupts": []any{map[string]any{
			"type": "tool_wait", "task_id": "task-1", "interrupt_id": "interrupt-1",
			"poll_after_ms": float64(500), "poll_max_ms": float64(5000),
		}},
	}
	encodedOutput, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	store := &runtimeWorkerStore{run: Run{
		ID: "run-tool-pending", UserID: "user-1", Status: "waiting_tool", Revision: 2,
		Output: encodedOutput, Resume: json.RawMessage(`{"type":"tool_poll","task_id":"task-1"}`),
	}}
	executor := &runtimeExecutorStub{result: ExecutionResult{Status: "completed"}}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetToolReadinessProbe(toolReadinessFunc(func(_ context.Context, userID, taskID string) (bool, error) {
		if userID != "user-1" || taskID != "task-1" {
			t.Fatalf("probe identity = %q/%q", userID, taskID)
		}
		return false, nil
	}))

	processed, err := worker.ProcessRun(context.Background(), "run-tool-pending")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if executor.calls != 0 || store.transition != "waiting_tool" {
		t.Fatalf("executor calls=%d transition=%q", executor.calls, store.transition)
	}
	wantAvailable := time.Date(2026, 7, 24, 12, 0, 1, 0, time.UTC)
	if !store.available.Equal(wantAvailable) {
		t.Fatalf("available_at = %v, want %v", store.available, wantAvailable)
	}
	var persisted map[string]any
	if err = json.Unmarshal(store.output, &persisted); err != nil || persisted["tool_probe_attempts"] != float64(1) {
		t.Fatalf("output=%s err=%v", store.output, err)
	}
}

func TestRuntimeWorkerResumesPythonWhenExternalToolIsReady(t *testing.T) {
	output := json.RawMessage(`{"interrupts":[{"type":"tool_wait","task_id":"task-1","interrupt_id":"interrupt-1","poll_after_ms":500}]}`)
	store := &runtimeWorkerStore{run: Run{
		ID: "run-tool-ready", UserID: "user-1", Status: "waiting_tool", Revision: 2,
		Output: output, Resume: json.RawMessage(`{"type":"tool_poll","task_id":"task-1"}`),
	}}
	executor := &runtimeExecutorStub{result: ExecutionResult{
		Status: "completed", Output: map[string]any{"response": "任务完成。"},
	}}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetToolReadinessProbe(toolReadinessFunc(func(context.Context, string, string) (bool, error) {
		return true, nil
	}))

	processed, err := worker.ProcessRun(context.Background(), "run-tool-ready")
	if err != nil || !processed || executor.calls != 1 || store.transition != "completed" {
		t.Fatalf("ProcessRun() = %v, %v calls=%d transition=%q", processed, err, executor.calls, store.transition)
	}
}

func TestNextToolProbeDelayUsesBoundedExponentialBackoff(t *testing.T) {
	output := map[string]any{"interrupts": []any{map[string]any{
		"type": "tool_wait", "poll_after_ms": float64(500), "poll_max_ms": float64(5000),
	}}}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	for index, expected := range want {
		if got := nextToolProbeDelay(output, 5*time.Second); got != expected {
			t.Fatalf("delay %d = %v, want %v", index, got, expected)
		}
	}
}

func TestToolPollDelayRejectsOutOfRangeExecutorValue(t *testing.T) {
	fallback := 7 * time.Second
	for _, milliseconds := range []float64{-1, 50, 600_000} {
		output := map[string]any{"interrupts": []any{map[string]any{
			"type": "tool_wait", "poll_after_ms": milliseconds,
		}}}
		if got := toolPollDelay(output, fallback); got != fallback {
			t.Fatalf("toolPollDelay(%v) = %v, want %v", milliseconds, got, fallback)
		}
	}
}

func TestRuntimeWorkerDefersTransientExecutorFailure(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-3", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{err: errors.New("OpenRouter is unavailable")}
	processed, err := newRuntimeWorkerTest(store, executor).ProcessRun(context.Background(), "run-3")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "queued" || store.reason != "OpenRouter is unavailable" {
		t.Fatalf("transition=%q reason=%q", store.transition, store.reason)
	}
}

func TestRuntimeWorkerRateLimitHonorsRetryAfter(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-rate-limit", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{err: &ExecutorError{
		ErrorCode: "model_unavailable", ErrorMessage: "rate limited", ShouldRetry: true,
		ProviderStatus: 429, RetryAfterValue: "17",
	}}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetRetryPolicy(time.Minute, 0)
	var observation RuntimeRetryObservation
	worker.SetRetryObservationHandler(func(value RuntimeRetryObservation) { observation = value })

	processed, err := worker.ProcessRun(context.Background(), "run-rate-limit")
	if err != nil || !processed || store.transition != "queued" {
		t.Fatalf("ProcessRun() = %v, %v transition=%q", processed, err, store.transition)
	}
	want := time.Date(2026, 7, 24, 12, 0, 17, 0, time.UTC)
	if !store.available.Equal(want) {
		t.Fatalf("available_at = %v, want %v", store.available, want)
	}
	if observation.Policy != "retry_after" || observation.ProviderStatus != 429 ||
		observation.Attempt != 1 || observation.Delay != 17*time.Second ||
		observation.AdvisedDelay != 17*time.Second || observation.BaseDelay != 15*time.Second ||
		observation.MaximumDelay != time.Minute || observation.JitterPercent != 0 {
		t.Fatalf("retry observation = %#v", observation)
	}
}

func TestRuntimeWorkerRetryRecoversAndCompletesExactlyOnce(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-recover", Status: "accepted", Revision: 1}}
	executor := &sequenceRuntimeExecutor{executions: []runtimeExecution{
		{err: &ExecutorError{
			ErrorCode: "model_unavailable", ErrorMessage: "rate limited", ShouldRetry: true,
			ProviderStatus: 429, RetryAfterValue: "1",
		}},
		{result: ExecutionResult{
			Status: "completed", Output: map[string]any{"response": "恢复后成功。"},
		}},
	}}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetRetryPolicy(time.Minute, 0)

	processed, err := worker.ProcessRun(context.Background(), "run-recover")
	if err != nil || !processed || store.transition != "queued" {
		t.Fatalf("first ProcessRun() = %v, %v transition=%q", processed, err, store.transition)
	}
	processed, err = worker.ProcessRun(context.Background(), "run-recover")
	if err != nil || !processed || store.transition != "completed" {
		t.Fatalf("second ProcessRun() = %v, %v transition=%q", processed, err, store.transition)
	}
	if executor.calls != 2 || store.defers != 1 || store.completes != 1 || store.failures != 0 {
		t.Fatalf(
			"calls=%d defers=%d completes=%d failures=%d",
			executor.calls, store.defers, store.completes, store.failures,
		)
	}
}

func TestExecutionRetryDelayUsesBoundedExponentialBackoff(t *testing.T) {
	worker := newRuntimeWorkerTest(&runtimeWorkerStore{}, &runtimeExecutorStub{})
	worker.SetRetryPolicy(time.Minute, 0)
	executionErr := &ExecutorError{
		ErrorCode: "model_unavailable", ErrorMessage: "provider unavailable",
		ShouldRetry: true, ProviderStatus: 503,
	}
	want := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, time.Minute}
	for index, expected := range want {
		if got := worker.executionRetryDelay("run-backoff", index+1, executionErr, time.Time{}); got != expected {
			t.Fatalf("attempt %d delay = %v, want %v", index+1, got, expected)
		}
	}
}

func TestExecutionRetryJitterIsStableAndBounded(t *testing.T) {
	worker := newRuntimeWorkerTest(&runtimeWorkerStore{}, &runtimeExecutorStub{})
	worker.SetRetryPolicy(time.Minute, 20)
	executionErr := errors.New("transport failed")
	first := worker.executionRetryDelay("run-jitter", 1, executionErr, time.Time{})
	second := worker.executionRetryDelay("run-jitter", 1, executionErr, time.Time{})
	if first != second || first < 15*time.Second || first > 18*time.Second {
		t.Fatalf("stable jitter delay = %v/%v", first, second)
	}
}

func TestRuntimeWorkerDoesNotScheduleRetryPastRunDeadline(t *testing.T) {
	now := time.Now().UTC()
	store := &runtimeWorkerStore{run: Run{
		ID: "run-retry-deadline", Status: "accepted", Revision: 1,
		DeadlineAt: now.Add(10 * time.Second),
	}}
	executor := &runtimeExecutorStub{err: &ExecutorError{
		ErrorCode: "model_unavailable", ErrorMessage: "rate limited", ShouldRetry: true,
		ProviderStatus: 429, RetryAfterValue: "17",
	}}
	service := NewServiceWithClock(store, func() time.Time { return now })
	worker := NewRuntimeWorker(service, executor, "agent-worker-1", time.Minute, 15*time.Second)
	worker.SetRetryPolicy(time.Minute, 0)

	processed, err := worker.ProcessRun(context.Background(), "run-retry-deadline")
	if err != nil || !processed || store.transition != "timed_out" {
		t.Fatalf("ProcessRun() = %v, %v transition=%q", processed, err, store.transition)
	}
	if store.timeoutCode != "execution_retry_deadline_exhausted" {
		t.Fatalf("timeout code = %q", store.timeoutCode)
	}
}

func TestExecutionAttemptCountsOnlyExecutionRetryEvents(t *testing.T) {
	store := &runtimeWorkerStore{events: []Event{
		{Type: "queued", Payload: json.RawMessage(`{"retry_kind":"approval"}`)},
		{Type: "queued", Payload: json.RawMessage(`{"retry_kind":"execution"}`)},
		{Type: "approval_resolved", Payload: json.RawMessage(`{"approved":true}`)},
	}}
	worker := newRuntimeWorkerTest(store, &runtimeExecutorStub{})
	attempt, err := worker.executionAttempt(context.Background(), "run-attempt-events")
	if err != nil || attempt != 2 {
		t.Fatalf("executionAttempt() = %d, %v", attempt, err)
	}
}

func TestRuntimeWorkerStopsAfterExecutionAttemptBudget(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-retry-budget", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{err: errors.New("OpenRouter is unavailable")}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetMaxExecutionAttempts(2)

	processed, err := worker.ProcessRun(context.Background(), "run-retry-budget")
	if err != nil || !processed || store.transition != "queued" {
		t.Fatalf("first ProcessRun() = %v, %v transition=%q", processed, err, store.transition)
	}
	processed, err = worker.ProcessRun(context.Background(), "run-retry-budget")
	if err != nil || !processed {
		t.Fatalf("second ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "failed" || executor.calls != 2 {
		t.Fatalf("transition=%q calls=%d", store.transition, executor.calls)
	}
}

func TestRuntimeWorkerFailsPermanentExecutorErrorWithoutRetry(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-permanent", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{err: &ExecutorError{
		ErrorCode: "runtime_contract", ErrorMessage: "graph version mismatch", ShouldRetry: false,
	}}
	processed, err := newRuntimeWorkerTest(store, executor).ProcessRun(context.Background(), "run-permanent")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "failed" {
		t.Fatalf("transition=%q", store.transition)
	}
}

func TestRuntimeWorkerRejectsMisclassifiedAuthenticationError(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-auth", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{err: &ExecutorError{
		ErrorCode: "model_unavailable", ErrorMessage: "invalid key", ShouldRetry: true,
		ProviderStatus: 401,
	}}
	processed, err := newRuntimeWorkerTest(store, executor).ProcessRun(context.Background(), "run-auth")
	if err != nil || !processed || store.transition != "failed" {
		t.Fatalf("ProcessRun() = %v, %v transition=%q", processed, err, store.transition)
	}
}

func TestRuntimeWorkerIgnoresDuplicateClaim(t *testing.T) {
	store := &runtimeWorkerStore{
		run:        Run{ID: "run-4", Status: "completed", Revision: 3},
		claimError: ErrConflict,
	}
	executor := &runtimeExecutorStub{}
	processed, err := newRuntimeWorkerTest(store, executor).ProcessRun(context.Background(), "run-4")
	if err != nil || processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls=%d", executor.calls)
	}
}

func TestRuntimeReconcilerDoesNotClaimBeforeFirstInterval(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{ID: "run-5", Status: "accepted", Revision: 1}}
	executor := &runtimeExecutorStub{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newRuntimeWorkerTest(store, executor).RunReconciler(ctx, time.Hour); err != nil {
		t.Fatalf("RunReconciler() error = %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls before first reconciliation interval = %d", executor.calls)
	}
}

func TestRuntimeWorkerCancelsExecutorAndFinalizesRequestedCancellation(t *testing.T) {
	store := &cancellingRuntimeStore{runtimeWorkerStore: runtimeWorkerStore{
		run: Run{ID: "run-cancel", Status: "accepted", Revision: 1},
	}}
	executor := &contextBlockingExecutor{cancelled: make(chan struct{})}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetControlPollInterval(time.Millisecond)
	processed, err := worker.ProcessRun(context.Background(), "run-cancel")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "cancelled" || store.run.Status != "cancelled" {
		t.Fatalf("transition/status = %q/%q", store.transition, store.run.Status)
	}
	select {
	case <-executor.cancelled:
	case <-time.After(time.Second):
		t.Fatal("executor did not observe cancellation")
	}
}

func TestRuntimeWorkerTimesOutExecutorAtFixedRunDeadline(t *testing.T) {
	store := &runtimeWorkerStore{run: Run{
		ID: "run-timeout", Status: "accepted", Revision: 1,
		DeadlineAt: time.Now().Add(20 * time.Millisecond),
	}}
	executor := &contextBlockingExecutor{cancelled: make(chan struct{})}
	worker := newRuntimeWorkerTest(store, executor)
	worker.SetControlPollInterval(time.Hour)
	processed, err := worker.ProcessRun(context.Background(), "run-timeout")
	if err != nil || !processed {
		t.Fatalf("ProcessRun() = %v, %v", processed, err)
	}
	if store.transition != "timed_out" || store.run.Status != "timed_out" {
		t.Fatalf("transition/status = %q/%q", store.transition, store.run.Status)
	}
	select {
	case <-executor.cancelled:
	case <-time.After(time.Second):
		t.Fatal("executor did not observe deadline cancellation")
	}
}
