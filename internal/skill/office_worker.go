package skill

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const maxOfficeWorkerOutput = 15 << 20

type OfficeWorker interface {
	Execute(context.Context, string, map[string]any) (ToolResult, error)
}

type PythonOfficeWorker struct {
	Executable string
	ModulePath string
	Timeout    time.Duration
}

type officeWorkerResponse struct {
	Output map[string]any        `json:"output"`
	Files  []officeWorkerFileDTO `json:"files"`
}

type officeWorkerFailure struct {
	ContractVersion string         `json:"contract_version"`
	Code            string         `json:"code"`
	Category        string         `json:"category"`
	Phase           string         `json:"phase"`
	Message         string         `json:"message"`
	RetrySameInput  bool           `json:"retry_same_input"`
	Repairable      bool           `json:"repairable"`
	SideEffectState string         `json:"side_effect_state"`
	FieldPaths      []string       `json:"field_paths"`
	AllowedRepairs  []string       `json:"allowed_repairs"`
	SafeDetails     map[string]any `json:"safe_details"`
}

type officeWorkerFileDTO struct {
	Name       string `json:"name"`
	MediaType  string `json:"media_type"`
	DataBase64 string `json:"data_base64"`
}

func (w PythonOfficeWorker) Execute(ctx context.Context, operation string, input map[string]any) (ToolResult, error) {
	executable := strings.TrimSpace(w.Executable)
	if executable == "" {
		executable = "python3"
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	request, err := json.Marshal(map[string]any{"operation": operation, "input": input})
	if err != nil {
		return ToolResult{}, fmt.Errorf("encode office worker request: %w", err)
	}
	executeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(executeCtx, executable, "-m", "ai_companion_worker.office_tools")
	command.Stdin = bytes.NewReader(request)
	command.Env = os.Environ()
	if modulePath := strings.TrimSpace(w.ModulePath); modulePath != "" {
		command.Env = append(command.Env, "PYTHONPATH="+modulePath)
	}
	stdout := &boundedBuffer{remaining: maxOfficeWorkerOutput + 1}
	stderr := &boundedBuffer{remaining: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err = command.Run(); err != nil {
		if errors.Is(executeCtx.Err(), context.DeadlineExceeded) {
			return ToolResult{}, fmt.Errorf("office worker timed out")
		}
		message := strings.TrimSpace(stderr.String())
		if failure, ok := parseOfficeWorkerFailure(message); ok {
			return ToolResult{}, NewToolExecutionError(failure, err)
		}
		return ToolResult{}, fmt.Errorf("office worker failed: %s", message)
	}
	if stdout.total > maxOfficeWorkerOutput {
		return ToolResult{}, fmt.Errorf("office worker output exceeds limit")
	}
	var response officeWorkerResponse
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return ToolResult{}, fmt.Errorf("decode office worker result: %w", err)
	}
	if response.Output == nil {
		return ToolResult{}, errors.New("office worker returned no output")
	}
	result := ToolResult{Output: response.Output, Files: make([]FileOutput, 0, len(response.Files))}
	for _, file := range response.Files {
		data, decodeErr := base64.StdEncoding.Strict().DecodeString(file.DataBase64)
		if decodeErr != nil {
			return ToolResult{}, fmt.Errorf("decode office worker file: %w", decodeErr)
		}
		result.Files = append(result.Files, FileOutput{Name: file.Name, MediaType: file.MediaType, Data: data})
	}
	return result, nil
}

func parseOfficeWorkerFailure(value string) (ToolFailure, bool) {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		var envelope officeWorkerFailure
		if json.Unmarshal([]byte(strings.TrimSpace(lines[index])), &envelope) != nil ||
			envelope.ContractVersion != ToolFailureContractVersion ||
			strings.TrimSpace(envelope.Code) == "" {
			continue
		}
		return normalizeToolFailure(ToolFailure{
			ContractVersion: envelope.ContractVersion,
			Code:            envelope.Code,
			Category:        envelope.Category,
			Phase:           envelope.Phase,
			Message:         envelope.Message,
			RetrySameInput:  envelope.RetrySameInput,
			Repairable:      envelope.Repairable,
			SideEffectState: envelope.SideEffectState,
			FieldPaths:      envelope.FieldPaths,
			AllowedRepairs:  envelope.AllowedRepairs,
			SafeDetails:     envelope.SafeDetails,
		}), true
	}
	return ToolFailure{}, false
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	total     int
}

func (w *boundedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	w.total += original
	if w.remaining > 0 {
		if len(data) > w.remaining {
			data = data[:w.remaining]
		}
		_, _ = w.buffer.Write(data)
		w.remaining -= len(data)
	}
	return original, nil
}

func (w *boundedBuffer) Bytes() []byte  { return w.buffer.Bytes() }
func (w *boundedBuffer) String() string { return w.buffer.String() }

func officeWorkerHandler(worker OfficeWorker, operation string) Handler {
	return HandlerFunc(func(ctx context.Context, input map[string]any) (ToolResult, error) {
		if worker == nil {
			return ToolResult{}, errors.New("office worker is unavailable")
		}
		return worker.Execute(ctx, operation, input)
	})
}
