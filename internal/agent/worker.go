package agent

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
)

type ExecutionResult struct {
	Status string         `json:"status"`
	Output map[string]any `json:"output"`
}

type RuntimeExecutor interface {
	Execute(context.Context, Run) (ExecutionResult, error)
}

type ToolReadinessProbe interface {
	Ready(context.Context, string, string) (bool, error)
}

type RuntimeRetryObservation struct {
	RunID             string
	Attempt           int
	MaximumAttempts   int
	Delay             time.Duration
	AdvisedDelay      time.Duration
	BaseDelay         time.Duration
	MaximumDelay      time.Duration
	JitterPercent     int
	Policy            string
	ProviderStatus    int
	ErrorCode         string
	DeadlineRemaining time.Duration
}

type RuntimeWorker struct {
	service              *Service
	executor             RuntimeExecutor
	owner                string
	lease                time.Duration
	retryDelay           time.Duration
	retryMaxDelay        time.Duration
	retryJitterPercent   int
	maxExecutionAttempts int
	toolPollInterval     time.Duration
	controlPollInterval  time.Duration
	toolReadiness        ToolReadinessProbe
	executionSlots       chan struct{}
	onRetry              func(RuntimeRetryObservation)
}

func NewRuntimeWorker(service *Service, executor RuntimeExecutor, owner string, lease, retryDelay time.Duration) *RuntimeWorker {
	if retryDelay <= 0 {
		retryDelay = 30 * time.Second
	}
	return &RuntimeWorker{
		service: service, executor: executor, owner: strings.TrimSpace(owner),
		lease: lease, retryDelay: retryDelay, retryMaxDelay: 2 * time.Minute,
		retryJitterPercent: 20, maxExecutionAttempts: 3,
		toolPollInterval:    5 * time.Second,
		controlPollInterval: time.Second,
		executionSlots:      make(chan struct{}, 1),
	}
}

func (w *RuntimeWorker) SetControlPollInterval(interval time.Duration) {
	if interval > 0 {
		w.controlPollInterval = interval
	}
}

func (w *RuntimeWorker) SetToolPollInterval(interval time.Duration) {
	if interval > 0 {
		w.toolPollInterval = interval
	}
}

func (w *RuntimeWorker) SetToolReadinessProbe(probe ToolReadinessProbe) {
	w.toolReadiness = probe
}

func (w *RuntimeWorker) SetMaxConcurrency(limit int) {
	if limit > 0 && limit <= 64 {
		w.executionSlots = make(chan struct{}, limit)
	}
}

func (w *RuntimeWorker) SetMaxExecutionAttempts(attempts int) {
	if attempts > 0 && attempts <= 20 {
		w.maxExecutionAttempts = attempts
	}
}

func (w *RuntimeWorker) SetRetryPolicy(maxDelay time.Duration, jitterPercent int) {
	if maxDelay >= w.retryDelay && maxDelay <= 24*time.Hour {
		w.retryMaxDelay = maxDelay
	}
	if jitterPercent >= 0 && jitterPercent <= 100 {
		w.retryJitterPercent = jitterPercent
	}
}

func (w *RuntimeWorker) SetRetryObservationHandler(handler func(RuntimeRetryObservation)) {
	w.onRetry = handler
}

func (w *RuntimeWorker) ProcessRun(ctx context.Context, runID string) (bool, error) {
	if w == nil || w.service == nil || w.executor == nil || w.owner == "" || w.lease <= 0 {
		return false, ErrValidation
	}
	if err := w.acquireExecutionSlot(ctx); err != nil {
		return false, err
	}
	defer w.releaseExecutionSlot()
	run, err := w.service.Claim(ctx, runID, w.owner, w.lease)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, w.executeClaimed(ctx, run)
}

func (w *RuntimeWorker) RunNext(ctx context.Context) (bool, error) {
	if w == nil || w.service == nil || w.executor == nil || w.owner == "" || w.lease <= 0 {
		return false, ErrValidation
	}
	if err := w.acquireExecutionSlot(ctx); err != nil {
		return false, err
	}
	defer w.releaseExecutionSlot()
	run, err := w.service.ClaimNext(ctx, w.owner, w.lease)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, w.executeClaimed(ctx, run)
}

func (w *RuntimeWorker) RunReconciler(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if _, err := w.service.ExpireDue(ctx, 100); err != nil && ctx.Err() == nil {
			return err
		}
		if err := w.runReconcileBatch(ctx); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

func (w *RuntimeWorker) runReconcileBatch(ctx context.Context) error {
	concurrency := cap(w.executionSlots)
	if concurrency < 1 {
		concurrency = 1
	}
	errorsCh := make(chan error, concurrency)
	for index := 0; index < concurrency; index++ {
		go func() {
			_, err := w.RunNext(ctx)
			errorsCh <- err
		}()
	}
	for index := 0; index < concurrency; index++ {
		if err := <-errorsCh; err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}

func (w *RuntimeWorker) executeClaimed(ctx context.Context, run Run) error {
	if suspended, err := w.suspendPendingTool(ctx, run); suspended || err != nil {
		return err
	}
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()
	type execution struct {
		result ExecutionResult
		err    error
	}
	resultCh := make(chan execution, 1)
	go func() {
		result, err := w.executor.Execute(execCtx, run)
		resultCh <- execution{result: result, err: err}
	}()

	controlInterval := w.controlPollInterval
	if controlInterval <= 0 {
		controlInterval = time.Second
	}
	ticker := time.NewTicker(controlInterval)
	defer ticker.Stop()
	var deadlineTimer *time.Timer
	var deadline <-chan time.Time
	if !run.DeadlineAt.IsZero() {
		until := time.Until(run.DeadlineAt)
		if until < 0 {
			until = 0
		}
		deadlineTimer = time.NewTimer(until)
		deadline = deadlineTimer.C
		defer deadlineTimer.Stop()
	}

	for {
		select {
		case execution := <-resultCh:
			return w.finishExecution(ctx, run, execution.result, execution.err)
		case <-ticker.C:
			latest, err := w.service.Get(ctx, run.ID)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					cancelExec()
					return nil
				}
				return err
			}
			switch latest.Status {
			case "cancel_requested":
				cancelExec()
				_, err = w.service.FinalizeCancellation(
					context.Background(), latest.ID, w.owner, latest.Revision,
				)
				return err
			case "cancelled", "timed_out":
				cancelExec()
				return nil
			case "running":
				if latest.Revision != run.Revision || latest.LeaseOwner != w.owner {
					cancelExec()
					return nil
				}
			default:
				cancelExec()
				return nil
			}
		case <-deadline:
			cancelExec()
			_, err := w.service.Timeout(
				context.Background(), run.ID, w.owner, run.Revision,
			)
			if errors.Is(err, ErrConflict) {
				return w.settleTransitionConflict(run.ID)
			}
			return err
		case <-ctx.Done():
			cancelExec()
			return ctx.Err()
		}
	}
}

func (w *RuntimeWorker) acquireExecutionSlot(ctx context.Context) error {
	if w.executionSlots == nil {
		w.executionSlots = make(chan struct{}, 1)
	}
	select {
	case w.executionSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *RuntimeWorker) releaseExecutionSlot() {
	if w.executionSlots != nil {
		<-w.executionSlots
	}
}

func (w *RuntimeWorker) suspendPendingTool(ctx context.Context, run Run) (bool, error) {
	taskID, ok := toolPollTaskID(run.Resume)
	if !ok || w.toolReadiness == nil {
		return false, nil
	}
	ready, err := w.toolReadiness.Ready(ctx, run.UserID, taskID)
	if err != nil || ready {
		// Preserve the previous behavior on probe errors: the Python runtime can
		// still observe the task and classify a gateway or contract failure.
		return false, nil
	}
	var output map[string]any
	if len(run.Output) == 0 || json.Unmarshal(run.Output, &output) != nil || output == nil {
		return false, nil
	}
	delay := nextToolProbeDelay(output, w.toolPollInterval)
	_, err = w.service.SuspendForTool(ctx, run.ID, w.owner, run.Revision, output, delay)
	if errors.Is(err, ErrConflict) {
		err = w.settleTransitionConflict(run.ID)
	}
	return true, err
}

func toolPollTaskID(resolution json.RawMessage) (string, bool) {
	var value struct {
		Type   string `json:"type"`
		TaskID string `json:"task_id"`
	}
	if len(resolution) == 0 || json.Unmarshal(resolution, &value) != nil ||
		value.Type != "tool_poll" || strings.TrimSpace(value.TaskID) == "" {
		return "", false
	}
	return strings.TrimSpace(value.TaskID), true
}

func (w *RuntimeWorker) finishExecution(ctx context.Context, run Run, result ExecutionResult, err error) error {
	if !run.DeadlineAt.IsZero() && !time.Now().Before(run.DeadlineAt) {
		_, timeoutErr := w.service.Timeout(
			context.Background(), run.ID, w.owner, run.Revision,
		)
		if errors.Is(timeoutErr, ErrConflict) {
			return w.settleTransitionConflict(run.ID)
		}
		return timeoutErr
	}
	latest, getErr := w.service.Get(ctx, run.ID)
	if getErr != nil {
		return getErr
	}
	switch latest.Status {
	case "cancel_requested":
		_, cancelErr := w.service.FinalizeCancellation(
			context.Background(), latest.ID, w.owner, latest.Revision,
		)
		return cancelErr
	case "cancelled", "timed_out":
		return nil
	case "running":
		if latest.Revision != run.Revision || latest.LeaseOwner != w.owner {
			return nil
		}
	default:
		return nil
	}

	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := strings.TrimSpace(err.Error())
		if len(message) > 1000 {
			message = message[:1000]
		}
		var classified interface {
			error
			Code() string
			Retryable() bool
		}
		if errors.As(err, &classified) && permanentlyNonRetryable(classified) {
			_, failErr := w.service.Fail(
				context.Background(), run.ID, w.owner, run.Revision,
				classified.Code(), message,
			)
			if errors.Is(failErr, ErrConflict) {
				return w.settleTransitionConflict(run.ID)
			}
			return failErr
		}
		attempt, budgetErr := w.executionAttempt(ctx, run.ID)
		if budgetErr != nil {
			return budgetErr
		}
		maximum := w.maxExecutionAttempts
		if maximum <= 0 {
			maximum = 3
		}
		if attempt >= maximum {
			_, failErr := w.service.Fail(
				context.Background(), run.ID, w.owner, run.Revision,
				"execution_retry_budget_exhausted", message,
			)
			if errors.Is(failErr, ErrConflict) {
				return w.settleTransitionConflict(run.ID)
			}
			return failErr
		}
		now := w.service.now().UTC()
		decision := w.executionRetryDecision(run.ID, attempt, err, now)
		delay := decision.Delay
		if !run.DeadlineAt.IsZero() && !now.Add(delay).Before(run.DeadlineAt) {
			_, timeoutErr := w.service.TimeoutForRetryDeadline(
				context.Background(), run.ID, w.owner, run.Revision,
			)
			if errors.Is(timeoutErr, ErrConflict) {
				return w.settleTransitionConflict(run.ID)
			}
			return timeoutErr
		}
		_, deferErr := w.service.Defer(
			context.Background(), run.ID, w.owner, run.Revision,
			delay, message,
		)
		if errors.Is(deferErr, ErrConflict) {
			return w.settleTransitionConflict(run.ID)
		}
		if deferErr == nil && w.onRetry != nil {
			deadlineRemaining := time.Duration(0)
			if !run.DeadlineAt.IsZero() {
				deadlineRemaining = run.DeadlineAt.Sub(now)
			}
			w.onRetry(RuntimeRetryObservation{
				RunID: run.ID, Attempt: attempt, MaximumAttempts: maximum,
				Delay: delay, AdvisedDelay: decision.AdvisedDelay,
				BaseDelay: w.retryDelay, MaximumDelay: w.retryMaxDelay,
				JitterPercent: w.retryJitterPercent, Policy: decision.Policy,
				ProviderStatus: decision.ProviderStatus,
				ErrorCode:      decision.ErrorCode, DeadlineRemaining: deadlineRemaining,
			})
		}
		return deferErr
	}
	if result.Output == nil {
		result.Output = map[string]any{}
	}
	switch result.Status {
	case "completed":
		if code, message, failed := terminalGraphFailure(result.Output); failed {
			_, err = w.service.Fail(ctx, run.ID, w.owner, run.Revision, code, message)
		} else {
			_, err = w.service.Complete(ctx, run.ID, w.owner, run.Revision, result.Output)
		}
	case "waiting_approval":
		_, err = w.service.PauseForApproval(ctx, run.ID, w.owner, run.Revision, result.Output)
	case "waiting_tool":
		delay := toolPollDelay(result.Output, w.toolPollInterval)
		if taskID, ok := toolWaitTaskID(result.Output); ok && w.toolReadiness != nil {
			ready, probeErr := w.toolReadiness.Ready(ctx, run.UserID, taskID)
			if probeErr == nil && ready {
				// The task may have completed while Python was returning the
				// interrupt. Make the durable fallback immediately claimable so an
				// in-flight coalesced dispatch can resume without a polling delay.
				delay = 0
			}
		}
		_, err = w.service.SuspendForTool(
			ctx, run.ID, w.owner, run.Revision, result.Output,
			delay,
		)
	default:
		_, err = w.service.Fail(ctx, run.ID, w.owner, run.Revision, "executor_contract", "Agent runtime returned an unsupported status")
	}
	if errors.Is(err, ErrConflict) {
		return w.settleTransitionConflict(run.ID)
	}
	return err
}

func terminalGraphFailure(output map[string]any) (string, string, bool) {
	outcome, _ := output["outcome"].(string)
	outcome = strings.TrimSpace(outcome)
	switch outcome {
	case "model_budget_exhausted", "model_version_mismatch", "model_authentication_error",
		"model_invalid_response", "model_unavailable", "artifact_quality_failed",
		"artifact_missing", "response_quality_failed", "tool_failed", "tool_timeout",
		"action_limit":
		message, _ := output["response"].(string)
		message = strings.TrimSpace(message)
		if message == "" {
			message = "Agent graph stopped before the task contract was satisfied"
		}
		return "graph_" + outcome, message, true
	default:
		return "", "", false
	}
}

func permanentlyNonRetryable(classified interface {
	error
	Code() string
	Retryable() bool
}) bool {
	if !classified.Retryable() {
		return true
	}
	switch strings.TrimSpace(classified.Code()) {
	case "runtime_contract", "model_authentication":
		return true
	}
	var statusAware interface{ StatusCode() int }
	if errors.As(classified, &statusAware) {
		status := statusAware.StatusCode()
		return status >= 400 && status < 500 && status != 408 && status != 409 && status != 429
	}
	return false
}

func (w *RuntimeWorker) executionAttempt(ctx context.Context, runID string) (int, error) {
	events, err := w.service.Events(ctx, runID, 0, 500)
	if err != nil {
		return 0, err
	}
	attempts := 1
	for _, event := range events {
		if isExecutionRetryEvent(event) {
			attempts++
		}
	}
	return attempts, nil
}

func isExecutionRetryEvent(event Event) bool {
	if event.Type != "queued" {
		return false
	}
	if len(event.Payload) == 0 {
		return true
	}
	var payload struct {
		RetryKind string `json:"retry_kind"`
		Reason    string `json:"reason"`
	}
	if json.Unmarshal(event.Payload, &payload) != nil {
		return false
	}
	if payload.RetryKind != "" {
		return payload.RetryKind == "execution"
	}
	return strings.TrimSpace(payload.Reason) != ""
}

func (w *RuntimeWorker) executionRetryDelay(
	runID string,
	attempt int,
	executionErr error,
	now time.Time,
) time.Duration {
	return w.executionRetryDecision(runID, attempt, executionErr, now).Delay
}

type executionRetryDecision struct {
	Delay          time.Duration
	AdvisedDelay   time.Duration
	Policy         string
	ProviderStatus int
	ErrorCode      string
}

func (w *RuntimeWorker) executionRetryDecision(
	runID string,
	attempt int,
	executionErr error,
	now time.Time,
) executionRetryDecision {
	base := w.retryDelay
	if base <= 0 {
		base = 30 * time.Second
	}
	maximum := w.retryMaxDelay
	if maximum < base {
		maximum = base
	}
	delay := base
	advisedDelay := time.Duration(0)
	policy := "exponential"
	providerStatus := 0
	var advised interface {
		StatusCode() int
		RetryAfter(time.Time) (time.Duration, bool)
	}
	if errors.As(executionErr, &advised) {
		providerStatus = advised.StatusCode()
		if retryAfter, ok := advised.RetryAfter(now); ok &&
			(providerStatus == 429 || providerStatus == 503) {
			delay = retryAfter
			advisedDelay = retryAfter
			policy = "retry_after"
			if delay < 100*time.Millisecond {
				delay = 100 * time.Millisecond
			}
		} else {
			delay = boundedExponentialDelay(base, maximum, attempt)
		}
	} else {
		delay = boundedExponentialDelay(base, maximum, attempt)
	}
	if delay > maximum {
		delay = maximum
	}
	jitterMaximum := delay * time.Duration(w.retryJitterPercent) / 100
	if jitterMaximum > 0 && delay < maximum {
		delay += deterministicRetryJitter(runID, attempt, jitterMaximum)
		if delay > maximum {
			delay = maximum
		}
	}
	errorCode := "runtime_error"
	var classified interface{ Code() string }
	if errors.As(executionErr, &classified) && strings.TrimSpace(classified.Code()) != "" {
		errorCode = strings.TrimSpace(classified.Code())
	}
	return executionRetryDecision{
		Delay: delay, AdvisedDelay: advisedDelay, Policy: policy,
		ProviderStatus: providerStatus, ErrorCode: errorCode,
	}
}

func boundedExponentialDelay(base, maximum time.Duration, attempt int) time.Duration {
	delay := base
	for current := 1; current < attempt && delay < maximum; current++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func deterministicRetryJitter(runID string, attempt int, maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.TrimSpace(runID)))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(strconv.Itoa(attempt)))
	return time.Duration(hash.Sum64() % (uint64(maximum) + 1))
}

func toolPollDelay(output map[string]any, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = 5 * time.Second
	}
	interrupts, ok := output["interrupts"].([]any)
	if !ok || len(interrupts) == 0 {
		return fallback
	}
	request, ok := interrupts[0].(map[string]any)
	if !ok || request["type"] != "tool_wait" {
		return fallback
	}
	var milliseconds int64
	switch value := request["poll_after_ms"].(type) {
	case int:
		milliseconds = int64(value)
	case int64:
		milliseconds = value
	case float64:
		milliseconds = int64(value)
	case json.Number:
		milliseconds, _ = value.Int64()
	}
	const (
		minimum = 100 * time.Millisecond
		maximum = 5 * time.Minute
	)
	delay := time.Duration(milliseconds) * time.Millisecond
	if delay < minimum || delay > maximum {
		return fallback
	}
	return delay
}

func toolWaitTaskID(output map[string]any) (string, bool) {
	interrupts, ok := output["interrupts"].([]any)
	if !ok || len(interrupts) != 1 {
		return "", false
	}
	request, ok := interrupts[0].(map[string]any)
	if !ok || request["type"] != "tool_wait" {
		return "", false
	}
	taskID, _ := request["task_id"].(string)
	taskID = strings.TrimSpace(taskID)
	return taskID, taskID != ""
}

func nextToolProbeDelay(output map[string]any, fallback time.Duration) time.Duration {
	initial := toolPollDelay(output, fallback)
	maximum := 5 * time.Second
	if interrupts, ok := output["interrupts"].([]any); ok && len(interrupts) > 0 {
		if request, ok := interrupts[0].(map[string]any); ok {
			if parsed := millisecondsDuration(request["poll_max_ms"]); parsed >= initial && parsed <= 5*time.Minute {
				maximum = parsed
			}
		}
	}
	if maximum < initial {
		maximum = initial
	}
	attempts := integerValue(output["tool_probe_attempts"]) + 1
	output["tool_probe_attempts"] = attempts
	delay := initial
	for index := 0; index < attempts && delay < maximum; index++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func millisecondsDuration(value any) time.Duration {
	var milliseconds int64
	switch typed := value.(type) {
	case int:
		milliseconds = int64(typed)
	case int64:
		milliseconds = typed
	case float64:
		milliseconds = int64(typed)
	case json.Number:
		milliseconds, _ = typed.Int64()
	}
	if milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func integerValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func (w *RuntimeWorker) settleTransitionConflict(runID string) error {
	latest, err := w.service.Get(context.Background(), runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	switch latest.Status {
	case "cancel_requested":
		_, err = w.service.FinalizeCancellation(
			context.Background(), latest.ID, w.owner, latest.Revision,
		)
		return err
	case "cancelled", "timed_out":
		return nil
	default:
		return ErrConflict
	}
}
