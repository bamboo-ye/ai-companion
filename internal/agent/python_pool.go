package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const pythonRuntimeProtocolVersion = "agent-runtime-jsonl-v1"

type PythonRuntimePool struct {
	config          PythonRuntimeExecutor
	args            []string
	permits         chan struct{}
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	processMu       sync.Mutex
	idleProcesses   []*pythonRuntimeProcess
	closed          bool
	active          sync.WaitGroup
	requests        atomic.Uint64
	requestErrors   atomic.Uint64
	processStarts   atomic.Uint64
	processDiscards atomic.Uint64
	onObserve       func(PythonRuntimeObservation)
}

type PythonRuntimePoolStats struct {
	Requests        uint64
	RequestErrors   uint64
	ProcessStarts   uint64
	ProcessDiscards uint64
}

type PythonRuntimeObservation struct {
	RunID           string
	ExecutionMode   string
	ResultStatus    string
	PoolWait        time.Duration
	Duration        time.Duration
	ModelCalls      int
	ModelDuration   time.Duration
	MaxModelTimeout time.Duration
	RuntimeOverhead time.Duration
	ColdStart       bool
	Recycled        bool
	Success         bool
	ErrorCode       string
}

type pythonRuntimeProcess struct {
	command  *exec.Cmd
	stdin    *bufio.Writer
	stdout   *bufio.Reader
	stderr   *boundedProcessBuffer
	stopOnce sync.Once
}

type pythonRuntimeEnvelope struct {
	OK     bool                    `json:"ok"`
	Result ExecutionResult         `json:"result"`
	Error  *pythonRuntimeErrorBody `json:"error"`
}

type pythonRuntimeReadyEnvelope struct {
	Type            string `json:"type"`
	ProtocolVersion string `json:"protocol_version"`
	GraphName       string `json:"graph_name"`
	GraphVersion    string `json:"graph_version"`
}

type pythonRuntimeErrorBody struct {
	Code           string `json:"code"`
	Retryable      bool   `json:"retryable"`
	Recycle        bool   `json:"recycle"`
	ProviderStatus int    `json:"provider_status"`
	RetryAfter     string `json:"retry_after"`
	Message        string `json:"message"`
}

type poolRoundTrip struct {
	result  ExecutionResult
	err     error
	healthy bool
}

func NewPythonRuntimePool(config PythonRuntimeExecutor, size int) *PythonRuntimePool {
	return newPythonRuntimePool(config, size, []string{"-m", "ai_companion_worker.agent_worker", "--serve"})
}

func newPythonRuntimePool(config PythonRuntimeExecutor, size int, args []string) *PythonRuntimePool {
	if size < 1 {
		size = 1
	}
	if size > 64 {
		size = 64
	}
	ctx, cancel := context.WithCancel(context.Background())
	pool := &PythonRuntimePool{
		config: config, args: append([]string(nil), args...),
		permits: make(chan struct{}, size), ctx: ctx, cancel: cancel,
	}
	for index := 0; index < size; index++ {
		pool.permits <- struct{}{}
	}
	return pool
}

func (p *PythonRuntimePool) Execute(ctx context.Context, run Run) (executionResult ExecutionResult, executionErr error) {
	if p == nil || p.config.Executable == "" && len(p.args) == 0 {
		return ExecutionResult{}, ErrValidation
	}
	if err := p.beginExecution(); err != nil {
		return ExecutionResult{}, err
	}
	p.requests.Add(1)
	defer p.active.Done()
	startedAt := time.Now()
	observation := PythonRuntimeObservation{RunID: run.ID}
	defer func() {
		observation.Duration = time.Since(startedAt)
		observation.Success = executionErr == nil
		observation.ErrorCode = pythonRuntimeErrorCode(executionErr)
		observation.ResultStatus = strings.TrimSpace(executionResult.Status)
		if mode, ok := executionResult.Output["execution_mode"].(string); ok {
			observation.ExecutionMode = strings.TrimSpace(mode)
		}
		populateModelTiming(&observation, executionResult)
		p.observe(observation)
	}()
	if p.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.config.Timeout)
		defer cancel()
	}
	var process *pythonRuntimeProcess
	poolWaitStarted := time.Now()
	select {
	case <-p.permits:
		observation.PoolWait = time.Since(poolWaitStarted)
		process = p.takeIdleProcess()
	case <-ctx.Done():
		observation.PoolWait = time.Since(poolWaitStarted)
		p.requestErrors.Add(1)
		return ExecutionResult{}, ctx.Err()
	case <-p.ctx.Done():
		observation.PoolWait = time.Since(poolWaitStarted)
		p.requestErrors.Add(1)
		return ExecutionResult{}, context.Canceled
	}
	healthy := false
	defer func() {
		if !healthy && process != nil {
			observation.Recycled = true
			p.processDiscards.Add(1)
			process.stop()
			process = nil
		}
		if process != nil && p.ctx.Err() == nil {
			p.putIdleProcess(process)
			process = nil
		}
		if process != nil {
			process.stop()
		}
		p.permits <- struct{}{}
	}()
	if process == nil {
		observation.ColdStart = true
		var err error
		process, err = p.startProcess(ctx)
		if err != nil {
			p.requestErrors.Add(1)
			return ExecutionResult{}, err
		}
	}
	payload, err := json.Marshal(run)
	if err != nil {
		p.requestErrors.Add(1)
		return ExecutionResult{}, err
	}
	if len(payload) == 0 || len(payload) > maxRuntimeOutputBytes {
		p.requestErrors.Add(1)
		return ExecutionResult{}, errors.New("Python Agent runtime input is too large")
	}
	resultCh := make(chan poolRoundTrip, 1)
	go func() {
		result, roundTripErr, processHealthy := process.roundTrip(payload)
		resultCh <- poolRoundTrip{result: result, err: roundTripErr, healthy: processHealthy}
	}()
	select {
	case result := <-resultCh:
		healthy = result.healthy
		if result.err != nil {
			p.requestErrors.Add(1)
		}
		return result.result, result.err
	case <-ctx.Done():
		process.stop()
		result := <-resultCh
		healthy = result.healthy
		p.requestErrors.Add(1)
		return ExecutionResult{}, ctx.Err()
	case <-p.ctx.Done():
		process.stop()
		result := <-resultCh
		healthy = result.healthy
		p.requestErrors.Add(1)
		return ExecutionResult{}, context.Canceled
	}
}

func populateModelTiming(observation *PythonRuntimeObservation, result ExecutionResult) {
	if observation == nil {
		return
	}
	model, ok := result.Output["model"].(map[string]any)
	if !ok {
		observation.RuntimeOverhead = observation.Duration
		return
	}
	calls, ok := model["calls"].([]any)
	if !ok {
		observation.RuntimeOverhead = observation.Duration
		return
	}
	for _, raw := range calls {
		call, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		observation.ModelCalls++
		latency, ok := call["latency_ms"].(float64)
		if ok && latency >= 0 {
			observation.ModelDuration += time.Duration(latency * float64(time.Millisecond))
		}
		timeout, ok := call["timeout_ms"].(float64)
		if ok && timeout >= 0 {
			value := time.Duration(timeout * float64(time.Millisecond))
			if value > observation.MaxModelTimeout {
				observation.MaxModelTimeout = value
			}
		}
	}
	if observation.Duration > observation.ModelDuration {
		observation.RuntimeOverhead = observation.Duration - observation.ModelDuration
	}
}

// Warm ensures up to count runtime processes have completed Python imports,
// graph compilation and checkpointer initialization before traffic arrives.
// A failed warmup does not close the pool; callers may continue with lazy start.
func (p *PythonRuntimePool) Warm(ctx context.Context, count int) (int, error) {
	if p == nil || count < 0 {
		return 0, ErrValidation
	}
	if count == 0 {
		return 0, nil
	}
	if count > cap(p.permits) {
		count = cap(p.permits)
	}
	if err := p.beginExecution(); err != nil {
		return 0, err
	}
	defer p.active.Done()
	reserved := 0
	defer func() {
		for index := 0; index < reserved; index++ {
			p.permits <- struct{}{}
		}
	}()
	for reserved < count {
		select {
		case <-p.permits:
			reserved++
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-p.ctx.Done():
			return 0, context.Canceled
		}
	}
	processes := make([]*pythonRuntimeProcess, 0, count)
	for len(processes) < count {
		process := p.takeIdleProcess()
		if process == nil {
			break
		}
		processes = append(processes, process)
	}
	missing := count - len(processes)
	type startResult struct {
		process *pythonRuntimeProcess
		err     error
	}
	results := make(chan startResult, missing)
	for index := 0; index < missing; index++ {
		go func() {
			process, err := p.startProcess(ctx)
			results <- startResult{process: process, err: err}
		}()
	}
	var warmupErrors []error
	for index := 0; index < missing; index++ {
		result := <-results
		if result.err != nil {
			warmupErrors = append(warmupErrors, result.err)
			continue
		}
		processes = append(processes, result.process)
	}
	if p.ctx.Err() != nil {
		for _, process := range processes {
			process.stop()
		}
		return 0, context.Canceled
	}
	for _, process := range processes {
		p.putIdleProcess(process)
	}
	return len(processes), errors.Join(warmupErrors...)
}

func (p *PythonRuntimePool) SetObservationHandler(handler func(PythonRuntimeObservation)) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.onObserve = handler
	p.mu.Unlock()
}

func (p *PythonRuntimePool) observe(observation PythonRuntimeObservation) {
	p.mu.Lock()
	handler := p.onObserve
	p.mu.Unlock()
	if handler != nil {
		handler(observation)
	}
}

func pythonRuntimeErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified interface{ Code() string }
	if errors.As(err, &classified) && strings.TrimSpace(classified.Code()) != "" {
		return strings.TrimSpace(classified.Code())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "runtime_error"
}

func (p *PythonRuntimePool) Stats() PythonRuntimePoolStats {
	if p == nil {
		return PythonRuntimePoolStats{}
	}
	return PythonRuntimePoolStats{
		Requests:        p.requests.Load(),
		RequestErrors:   p.requestErrors.Load(),
		ProcessStarts:   p.processStarts.Load(),
		ProcessDiscards: p.processDiscards.Load(),
	}
}

func (p *PythonRuntimePool) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.cancel()
	p.mu.Unlock()
	p.active.Wait()
	p.processMu.Lock()
	processes := p.idleProcesses
	p.idleProcesses = nil
	p.processMu.Unlock()
	for _, process := range processes {
		process.stop()
	}
}

func (p *PythonRuntimePool) takeIdleProcess() *pythonRuntimeProcess {
	p.processMu.Lock()
	defer p.processMu.Unlock()
	last := len(p.idleProcesses) - 1
	if last < 0 {
		return nil
	}
	process := p.idleProcesses[last]
	p.idleProcesses = p.idleProcesses[:last]
	return process
}

func (p *PythonRuntimePool) putIdleProcess(process *pythonRuntimeProcess) {
	p.processMu.Lock()
	p.idleProcesses = append(p.idleProcesses, process)
	p.processMu.Unlock()
}

func (p *PythonRuntimePool) beginExecution() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return context.Canceled
	}
	p.active.Add(1)
	return nil
}

func (p *PythonRuntimePool) startProcess(ctx context.Context) (*pythonRuntimeProcess, error) {
	executable := strings.TrimSpace(p.config.Executable)
	if executable == "" {
		executable = "python3"
	}
	command := exec.Command(executable, p.args...)
	command.Env = p.config.commandEnv()
	stdinPipe, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, err
	}
	stderr := &boundedProcessBuffer{maximum: 4000}
	command.Stderr = stderr
	if err = command.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return nil, err
	}
	p.processStarts.Add(1)
	process := &pythonRuntimeProcess{
		command: command,
		stdin:   bufio.NewWriter(stdinPipe),
		stdout:  bufio.NewReaderSize(stdoutPipe, 64*1024),
		stderr:  stderr,
	}
	readyCh := make(chan error, 1)
	go func() { readyCh <- process.awaitReady() }()
	select {
	case err = <-readyCh:
		if err == nil {
			return process, nil
		}
		process.stop()
		p.processDiscards.Add(1)
		var classified *ExecutorError
		if errors.As(err, &classified) {
			return nil, classified
		}
		return nil, &ExecutorError{
			ErrorCode: "runtime_startup", ErrorMessage: err.Error(), ShouldRetry: true,
		}
	case <-ctx.Done():
		process.stop()
		<-readyCh
		p.processDiscards.Add(1)
		return nil, ctx.Err()
	case <-p.ctx.Done():
		process.stop()
		<-readyCh
		p.processDiscards.Add(1)
		return nil, context.Canceled
	}
}

func (p *pythonRuntimeProcess) awaitReady() error {
	line, err := p.stdout.ReadBytes('\n')
	if err != nil {
		return p.transportError(err)
	}
	if len(line) == 0 || len(line) > maxRuntimeOutputBytes {
		return runtimeContractError("Python Agent runtime returned invalid ready envelope")
	}
	var ready pythonRuntimeReadyEnvelope
	if err = json.Unmarshal(line, &ready); err != nil {
		return runtimeContractError(
			fmt.Sprintf("decode Python Agent runtime ready envelope: %v", err),
		)
	}
	if ready.Type != "ready" || ready.ProtocolVersion != pythonRuntimeProtocolVersion ||
		ready.GraphName != GraphName || ready.GraphVersion != GraphVersion {
		return runtimeContractError(fmt.Sprintf(
			"Python Agent runtime identity mismatch: protocol=%q graph=%q version=%q",
			ready.ProtocolVersion, ready.GraphName, ready.GraphVersion,
		))
	}
	return nil
}

func (p *pythonRuntimeProcess) roundTrip(payload []byte) (ExecutionResult, error, bool) {
	if _, err := p.stdin.Write(payload); err != nil {
		return ExecutionResult{}, p.transportError(err), false
	}
	if err := p.stdin.WriteByte('\n'); err != nil {
		return ExecutionResult{}, p.transportError(err), false
	}
	if err := p.stdin.Flush(); err != nil {
		return ExecutionResult{}, p.transportError(err), false
	}
	line, err := p.stdout.ReadBytes('\n')
	if err != nil {
		return ExecutionResult{}, p.transportError(err), false
	}
	if len(line) == 0 || len(line) > maxRuntimeOutputBytes {
		return ExecutionResult{}, runtimeContractError("Python Agent runtime returned invalid output"), false
	}
	var envelope pythonRuntimeEnvelope
	if err = json.Unmarshal(line, &envelope); err != nil {
		return ExecutionResult{}, runtimeContractError(
			fmt.Sprintf("decode Python Agent runtime envelope: %v", err),
		), false
	}
	if !envelope.OK {
		if envelope.Error == nil || strings.TrimSpace(envelope.Error.Message) == "" {
			return ExecutionResult{}, runtimeContractError("Python Agent runtime returned invalid error"), false
		}
		code := strings.TrimSpace(envelope.Error.Code)
		if code == "" {
			code = "runtime_contract"
		}
		return ExecutionResult{}, &ExecutorError{
			ErrorCode: code, ErrorMessage: strings.TrimSpace(envelope.Error.Message),
			ShouldRetry:     envelope.Error.Retryable,
			ProviderStatus:  envelope.Error.ProviderStatus,
			RetryAfterValue: strings.TrimSpace(envelope.Error.RetryAfter),
		}, !envelope.Error.Recycle
	}
	if envelope.Result.Status != "completed" && envelope.Result.Status != "waiting_approval" && envelope.Result.Status != "waiting_tool" {
		return ExecutionResult{}, runtimeContractError("Python Agent runtime returned invalid output"), false
	}
	return envelope.Result, nil, true
}

func (p *pythonRuntimeProcess) transportError(cause error) error {
	message := strings.TrimSpace(p.stderr.String())
	if message == "" {
		message = cause.Error()
	}
	return fmt.Errorf("Python Agent runtime process failed: %s", message)
}

func (p *pythonRuntimeProcess) stop() {
	p.stopOnce.Do(func() {
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
		_ = p.command.Wait()
	})
}

type boundedProcessBuffer struct {
	mu      sync.Mutex
	maximum int
	value   []byte
}

func (b *boundedProcessBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(value)
	b.value = append(b.value, value...)
	if b.maximum > 0 && len(b.value) > b.maximum {
		b.value = append([]byte(nil), b.value[len(b.value)-b.maximum:]...)
	}
	return written, nil
}

func (b *boundedProcessBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(append([]byte(nil), b.value...))
}

var _ io.Writer = (*boundedProcessBuffer)(nil)
