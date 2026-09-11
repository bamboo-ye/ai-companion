package agent

import (
	"context"
	"time"
)

// OperationsStore is the read-only projection used by the operator console.
// It deliberately stays separate from Store so lightweight runtime stores and
// test doubles do not need to implement administration-only queries.
type OperationsStore interface {
	ListAgentRuns(context.Context, RunOperationsFilter) ([]RunSummary, error)
	GetAgentRun(context.Context, string) (Run, error)
	ListAgentRunEvents(context.Context, string, int64, int) ([]Event, error)
	ListAgentToolCalls(context.Context, string) ([]ToolCallSummary, error)
}

type RunOperationsFilter struct {
	Status          string
	Module          string
	ErrorCode       string
	Query           string
	CreatedFrom     *time.Time
	CreatedTo       *time.Time
	CursorCreatedAt *time.Time
	CursorID        string
	Limit           int
}

// RunSummary contains operational metadata only. User prompts, model output,
// tool arguments and tool results are intentionally excluded.
type RunSummary struct {
	ID                         string     `json:"id"`
	ThreadID                   string     `json:"thread_id"`
	UserID                     string     `json:"user_id"`
	ConversationID             string     `json:"conversation_id"`
	Module                     string     `json:"module"`
	GraphName                  string     `json:"graph_name"`
	GraphVersion               string     `json:"graph_version"`
	AgentDefinitionKey         string     `json:"agent_definition_key,omitempty"`
	AgentDefinitionVersionID   string     `json:"agent_definition_version_id,omitempty"`
	AgentDefinitionVersion     int        `json:"agent_definition_version,omitempty"`
	AgentDefinitionRevision    int        `json:"agent_definition_revision,omitempty"`
	AgentDefinitionFingerprint string     `json:"agent_definition_fingerprint,omitempty"`
	ModelProfileKey            string     `json:"model_profile_key,omitempty"`
	ModelVersionID             string     `json:"model_profile_version_id,omitempty"`
	ModelRevision              int        `json:"model_profile_revision,omitempty"`
	ModelConfig                string     `json:"model_profile_config_version,omitempty"`
	ModelFingerprint           string     `json:"model_profile_fingerprint,omitempty"`
	Status                     string     `json:"status"`
	CurrentNode                string     `json:"current_node,omitempty"`
	Revision                   int        `json:"revision"`
	ModelCalls                 int64      `json:"model_calls"`
	PromptTokens               int64      `json:"prompt_tokens"`
	CompletionTokens           int64      `json:"completion_tokens"`
	CostMicros                 int64      `json:"cost_micros"`
	ToolCalls                  int64      `json:"tool_calls"`
	ErrorCode                  string     `json:"error_code,omitempty"`
	ErrorMessage               string     `json:"error_message,omitempty"`
	DurationMS                 int64      `json:"duration_ms"`
	DeadlineAt                 time.Time  `json:"deadline_at"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
	CompletedAt                *time.Time `json:"completed_at,omitempty"`
}

// ToolCallSummary excludes arguments and results because they can contain
// user content or credentials returned by an integration.
type ToolCallSummary struct {
	ID           string     `json:"id"`
	RunID        string     `json:"run_id"`
	ToolName     string     `json:"tool_name"`
	RiskLevel    string     `json:"risk_level"`
	Status       string     `json:"status"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}
