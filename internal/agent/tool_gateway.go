package agent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/chatattachment"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/skill"
)

var (
	ErrToolDenied        = errors.New("agent tool is not allowed")
	ErrInvalidToken      = errors.New("agent confirmation token is invalid")
	ErrConfirmationState = errors.New("agent tool does not require confirmation")
)

const defaultConfirmationTTL = 15 * time.Minute

type RunReader interface {
	Get(context.Context, string) (Run, error)
}

type ToolPrepareInput struct {
	CallKey          string         `json:"call_key"`
	ToolName         string         `json:"tool_name"`
	Arguments        map[string]any `json:"arguments"`
	ExpectedRevision int            `json:"-"`
}

type ToolPreparation struct {
	Status              string         `json:"status"`
	ToolName            string         `json:"tool_name"`
	Response            string         `json:"response,omitempty"`
	RiskLevel           string         `json:"risk_level"`
	Summary             string         `json:"summary,omitempty"`
	ConfirmationToken   string         `json:"confirmation_token,omitempty"`
	NormalizedArguments map[string]any `json:"normalized_arguments"`
	Data                any            `json:"data,omitempty"`
}

type ToolCommitInput struct {
	CallKey           string `json:"call_key"`
	ConfirmationToken string `json:"confirmation_token"`
	Approved          bool   `json:"approved"`
	ExpectedRevision  int    `json:"-"`
}

type ToolRetryInput struct {
	CallKey          string         `json:"call_key"`
	OperatorID       string         `json:"operator_id"`
	Arguments        map[string]any `json:"arguments"`
	ExpectedRevision int            `json:"-"`
}

type ToolOutcome struct {
	Response string `json:"response"`
	Data     any    `json:"data,omitempty"`
}

type ToolGateway struct {
	runs     RunReader
	tools    conversation.ModelToolExecutor
	ledger   *ledger.Service
	planner  *planner.Service
	skills   *skill.Service
	secret   []byte
	tokenTTL time.Duration
	now      func() time.Time
}

type agentRunInput struct {
	MessageID string `json:"message_id"`
	Text      string `json:"text"`
	Context   struct {
		History []conversation.Message `json:"history"`
	} `json:"context"`
}

type confirmationClaims struct {
	Version     int               `json:"v"`
	RunID       string            `json:"run_id"`
	UserID      string            `json:"user_id"`
	ToolName    string            `json:"tool_name"`
	PrepareKey  string            `json:"prepare_key"`
	Kind        string            `json:"kind"`
	CandidateID string            `json:"candidate_id"`
	Summary     string            `json:"summary"`
	Payload     map[string]string `json:"payload,omitempty"`
	ExpiresAt   int64             `json:"exp"`
}

func NewToolGateway(
	runs RunReader,
	tools conversation.ModelToolExecutor,
	ledgerService *ledger.Service,
	plannerService *planner.Service,
	skillService *skill.Service,
	secret string,
	tokenTTL time.Duration,
) *ToolGateway {
	return newToolGatewayWithClock(runs, tools, ledgerService, plannerService, skillService, secret, tokenTTL, time.Now)
}

func newToolGatewayWithClock(
	runs RunReader,
	tools conversation.ModelToolExecutor,
	ledgerService *ledger.Service,
	plannerService *planner.Service,
	skillService *skill.Service,
	secret string,
	tokenTTL time.Duration,
	now func() time.Time,
) *ToolGateway {
	if tokenTTL <= 0 {
		tokenTTL = defaultConfirmationTTL
	}
	if now == nil {
		now = time.Now
	}
	return &ToolGateway{
		runs: runs, tools: tools, ledger: ledgerService, planner: plannerService,
		skills: skillService, secret: []byte(secret), tokenTTL: tokenTTL, now: now,
	}
}

func (g *ToolGateway) Prepare(ctx context.Context, runID string, input ToolPrepareInput) (ToolPreparation, error) {
	run, request, err := g.loadRequest(ctx, runID, input.ExpectedRevision)
	if err != nil {
		return ToolPreparation{}, err
	}
	input.CallKey = strings.TrimSpace(input.CallKey)
	input.ToolName = strings.TrimSpace(input.ToolName)
	if input.CallKey == "" || len(input.CallKey) > 128 || input.ToolName == "" || len(input.ToolName) > 128 {
		return ToolPreparation{}, ErrValidation
	}
	if !toolInDefinitions(input.ToolName, g.tools.ModelTools(request)) {
		return ToolPreparation{}, fmt.Errorf("%w: %s is unavailable for module %s", ErrToolDenied, input.ToolName, run.Module)
	}
	if input.Arguments == nil {
		input.Arguments = map[string]any{}
	}
	// A LangGraph action is the idempotency boundary for an independent
	// persisted task. Replaying the same action reuses its Skill Run, while a
	// second attachment action receives its own queue lease and retry state.
	request.JobID = input.CallKey
	result, err := g.tools.ExecuteModelTool(ctx, request, conversation.ModelToolCall{
		Name: input.ToolName, Arguments: input.Arguments,
	})
	if err != nil {
		return ToolPreparation{}, err
	}
	if result.Confirmation == nil {
		if skillRun, ok := skillRunAwaitingConfirmation(result.Data); ok {
			return g.prepareConfirmation(run, input, result, conversation.ToolConfirmation{
				Kind: "skill_run", CandidateID: skillRun.ID,
				Summary: fmt.Sprintf("工具：%s\n风险：%s\n操作：%s", skillRun.SkillName, skillRun.RiskLevel, result.Response),
			}, skillRun.RiskLevel)
		}
		response := strings.TrimSpace(result.Response)
		if response == "" {
			response = "当前请求无需调用项目工具。"
		}
		return ToolPreparation{
			Status: "completed", ToolName: input.ToolName, Response: response,
			RiskLevel: "none", NormalizedArguments: cloneMap(input.Arguments), Data: publicToolData(result.Data),
		}, nil
	}
	return g.prepareConfirmation(run, input, result, *result.Confirmation, "medium")
}

func (g *ToolGateway) Definitions(ctx context.Context, runID string, expectedRevision int) ([]conversation.ModelToolDefinition, error) {
	_, request, err := g.loadRequest(ctx, runID, expectedRevision)
	if err != nil {
		return nil, err
	}
	definitions := g.tools.ModelTools(request)
	result := make([]conversation.ModelToolDefinition, len(definitions))
	copy(result, definitions)
	return result, nil
}

func (g *ToolGateway) Commit(ctx context.Context, runID string, input ToolCommitInput) (ToolOutcome, error) {
	run, _, err := g.loadRequest(ctx, runID, input.ExpectedRevision)
	if err != nil {
		return ToolOutcome{}, err
	}
	input.CallKey = strings.TrimSpace(input.CallKey)
	input.ConfirmationToken = strings.TrimSpace(input.ConfirmationToken)
	if input.CallKey == "" || len(input.CallKey) > 128 || input.ConfirmationToken == "" {
		return ToolOutcome{}, ErrValidation
	}
	if !input.Approved {
		return ToolOutcome{}, ErrConfirmationState
	}
	claims, err := g.verifyToken(input.ConfirmationToken)
	if err != nil {
		return ToolOutcome{}, err
	}
	if claims.RunID != run.ID || claims.UserID != run.UserID {
		return ToolOutcome{}, ErrInvalidToken
	}
	key := "agent:" + run.ID + ":" + input.CallKey
	switch claims.Kind {
	case "ledger":
		item, _, confirmErr := g.ledger.Confirm(ctx, run.UserID, claims.CandidateID, key, "由 Agent 确认写入")
		if confirmErr != nil {
			return ToolOutcome{}, confirmErr
		}
		return ToolOutcome{Response: "已写入生活账本。", Data: item}, nil
	case "reminder":
		item, _, confirmErr := g.planner.ConfirmReminder(ctx, run.UserID, claims.CandidateID, key)
		if confirmErr != nil {
			return ToolOutcome{}, confirmErr
		}
		return ToolOutcome{Response: "提醒已创建。", Data: item}, nil
	case "today_plan":
		item, confirmErr := g.planner.AddTodayItem(
			ctx, run.UserID, claims.Payload["local_date"], claims.Payload["timezone"],
			claims.Payload["title"], claims.Payload["source"],
		)
		if confirmErr != nil {
			return ToolOutcome{}, confirmErr
		}
		return ToolOutcome{Response: "已加入今日计划。", Data: item}, nil
	case "reminder_reschedule":
		expected, parseErr := parseOptionalTimestamp(claims.Payload["expected_updated_at"])
		if parseErr != nil {
			return ToolOutcome{}, ErrInvalidToken
		}
		item, confirmErr := g.planner.RescheduleReminder(
			ctx, run.UserID, claims.Payload["reminder_id"], claims.Payload["local_due"],
			claims.Payload["timezone"], expected,
		)
		if confirmErr != nil {
			return ToolOutcome{}, confirmErr
		}
		return ToolOutcome{Response: "提醒时间已更新。", Data: item}, nil
	case "today_plan_schedule":
		startsAt, parseErr := time.Parse(time.RFC3339Nano, claims.Payload["starts_at"])
		if parseErr != nil {
			return ToolOutcome{}, ErrInvalidToken
		}
		expected, parseErr := parseOptionalTimestamp(claims.Payload["expected_updated_at"])
		if parseErr != nil {
			return ToolOutcome{}, ErrInvalidToken
		}
		item, confirmErr := g.planner.SchedulePlanItem(
			ctx, run.UserID, claims.Payload["item_id"], startsAt, expected,
		)
		if confirmErr != nil {
			return ToolOutcome{}, confirmErr
		}
		return ToolOutcome{Response: "今日计划时间已更新。", Data: item}, nil
	case "task_complete":
		switch claims.Payload["task_type"] {
		case "today_plan":
			if confirmErr := g.planner.CompletePlanItem(ctx, run.UserID, claims.Payload["item_id"]); confirmErr != nil {
				return ToolOutcome{}, confirmErr
			}
			return ToolOutcome{Response: "今日计划事项已完成。"}, nil
		case "reminder":
			if confirmErr := g.planner.CompleteReminder(ctx, run.UserID, claims.Payload["item_id"]); confirmErr != nil {
				return ToolOutcome{}, confirmErr
			}
			return ToolOutcome{Response: "提醒事项已完成。"}, nil
		default:
			return ToolOutcome{}, ErrInvalidToken
		}
	case "skill_run":
		item, confirmErr := g.skills.Confirm(ctx, run.UserID, claims.CandidateID, key)
		if confirmErr != nil {
			return ToolOutcome{}, confirmErr
		}
		return skillRunOutcome(item, true), nil
	default:
		return ToolOutcome{}, ErrInvalidToken
	}
}

func (g *ToolGateway) ObserveTask(ctx context.Context, runID, taskID string, expectedRevision int) (ToolOutcome, error) {
	run, _, err := g.loadRequest(ctx, runID, expectedRevision)
	if err != nil {
		return ToolOutcome{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 128 || g.skills == nil {
		return ToolOutcome{}, ErrValidation
	}
	item, err := g.skills.Get(ctx, run.UserID, taskID)
	if err != nil {
		return ToolOutcome{}, err
	}
	return skillRunOutcome(item, false), nil
}

func (g *ToolGateway) RetryTask(ctx context.Context, runID, taskID string, input ToolRetryInput) (ToolOutcome, error) {
	run, request, err := g.loadRequest(ctx, runID, input.ExpectedRevision)
	if err != nil {
		return ToolOutcome{}, err
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || len(taskID) > 128 || g.skills == nil || input.Arguments == nil {
		return ToolOutcome{}, ErrValidation
	}
	current, err := g.skills.Get(ctx, run.UserID, taskID)
	if err != nil {
		return ToolOutcome{}, err
	}
	if current.ConversationID != run.ConversationID || current.OriginMessageID != request.MessageID {
		return ToolOutcome{}, ErrToolDenied
	}
	item, err := g.skills.RepairRetry(
		ctx, run.UserID, taskID, input.CallKey, input.OperatorID, input.Arguments,
	)
	if err != nil {
		return ToolOutcome{}, err
	}
	return skillRunOutcome(item, true), nil
}

func (g *ToolGateway) loadRequest(ctx context.Context, runID string, expectedRevision int) (Run, conversation.ToolRequest, error) {
	if g == nil || g.runs == nil || g.tools == nil || len(g.secret) < 16 {
		return Run{}, conversation.ToolRequest{}, ErrValidation
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Run{}, conversation.ToolRequest{}, ErrValidation
	}
	run, err := g.runs.Get(ctx, runID)
	if err != nil {
		return Run{}, conversation.ToolRequest{}, err
	}
	now := g.now().UTC()
	if run.Status != "running" || expectedRevision <= 0 || run.Revision != expectedRevision ||
		run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) ||
		run.DeadlineAt.IsZero() || !run.DeadlineAt.After(now) {
		return Run{}, conversation.ToolRequest{}, ErrConflict
	}
	var input agentRunInput
	if err = json.Unmarshal(run.Input, &input); err != nil {
		return Run{}, conversation.ToolRequest{}, fmt.Errorf("%w: malformed agent run input", ErrValidation)
	}
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.Text = strings.TrimSpace(input.Text)
	if input.MessageID == "" || input.Text == "" {
		return Run{}, conversation.ToolRequest{}, fmt.Errorf("%w: message_id and text are required", ErrValidation)
	}
	history := make([]conversation.Message, 0, len(input.Context.History))
	for _, message := range input.Context.History {
		message.Content = strings.TrimSpace(message.Content)
		if (message.Role == "user" || message.Role == "assistant") && message.Content != "" {
			history = append(history, conversation.Message{Role: message.Role, Content: message.Content})
		}
	}
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	return run, conversation.ToolRequest{
		UserID: run.UserID, MessageID: input.MessageID, JobID: run.ID,
		ConversationID: run.ConversationID, CharacterID: run.CharacterID,
		Module: run.Module, Text: input.Text, History: history,
	}, nil
}

func (g *ToolGateway) prepareConfirmation(
	run Run,
	input ToolPrepareInput,
	result conversation.ToolResult,
	confirmation conversation.ToolConfirmation,
	riskLevel string,
) (ToolPreparation, error) {
	confirmation.Summary = chatattachment.VisibleText(confirmation.Summary)
	if confirmation.Kind == "" || confirmation.CandidateID == "" || confirmation.Summary == "" {
		return ToolPreparation{}, ErrConfirmationState
	}
	if riskLevel != "low" && riskLevel != "medium" && riskLevel != "high" {
		riskLevel = "medium"
	}
	claims := confirmationClaims{
		Version: 1, RunID: run.ID, UserID: run.UserID, ToolName: input.ToolName,
		PrepareKey: input.CallKey, Kind: confirmation.Kind, CandidateID: confirmation.CandidateID,
		Summary: confirmation.Summary, Payload: confirmation.Payload,
		ExpiresAt: g.now().UTC().Add(g.tokenTTL).Unix(),
	}
	token, err := g.signToken(claims)
	if err != nil {
		return ToolPreparation{}, err
	}
	normalized := map[string]any{
		"kind": confirmation.Kind, "candidate_id": confirmation.CandidateID,
		"arguments": cloneMap(input.Arguments),
	}
	if len(confirmation.Payload) > 0 {
		normalized["payload"] = confirmation.Payload
	}
	return ToolPreparation{
		Status: "requires_confirmation", ToolName: input.ToolName, Response: result.Response,
		RiskLevel: riskLevel, Summary: confirmation.Summary, ConfirmationToken: token,
		NormalizedArguments: normalized, Data: publicToolData(result.Data),
	}, nil
}

func (g *ToolGateway) signToken(claims confirmationClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, g.secret)
	_, _ = mac.Write([]byte(encodedPayload))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encodedPayload + "." + signature, nil
}

func (g *ToolGateway) verifyToken(token string) (confirmationClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return confirmationClaims{}, ErrInvalidToken
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[1] {
		return confirmationClaims{}, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, g.secret)
	_, _ = mac.Write([]byte(parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return confirmationClaims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] {
		return confirmationClaims{}, ErrInvalidToken
	}
	var claims confirmationClaims
	if err = json.Unmarshal(payload, &claims); err != nil ||
		claims.Version != 1 || claims.RunID == "" || claims.UserID == "" ||
		claims.ToolName == "" || claims.Kind == "" || claims.CandidateID == "" ||
		claims.ExpiresAt <= g.now().UTC().Unix() {
		return confirmationClaims{}, ErrInvalidToken
	}
	return claims, nil
}

func toolInDefinitions(name string, definitions []conversation.ModelToolDefinition) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func skillRunAwaitingConfirmation(value any) (skill.Run, bool) {
	switch item := value.(type) {
	case skill.Run:
		return item, item.Status == "waiting_confirmation" && item.RequiresConfirmation
	case *skill.Run:
		if item == nil {
			return skill.Run{}, false
		}
		return *item, item.Status == "waiting_confirmation" && item.RequiresConfirmation
	default:
		return skill.Run{}, false
	}
}

func publicToolData(value any) any {
	switch item := value.(type) {
	case skill.Run:
		return publicSkillRun(item)
	case *skill.Run:
		if item == nil {
			return nil
		}
		return publicSkillRun(*item)
	default:
		return value
	}
}

func publicSkillRun(item skill.Run) map[string]any {
	result := map[string]any{
		"kind":       "skill_run",
		"id":         item.ID,
		"skill_name": item.SkillName,
		"status":     item.Status,
		"attempt":    item.Attempt,
		"error_code": item.ErrorCode,
	}
	if strings.TrimSpace(item.ErrorMessage) != "" {
		result["error_message"] = strings.TrimSpace(item.ErrorMessage)
	}
	if failure, ok := skillFailure(item.Output); ok {
		result["failure"] = failure
	}
	if len(item.Output) > 0 {
		var output any
		if json.Unmarshal(item.Output, &output) == nil {
			result["output"] = output
		}
	}
	if len(item.Files) > 0 {
		files := make([]map[string]any, 0, len(item.Files))
		for _, file := range item.Files {
			files = append(files, map[string]any{
				"id": file.ID, "name": file.Name, "media_type": file.MediaType, "size_bytes": file.SizeBytes,
			})
		}
		result["files"] = files
	}
	return result
}

func skillFailure(output json.RawMessage) (map[string]any, bool) {
	if len(output) == 0 {
		return nil, false
	}
	var envelope struct {
		Failure map[string]any `json:"failure"`
	}
	if json.Unmarshal(output, &envelope) != nil || len(envelope.Failure) == 0 {
		return nil, false
	}
	if code, _ := envelope.Failure["code"].(string); strings.TrimSpace(code) == "" {
		return nil, false
	}
	return envelope.Failure, true
}

func skillRunOutcome(item skill.Run, confirmed bool) ToolOutcome {
	response := "工作任务正在执行。"
	switch item.Status {
	case "waiting_confirmation":
		response = "工作任务等待确认。"
	case "queued":
		response = "工作任务已进入执行队列。"
		if confirmed {
			response = "工作任务已确认并进入执行队列。"
		}
	case "running":
		response = "工作任务正在执行。"
		if confirmed {
			response = "工作任务已确认并开始执行。"
		}
	case "succeeded":
		lines := []string{"工作任务已执行完成。"}
		for _, file := range item.Files {
			lines = append(lines, chatattachment.GeneratedFileMarker(item.ID, file.ID, file.Name))
		}
		response = strings.Join(lines, "\n")
	case "failed", "cancelled":
		response = "工作任务执行失败，可以直接在聊天窗口或历史任务中重试。"
		if strings.TrimSpace(item.ErrorMessage) != "" {
			response = "工作任务执行失败：" + strings.TrimSpace(item.ErrorMessage)
		}
	}
	response = strings.TrimSpace(response + "\n" + chatattachment.SkillRunMarker(item.ID, item.Attempt, item.Status))
	return ToolOutcome{Response: response, Data: publicSkillRun(item)}
}

func parseOptionalTimestamp(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
