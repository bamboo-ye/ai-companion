package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

const maxRuntimeOutputBytes = 4 << 20

const runtimeErrorPrefix = "agent runtime error:"

type ExecutorError struct {
	ErrorCode       string
	ErrorMessage    string
	ShouldRetry     bool
	ProviderStatus  int
	RetryAfterValue string
}

func (e *ExecutorError) Error() string {
	return e.ErrorMessage
}

func (e *ExecutorError) Code() string {
	return e.ErrorCode
}

func (e *ExecutorError) Retryable() bool {
	return e.ShouldRetry
}

func (e *ExecutorError) StatusCode() int {
	return e.ProviderStatus
}

func (e *ExecutorError) RetryAfter(now time.Time) (time.Duration, bool) {
	return parseRetryAfter(e.RetryAfterValue, now)
}

func runtimeContractError(message string) *ExecutorError {
	return &ExecutorError{
		ErrorCode: "runtime_contract", ErrorMessage: strings.TrimSpace(message),
		ShouldRetry: false,
	}
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 || seconds > int64((24*time.Hour)/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := when.Sub(now)
	if delay < 0 {
		delay = 0
	}
	if delay > 24*time.Hour {
		return 0, false
	}
	return delay, true
}

type PythonRuntimeExecutor struct {
	Executable  string
	ModulePath  string
	Timeout     time.Duration
	OnObserve   func(PythonRuntimeObservation)
	Environment map[string]string
}

func (e PythonRuntimeExecutor) Execute(ctx context.Context, run Run) (result ExecutionResult, executionErr error) {
	startedAt := time.Now()
	if e.OnObserve != nil {
		defer func() {
			observation := PythonRuntimeObservation{
				RunID: run.ID, TraceID: tracectx.ID(ctx), ExecutionMode: "process", ResultStatus: result.Status,
				Duration: time.Since(startedAt), Success: executionErr == nil,
				ErrorCode: pythonRuntimeErrorCode(executionErr),
			}
			populateModelTiming(&observation, result)
			e.OnObserve(observation)
		}()
	}
	executable := strings.TrimSpace(e.Executable)
	if executable == "" {
		executable = "python3"
	}
	if e.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.Timeout)
		defer cancel()
	}
	run = runtimeRunWithTraceContext(ctx, run)
	payload, err := json.Marshal(run)
	if err != nil {
		return ExecutionResult{}, err
	}
	command := exec.CommandContext(ctx, executable, "-m", "ai_companion_worker.agent_worker")
	command.Stdin = bytes.NewReader(payload)
	command.Env = e.commandEnv()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err = command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ExecutionResult{}, ctxErr
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 4000 {
			message = message[:4000]
		}
		if message == "" {
			message = err.Error()
		}
		if structured := parseExecutorError(message); structured != nil {
			return ExecutionResult{}, structured
		}
		return ExecutionResult{}, fmt.Errorf("Python Agent runtime failed: %s", message)
	}
	if stdout.Len() == 0 || stdout.Len() > maxRuntimeOutputBytes {
		return ExecutionResult{}, runtimeContractError("Python Agent runtime returned invalid output")
	}
	result = ExecutionResult{}
	if err = json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return ExecutionResult{}, runtimeContractError(
			fmt.Sprintf("decode Python Agent runtime output: %v", err),
		)
	}
	if result.Status != "completed" && result.Status != "waiting_approval" && result.Status != "waiting_tool" {
		return ExecutionResult{}, runtimeContractError("Python Agent runtime returned invalid output")
	}
	return result, nil
}

func runtimeRunWithTraceContext(ctx context.Context, run Run) Run {
	run.OTelTraceParent = tracectx.TraceParent(ctx)
	run.OTelTraceState = tracectx.TraceState(ctx)
	return run
}

func (e PythonRuntimeExecutor) commandEnv() []string {
	overrides := cloneRuntimeEnvironment(e.Environment)
	if modulePath := strings.TrimSpace(e.ModulePath); modulePath != "" {
		pythonPath := modulePath
		if existing := strings.TrimSpace(overrides["PYTHONPATH"]); existing != "" {
			pythonPath += string(os.PathListSeparator) + existing
		} else if existing = strings.TrimSpace(os.Getenv("PYTHONPATH")); existing != "" {
			pythonPath += string(os.PathListSeparator) + existing
		}
		overrides["PYTHONPATH"] = pythonPath
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[key]; !replaced {
			environment = append(environment, item)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+overrides[key])
	}
	return environment
}

type ReloadablePythonRuntimeExecutor struct {
	mu     sync.RWMutex
	config PythonRuntimeExecutor
}

func NewReloadablePythonRuntimeExecutor(config PythonRuntimeExecutor) *ReloadablePythonRuntimeExecutor {
	config.Environment = cloneRuntimeEnvironment(config.Environment)
	return &ReloadablePythonRuntimeExecutor{config: config}
}

func (e *ReloadablePythonRuntimeExecutor) Execute(ctx context.Context, run Run) (ExecutionResult, error) {
	if e == nil {
		return ExecutionResult{}, ErrValidation
	}
	e.mu.RLock()
	config := e.config
	config.Environment = cloneRuntimeEnvironment(e.config.Environment)
	e.mu.RUnlock()
	return config.Execute(ctx, run)
}

func (e *ReloadablePythonRuntimeExecutor) ReloadEnvironment(environment map[string]string) bool {
	if e == nil {
		return false
	}
	next := cloneRuntimeEnvironment(environment)
	e.mu.Lock()
	defer e.mu.Unlock()
	if equalRuntimeEnvironment(e.config.Environment, next) {
		return false
	}
	e.config.Environment = next
	return true
}

func cloneRuntimeEnvironment(environment map[string]string) map[string]string {
	cloned := make(map[string]string, len(environment))
	for key, value := range environment {
		key = strings.TrimSpace(key)
		if key != "" && !strings.ContainsAny(key, "=\x00") && !strings.ContainsRune(value, '\x00') {
			cloned[key] = value
		}
	}
	return cloned
}

func equalRuntimeEnvironment(first, second map[string]string) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if second[key] != value {
			return false
		}
	}
	return true
}

func parseExecutorError(stderr string) *ExecutorError {
	index := strings.LastIndex(stderr, runtimeErrorPrefix)
	if index < 0 {
		return nil
	}
	raw := strings.TrimSpace(stderr[index+len(runtimeErrorPrefix):])
	var envelope struct {
		Code           string `json:"code"`
		Retryable      bool   `json:"retryable"`
		ProviderStatus int    `json:"provider_status"`
		RetryAfter     string `json:"retry_after"`
		Message        string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil
	}
	envelope.Code = strings.TrimSpace(envelope.Code)
	envelope.Message = strings.TrimSpace(envelope.Message)
	if envelope.Code == "" || envelope.Message == "" {
		return nil
	}
	return &ExecutorError{
		ErrorCode: envelope.Code, ErrorMessage: envelope.Message,
		ShouldRetry: envelope.Retryable, ProviderStatus: envelope.ProviderStatus,
		RetryAfterValue: strings.TrimSpace(envelope.RetryAfter),
	}
}
