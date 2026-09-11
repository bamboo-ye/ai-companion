package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const (
	maxAgentSandboxInputBytes  = 1 << 20
	maxAgentSandboxOutputBytes = 4 << 20
)

var ErrSandboxUnavailable = errors.New("Agent sandbox unavailable")

type AgentSandboxIdentity struct {
	Key         string `json:"key"`
	VersionID   string `json:"version_id"`
	Fingerprint string `json:"fingerprint"`
}

type AgentSandboxRequest struct {
	Definition AgentDefinitionPayload `json:"definition"`
	Identity   AgentSandboxIdentity   `json:"identity"`
	Scenario   map[string]any         `json:"scenario"`
}

type AgentSandboxReport struct {
	SchemaVersion  string           `json:"schema_version"`
	RunID          string           `json:"run_id"`
	Mode           string           `json:"mode"`
	Status         string           `json:"status"`
	Outcome        string           `json:"outcome"`
	Response       string           `json:"response"`
	Definition     map[string]any   `json:"definition"`
	Safety         map[string]any   `json:"safety"`
	Budget         map[string]any   `json:"budget"`
	NodeOutputs    map[string]any   `json:"node_outputs"`
	NodeTrace      []map[string]any `json:"node_trace"`
	ModelCalls     []map[string]any `json:"model_calls"`
	ToolCalls      []map[string]any `json:"tool_calls"`
	ToolSelections []map[string]any `json:"tool_selections"`
	Interrupts     []map[string]any `json:"interrupts"`
	Recovery       map[string]any   `json:"recovery"`
}

type AgentSandbox interface {
	Run(context.Context, AgentSandboxRequest) (AgentSandboxReport, error)
}

// PythonAgentSandbox runs the production declarative graph compiler against
// in-memory model and tool fixtures. Its environment is intentionally built
// from an allowlist so provider credentials and database endpoints never
// enter the child process.
type PythonAgentSandbox struct {
	Executable string
	ModulePath string
	Timeout    time.Duration
}

func (s PythonAgentSandbox) Run(ctx context.Context, input AgentSandboxRequest) (AgentSandboxReport, error) {
	payload, err := json.Marshal(input)
	if err != nil || len(payload) == 0 || len(payload) > maxAgentSandboxInputBytes {
		return AgentSandboxReport{}, validationError("Agent sandbox input is too large or invalid")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	executable := strings.TrimSpace(s.Executable)
	if executable == "" {
		executable = "python3"
	}
	command := exec.CommandContext(ctx, executable, "-m", "ai_companion_worker.agent_sandbox")
	command.Stdin = bytes.NewReader(payload)
	command.Env = sandboxEnvironment(s.ModulePath)
	stdout := &sandboxBuffer{limit: maxAgentSandboxOutputBytes}
	stderr := &sandboxBuffer{limit: 8 << 10}
	command.Stdout, command.Stderr = stdout, stderr
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return AgentSandboxReport{}, fmt.Errorf("%w: execution timed out", ErrSandboxUnavailable)
		}
		return AgentSandboxReport{}, fmt.Errorf("%w: child process failed", ErrSandboxUnavailable)
	}
	if stdout.exceeded || stdout.Len() == 0 {
		return AgentSandboxReport{}, fmt.Errorf("%w: invalid runtime output", ErrSandboxUnavailable)
	}
	var envelope struct {
		OK     bool               `json:"ok"`
		Report AgentSandboxReport `json:"report"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err = json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return AgentSandboxReport{}, fmt.Errorf("%w: malformed runtime output", ErrSandboxUnavailable)
	}
	if !envelope.OK {
		if envelope.Error.Code == "validation" && strings.TrimSpace(envelope.Error.Message) != "" {
			return AgentSandboxReport{}, validationError("%s", envelope.Error.Message)
		}
		return AgentSandboxReport{}, fmt.Errorf("%w: synthetic runtime failed", ErrSandboxUnavailable)
	}
	if envelope.Report.SchemaVersion != "agent-studio-synthetic-run/v1" ||
		envelope.Report.Mode != "synthetic" || envelope.Report.RunID == "" ||
		envelope.Report.Status == "" {
		return AgentSandboxReport{}, fmt.Errorf("%w: runtime contract mismatch", ErrSandboxUnavailable)
	}
	return envelope.Report, nil
}

func sandboxEnvironment(modulePath string) []string {
	environment := map[string]string{
		"LANGGRAPH_STRICT_MSGPACK":  "true",
		"PYTHONDONTWRITEBYTECODE":   "1",
		"PYTHONUNBUFFERED":          "1",
		"AI_COMPANION_SANDBOX_MODE": "synthetic",
	}
	if value := strings.TrimSpace(os.Getenv("PATH")); value != "" {
		environment["PATH"] = value
	}
	if value := strings.TrimSpace(modulePath); value != "" {
		environment["PYTHONPATH"] = value
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]string, 0, len(keys))
	for _, key := range keys {
		items = append(items, key+"="+environment[key])
	}
	return items
}

type sandboxBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *sandboxBuffer) Write(value []byte) (int, error) {
	length := len(value)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return length, nil
	}
	if len(value) > remaining {
		b.exceeded = true
		value = value[:remaining]
	}
	_, _ = b.Buffer.Write(value)
	return length, nil
}
