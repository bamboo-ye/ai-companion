package skill

import (
	"encoding/json"
	"errors"
	"strings"
)

const ToolFailureContractVersion = "tool-failure-v1"

// ToolFailure is the machine-readable failure contract shared by workers,
// the Skill service, the Agent gateway, and the recovery graph.
type ToolFailure struct {
	ContractVersion string         `json:"contract_version"`
	Code            string         `json:"code"`
	Category        string         `json:"category"`
	Phase           string         `json:"phase"`
	Message         string         `json:"message,omitempty"`
	RetrySameInput  bool           `json:"retry_same_input"`
	Repairable      bool           `json:"repairable"`
	SideEffectState string         `json:"side_effect_state"`
	FieldPaths      []string       `json:"field_paths,omitempty"`
	AllowedRepairs  []string       `json:"allowed_repairs,omitempty"`
	SafeDetails     map[string]any `json:"safe_details,omitempty"`
}

// ToolExecutionError preserves a trusted ToolFailure while satisfying error.
type ToolExecutionError struct {
	Failure ToolFailure
	Cause   error
}

func (e *ToolExecutionError) Error() string {
	if message := strings.TrimSpace(e.Failure.Message); message != "" {
		return message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return strings.TrimSpace(e.Failure.Code)
}

func (e *ToolExecutionError) Unwrap() error { return e.Cause }

func NewToolExecutionError(failure ToolFailure, cause error) error {
	failure = normalizeToolFailure(failure)
	return &ToolExecutionError{Failure: failure, Cause: cause}
}

func toolFailureFromError(cause error, fallbackCode string) ToolFailure {
	var structured *ToolExecutionError
	if errors.As(cause, &structured) {
		return normalizeToolFailure(structured.Failure)
	}
	code := strings.TrimSpace(fallbackCode)
	if code == "" {
		code = "tool_failed"
	}
	failure := ToolFailure{
		ContractVersion: ToolFailureContractVersion,
		Code:            code,
		Category:        "execution",
		Phase:           "execution",
		Message:         strings.TrimSpace(cause.Error()),
		RetrySameInput:  false,
		Repairable:      false,
		SideEffectState: "unknown",
	}
	if failure.Message == "" {
		failure.Message = "工具执行失败。"
	}
	if len(failure.Message) > 1024 {
		failure.Message = failure.Message[:1024]
	}
	if code == "tool_timeout" {
		failure.Category = "transient"
		failure.Message = "工具执行超时，执行结果尚未确认。"
	}
	if code == "tool_cancelled" {
		failure.Category = "cancelled"
		failure.Message = "工具执行已取消。"
	}
	return failure
}

func normalizeToolFailure(failure ToolFailure) ToolFailure {
	failure.ContractVersion = ToolFailureContractVersion
	failure.Code = strings.TrimSpace(failure.Code)
	if failure.Code == "" {
		failure.Code = "tool_failed"
	}
	failure.Category = strings.TrimSpace(failure.Category)
	if failure.Category == "" {
		failure.Category = "execution"
	}
	failure.Phase = strings.TrimSpace(failure.Phase)
	if failure.Phase == "" {
		failure.Phase = "execution"
	}
	failure.SideEffectState = strings.TrimSpace(failure.SideEffectState)
	if failure.SideEffectState == "" {
		failure.SideEffectState = "unknown"
	}
	failure.Message = strings.TrimSpace(failure.Message)
	return failure
}

func failureOutput(existing json.RawMessage, failure ToolFailure) json.RawMessage {
	output := make(map[string]any)
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &output)
	}
	output["failure"] = normalizeToolFailure(failure)
	encoded, err := json.Marshal(output)
	if err != nil {
		return existing
	}
	return encoded
}

func toolFailureFromOutput(output json.RawMessage) (ToolFailure, bool) {
	if len(output) == 0 {
		return ToolFailure{}, false
	}
	var envelope struct {
		Failure ToolFailure `json:"failure"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil || strings.TrimSpace(envelope.Failure.Code) == "" {
		return ToolFailure{}, false
	}
	return normalizeToolFailure(envelope.Failure), true
}
