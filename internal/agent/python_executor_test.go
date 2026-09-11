package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseExecutorError(t *testing.T) {
	err := parseExecutorError(`diagnostic
agent runtime error:{"code":"runtime_contract","retryable":false,"provider_status":400,"retry_after":"17","message":"graph version mismatch"}`)
	if err == nil || err.Code() != "runtime_contract" || err.Retryable() || err.Error() != "graph version mismatch" {
		t.Fatalf("parseExecutorError() = %#v", err)
	}
	if err.StatusCode() != 400 {
		t.Fatalf("StatusCode() = %d", err.StatusCode())
	}
	if delay, ok := err.RetryAfter(time.Now()); !ok || delay != 17*time.Second {
		t.Fatalf("RetryAfter() = %v, %v", delay, ok)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	delay, ok := parseRetryAfter("Mon, 10 Aug 2026 12:00:09 GMT", now)
	if !ok || delay != 9*time.Second {
		t.Fatalf("parseRetryAfter() = %v, %v", delay, ok)
	}
	if _, ok = parseRetryAfter("86401", now); ok {
		t.Fatal("parseRetryAfter() accepted an unbounded delay")
	}
}

func TestParseExecutorErrorRejectsUnstructuredStderr(t *testing.T) {
	if err := parseExecutorError("plain failure"); err != nil {
		t.Fatalf("parseExecutorError() = %#v", err)
	}
}

func TestPythonRuntimeExecutorObservesProcessExecution(t *testing.T) {
	var observation PythonRuntimeObservation
	executor := PythonRuntimeExecutor{
		Executable: os.Args[0], Timeout: time.Second,
		OnObserve: func(value PythonRuntimeObservation) { observation = value },
	}
	_, err := executor.Execute(context.Background(), Run{ID: "process-run"})
	if err == nil {
		t.Fatal("Execute() expected the test binary invocation to fail")
	}
	if observation.RunID != "process-run" || observation.ExecutionMode != "process" ||
		observation.Success || observation.Duration <= 0 || observation.ErrorCode == "" {
		t.Fatalf("process observation = %#v", observation)
	}
}

func TestPythonRuntimeExecutorEnvironmentOverridesParent(t *testing.T) {
	t.Setenv("MODEL_CONFIG_VERSION", "parent")
	executor := PythonRuntimeExecutor{Environment: map[string]string{
		"MODEL_CONFIG_VERSION": "published-v2",
		"MODEL_PROVIDER":       "openrouter",
	}}
	environment := executor.commandEnv()
	seen := map[string][]string{}
	for _, item := range environment {
		key, value, ok := strings.Cut(item, "=")
		if ok && (key == "MODEL_CONFIG_VERSION" || key == "MODEL_PROVIDER") {
			seen[key] = append(seen[key], value)
		}
	}
	if len(seen["MODEL_CONFIG_VERSION"]) != 1 || seen["MODEL_CONFIG_VERSION"][0] != "published-v2" {
		t.Fatalf("MODEL_CONFIG_VERSION values = %#v", seen["MODEL_CONFIG_VERSION"])
	}
	if len(seen["MODEL_PROVIDER"]) != 1 || seen["MODEL_PROVIDER"][0] != "openrouter" {
		t.Fatalf("MODEL_PROVIDER values = %#v", seen["MODEL_PROVIDER"])
	}
}

func TestPythonRuntimePoolReusesHealthyProcess(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	var observations []PythonRuntimeObservation
	pool.SetObservationHandler(func(observation PythonRuntimeObservation) {
		observations = append(observations, observation)
	})
	first, err := pool.Execute(context.Background(), Run{ID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.Execute(context.Background(), Run{ID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Output["sequence"] != float64(1) || second.Output["sequence"] != float64(2) ||
		first.Output["pid"] != second.Output["pid"] {
		t.Fatalf("process was not reused: first=%#v second=%#v", first.Output, second.Output)
	}
	if stats := pool.Stats(); stats.Requests != 2 || stats.ProcessStarts != 1 || stats.ProcessDiscards != 0 {
		t.Fatalf("pool stats = %#v", stats)
	}
	if len(observations) != 2 || !observations[0].ColdStart || observations[1].ColdStart ||
		!observations[0].Success || !observations[1].Success || observations[0].Recycled || observations[1].Recycled {
		t.Fatalf("pool observations = %#v", observations)
	}
	if observations[0].RunID != "first" || observations[1].RunID != "second" ||
		observations[0].Duration <= 0 || observations[1].Duration <= 0 {
		t.Fatalf("pool observation metadata = %#v", observations)
	}
}

func TestPythonRuntimePoolReloadEnvironmentRetiresIdleRuntime(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second, Environment: map[string]string{"MODEL_CONFIG_VERSION": "published-v1"}},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	first, err := pool.Execute(context.Background(), Run{ID: "before-reload"})
	if err != nil {
		t.Fatal(err)
	}
	if changed := pool.ReloadEnvironment(map[string]string{"MODEL_CONFIG_VERSION": "published-v2"}); !changed {
		t.Fatal("ReloadEnvironment() did not observe a changed snapshot")
	}
	if changed := pool.ReloadEnvironment(map[string]string{"MODEL_CONFIG_VERSION": "published-v2"}); changed {
		t.Fatal("ReloadEnvironment() reloaded an identical snapshot")
	}
	second, err := pool.Execute(context.Background(), Run{ID: "after-reload"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Output["model_config_version"] != "published-v1" || second.Output["model_config_version"] != "published-v2" {
		t.Fatalf("runtime versions = first %#v, second %#v", first.Output, second.Output)
	}
	if first.Output["pid"] == second.Output["pid"] {
		t.Fatalf("reload reused stale process: first=%#v second=%#v", first.Output, second.Output)
	}
	if stats := pool.Stats(); stats.ProcessStarts != 2 || stats.ProcessDiscards != 1 {
		t.Fatalf("pool reload stats = %#v", stats)
	}
}

func TestPythonRuntimePoolPrefersWarmProcessBeforeStartingUnusedCapacity(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		4,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	var observations []PythonRuntimeObservation
	pool.SetObservationHandler(func(observation PythonRuntimeObservation) {
		observations = append(observations, observation)
	})
	for _, runID := range []string{"first", "second", "third"} {
		if _, err := pool.Execute(context.Background(), Run{ID: runID}); err != nil {
			t.Fatal(err)
		}
	}
	if stats := pool.Stats(); stats.ProcessStarts != 1 {
		t.Fatalf("sequential requests started unused pool capacity: %#v", stats)
	}
	if len(observations) != 3 || !observations[0].ColdStart ||
		observations[1].ColdStart || observations[2].ColdStart {
		t.Fatalf("warm-process observations = %#v", observations)
	}
}

func TestPythonRuntimePoolWarmStartsReusableProcess(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		4,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	ready, err := pool.Warm(context.Background(), 1)
	if err != nil || ready != 1 {
		t.Fatalf("Warm() = %d, %v", ready, err)
	}
	var observation PythonRuntimeObservation
	pool.SetObservationHandler(func(value PythonRuntimeObservation) { observation = value })
	result, err := pool.Execute(context.Background(), Run{ID: "after-warmup"})
	if err != nil {
		t.Fatal(err)
	}
	if observation.ColdStart || !observation.Success || result.Output["sequence"] != float64(1) {
		t.Fatalf("warmed execution = %#v, %#v", observation, result.Output)
	}
	if stats := pool.Stats(); stats.Requests != 1 || stats.ProcessStarts != 1 || stats.ProcessDiscards != 0 {
		t.Fatalf("warm pool stats = %#v", stats)
	}
}

func TestPopulateModelTimingSeparatesRuntimeOverhead(t *testing.T) {
	observation := PythonRuntimeObservation{Duration: 3500 * time.Millisecond}
	populateModelTiming(&observation, ExecutionResult{Output: map[string]any{
		"model": map[string]any{
			"calls": []any{
				map[string]any{"latency_ms": float64(1200), "timeout_ms": float64(15000)},
				map[string]any{"latency_ms": float64(1800), "timeout_ms": float64(10000)},
			},
		},
	}})
	if observation.ModelCalls != 2 || observation.ModelDuration != 3*time.Second ||
		observation.MaxModelTimeout != 15*time.Second ||
		observation.RuntimeOverhead != 500*time.Millisecond {
		t.Fatalf("model timing = %#v", observation)
	}
}

func TestPythonRuntimePoolWarmTimeoutFallsBackToLazyStart(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	t.Setenv("GO_WANT_PYTHON_POOL_READY_DELAY", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	ready, err := pool.Warm(ctx, 1)
	if !errors.Is(err, context.DeadlineExceeded) || ready != 0 {
		t.Fatalf("Warm() = %d, %v", ready, err)
	}
	if stats := pool.Stats(); stats.ProcessStarts != 1 || stats.ProcessDiscards != 1 {
		t.Fatalf("failed warmup stats = %#v", stats)
	}
	t.Setenv("GO_WANT_PYTHON_POOL_READY_DELAY", "")
	result, err := pool.Execute(context.Background(), Run{ID: "lazy-after-warmup-timeout"})
	if err != nil || result.Output["sequence"] != float64(1) {
		t.Fatalf("lazy fallback = %#v, %v", result.Output, err)
	}
	if stats := pool.Stats(); stats.ProcessStarts != 2 || stats.ProcessDiscards != 1 {
		t.Fatalf("lazy fallback stats = %#v", stats)
	}
}

func TestPythonRuntimePoolRejectsWrongReadyIdentity(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	t.Setenv("GO_WANT_PYTHON_POOL_WRONG_GRAPH", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	ready, err := pool.Warm(context.Background(), 1)
	var classified *ExecutorError
	if ready != 0 || !errors.As(err, &classified) || classified.Code() != "runtime_contract" || classified.Retryable() {
		t.Fatalf("Warm() = %d, %#v", ready, err)
	}
	if stats := pool.Stats(); stats.ProcessStarts != 1 || stats.ProcessDiscards != 1 {
		t.Fatalf("identity rejection stats = %#v", stats)
	}
}

func TestPythonRuntimePoolKeepsProcessAfterRequestError(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	if _, err := pool.Execute(context.Background(), Run{ID: "error"}); err == nil {
		t.Fatal("Execute() expected a request error")
	}
	result, err := pool.Execute(context.Background(), Run{ID: "after-error"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["sequence"] != float64(2) {
		t.Fatalf("request error restarted healthy process: %#v", result.Output)
	}
	if stats := pool.Stats(); stats.RequestErrors != 1 || stats.ProcessStarts != 1 || stats.ProcessDiscards != 0 {
		t.Fatalf("pool stats = %#v", stats)
	}
}

func TestPythonRuntimePoolPreservesRetryAdvice(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	_, err := pool.Execute(context.Background(), Run{ID: "rate-limit"})
	var classified *ExecutorError
	if !errors.As(err, &classified) || classified.StatusCode() != 429 || !classified.Retryable() {
		t.Fatalf("Execute() error = %#v", err)
	}
	if delay, ok := classified.RetryAfter(time.Now()); !ok || delay != 19*time.Second {
		t.Fatalf("RetryAfter() = %v, %v", delay, ok)
	}
}

func TestPythonRuntimePoolRestartsCrashedProcess(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	var observations []PythonRuntimeObservation
	pool.SetObservationHandler(func(observation PythonRuntimeObservation) {
		observations = append(observations, observation)
	})
	if _, err := pool.Execute(context.Background(), Run{ID: "crash"}); err == nil {
		t.Fatal("Execute() expected a process error")
	}
	result, err := pool.Execute(context.Background(), Run{ID: "after-crash"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["sequence"] != float64(1) {
		t.Fatalf("crashed process was not replaced: %#v", result.Output)
	}
	if stats := pool.Stats(); stats.ProcessStarts != 2 || stats.ProcessDiscards != 1 {
		t.Fatalf("pool stats = %#v", stats)
	}
	if len(observations) != 2 || !observations[0].ColdStart || !observations[0].Recycled ||
		observations[0].Success || observations[0].ErrorCode != "runtime_error" ||
		!observations[1].ColdStart || observations[1].Recycled || !observations[1].Success {
		t.Fatalf("pool observations = %#v", observations)
	}
}

func TestPythonRuntimePoolRecyclesProcessWhenProtocolRequestsIt(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	if _, err := pool.Execute(context.Background(), Run{ID: "recycle"}); err == nil {
		t.Fatal("Execute() expected a recycled request error")
	}
	result, err := pool.Execute(context.Background(), Run{ID: "after-recycle"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["sequence"] != float64(1) {
		t.Fatalf("recycled process was not replaced: %#v", result.Output)
	}
}

func TestPythonRuntimePoolKillsTimedOutProcess(t *testing.T) {
	t.Setenv("GO_WANT_PYTHON_POOL_HELPER", "1")
	pool := newPythonRuntimePool(
		PythonRuntimeExecutor{Executable: os.Args[0], Timeout: time.Second},
		1,
		[]string{"-test.run=^TestPythonRuntimePoolHelperProcess$"},
	)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := pool.Execute(ctx, Run{ID: "block"}); err == nil {
		t.Fatal("Execute() expected a timeout")
	}
	result, err := pool.Execute(context.Background(), Run{ID: "after-timeout"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output["sequence"] != float64(1) {
		t.Fatalf("timed out process was not replaced: %#v", result.Output)
	}
}

func TestPythonRuntimePoolHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_PYTHON_POOL_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	if os.Getenv("GO_WANT_PYTHON_POOL_READY_DELAY") == "1" {
		time.Sleep(10 * time.Second)
	}
	graphVersion := GraphVersion
	if os.Getenv("GO_WANT_PYTHON_POOL_WRONG_GRAPH") == "1" {
		graphVersion = "wrong-version"
	}
	if err := encoder.Encode(map[string]any{
		"type": "ready", "protocol_version": pythonRuntimeProtocolVersion,
		"graph_name": GraphName, "graph_version": graphVersion,
	}); err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	sequence := 0
	for scanner.Scan() {
		var run Run
		if err := json.Unmarshal(scanner.Bytes(), &run); err != nil {
			os.Exit(2)
		}
		switch run.ID {
		case "crash":
			os.Exit(3)
		case "block":
			time.Sleep(10 * time.Second)
		}
		sequence++
		if run.ID == "error" || run.ID == "recycle" || run.ID == "rate-limit" {
			errorBody := map[string]any{
				"code": "runtime_contract", "retryable": false,
				"recycle": run.ID == "recycle", "message": "request failed",
			}
			if run.ID == "rate-limit" {
				errorBody = map[string]any{
					"code": "model_unavailable", "retryable": true, "recycle": false,
					"provider_status": 429, "retry_after": "19", "message": "rate limited",
				}
			}
			_ = encoder.Encode(map[string]any{
				"ok":    false,
				"error": errorBody,
			})
			continue
		}
		_ = encoder.Encode(map[string]any{
			"ok": true,
			"result": map[string]any{
				"status": "completed",
				"output": map[string]any{"sequence": sequence, "pid": os.Getpid(), "model_config_version": os.Getenv("MODEL_CONFIG_VERSION")},
			},
		})
	}
	os.Exit(0)
}
