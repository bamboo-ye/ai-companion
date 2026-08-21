package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
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
	Executable string
	ModulePath string
	Timeout    time.Duration
	OnObserve  func(PythonRuntimeObservation)
}

func (e PythonRuntimeExecutor) Execute(ctx context.Context, run Run) (result ExecutionResult, executionErr error) {
	startedAt := time.Now()
	if e.OnObserve != nil {
		defer func() {
			observation := PythonRuntimeObservation{
				RunID: run.ID, ExecutionMode: "process", ResultStatus: result.Status,
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

func (e PythonRuntimeExecutor) commandEnv() []string {
	environment := os.Environ()
	if modulePath := strings.TrimSpace(e.ModulePath); modulePath != "" {
		pythonPath := modulePath
		if existing := strings.TrimSpace(os.Getenv("PYTHONPATH")); existing != "" {
			pythonPath += string(os.PathListSeparator) + existing
		}
		environment = append(environment, "PYTHONPATH="+pythonPath)
	}
	return environment
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
