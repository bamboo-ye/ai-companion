package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/productknowledge"
	"github.com/windcry1/ai-companion/internal/reliability"
	"github.com/windcry1/ai-companion/internal/safety"
)

var (
	ErrNotFound      = errors.New("conversation resource not found")
	ErrConflict      = errors.New("conversation state conflict")
	ErrValidation    = errors.New("conversation validation failed")
	ErrNoRunnableJob = errors.New("no runnable generation job")
)

type Conversation struct {
	ID            string     `json:"id"`
	UserID        string     `json:"-"`
	CharacterID   string     `json:"character_id"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	NextSequence  uint64     `json:"-"`
	LastMessageAt *time.Time `json:"last_message_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}
type Message struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	UserID         string     `json:"-"`
	Role           string     `json:"role"`
	Sequence       uint64     `json:"sequence"`
	Bubble         int        `json:"bubble"`
	Content        string     `json:"content"`
	Status         string     `json:"status"`
	ReplyToID      string     `json:"reply_to_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}
type Job struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	UserMessageID  string     `json:"user_message_id"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	Provider       string     `json:"provider,omitempty"`
	Model          string     `json:"model,omitempty"`
	DeadlineAt     time.Time  `json:"deadline_at"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}
type Event struct {
	ID        uint64    `json:"id"`
	JobID     string    `json:"job_id"`
	Type      string    `json:"type"`
	Data      any       `json:"data"`
	CreatedAt time.Time `json:"created_at"`
}
type Usage struct {
	Provider            string
	Model               string
	InputTokens         int
	OutputTokens        int
	EstimatedCostMicros int64
	Latency             time.Duration
}

type Store interface {
	CreateConversation(context.Context, Conversation) error
	ListConversations(context.Context, string) ([]Conversation, error)
	GetConversation(context.Context, string, string) (Conversation, error)
	DeleteConversation(context.Context, string, string, time.Time) error
	AcceptMessage(context.Context, string, string, Message, Job) (Message, Job, error)
	ListMessages(context.Context, string, string, uint64, int, int) ([]Message, error)
	GetJob(context.Context, string, string) (Job, error)
	MarkJobRunning(context.Context, string, string, time.Time) error
	AppendAssistantBubble(context.Context, string, string, int, string, time.Time) (Message, error)
	FinishJob(context.Context, string, string, string, string, string, Usage, time.Time) error
	RequestCancel(context.Context, string, string, time.Time) error
	CreateRetry(context.Context, string, string, Job) (Job, error)
	AppendEvent(context.Context, string, string, any, time.Time) (Event, error)
	ListEvents(context.Context, string, string, uint64, int) ([]Event, error)
	RecoverInterrupted(context.Context, time.Time) error
}

type recentMessageStore interface {
	ListRecentMessages(context.Context, string, string, int) ([]Message, error)
}

type Provider interface {
	Generate(context.Context, character.Character, []Message) (string, Usage, error)
}

type ModelToolDefinition struct {
	Name             string              `json:"name"`
	Description      string              `json:"description"`
	Parameters       map[string]any      `json:"parameters"`
	RequiresPlan     bool                `json:"requires_plan,omitempty"`
	Repeatable       bool                `json:"repeatable,omitempty"`
	IdentityFields   []string            `json:"identity_fields,omitempty"`
	ComposeArguments bool                `json:"compose_arguments,omitempty"`
	RepairPolicies   []ModelRepairPolicy `json:"repair_policies,omitempty"`
}

type ModelRepairPolicy struct {
	OperatorID          string `json:"operator_id"`
	FieldPath           string `json:"field_path"`
	Extension           string `json:"extension,omitempty"`
	SourceField         string `json:"source_field,omitempty"`
	SemanticsPreserving bool   `json:"semantics_preserving"`
	Preflight           bool   `json:"preflight"`
}

type ModelToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type ModelToolTurn struct {
	Text string
	Call *ModelToolCall
}

type ToolCallingProvider interface {
	GenerateWithTools(context.Context, character.Character, []Message, []ModelToolDefinition) (ModelToolTurn, Usage, error)
}

type PolicySource interface {
	Policy() reliability.Policy
}

type DurableJobStore interface {
	ClaimGenerationJobByID(context.Context, string, string, time.Time, time.Duration, time.Duration) (Job, string, error)
	ClaimGenerationJob(context.Context, string, time.Time, time.Duration, time.Duration) (Job, string, error)
}

type DeferrableJobStore interface {
	DeferGenerationJob(context.Context, string, string, string, string, time.Time, time.Time) error
}
type MemoryContext interface {
	Recall(context.Context, string, string, int) ([]string, error)
}

type ToolRequest struct {
	UserID         string
	JobID          string
	MessageID      string
	ConversationID string
	CharacterID    string
	Module         string
	Text           string
	History        []Message
}

type ToolResult struct {
	Handled       bool
	ReferenceOnly bool // Read-only evidence must be composed into an answer.
	ToolName      string
	Response      string
	Data          any
	Confirmation  *ToolConfirmation
}

type ToolConfirmation struct {
	Kind        string            `json:"kind"`
	CandidateID string            `json:"candidate_id"`
	Summary     string            `json:"summary"`
	Payload     map[string]string `json:"payload,omitempty"`
}

type ToolExecutor interface {
	Execute(context.Context, ToolRequest) (ToolResult, error)
}

type ModelToolExecutor interface {
	ModelTools(ToolRequest) []ModelToolDefinition
	ExecuteModelTool(context.Context, ToolRequest, ModelToolCall) (ToolResult, error)
}

type ToolContextProvider interface {
	Context(context.Context, ToolRequest) (string, error)
}

type noMemoryContext struct{}

func (noMemoryContext) Recall(context.Context, string, string, int) ([]string, error) {
	return nil, nil
}

type Service struct {
	store                       Store
	characters                  *character.Service
	provider                    Provider
	timeout, timeBetweenBubbles time.Duration
	mu                          sync.Mutex
	active                      map[string]context.CancelFunc
	now                         func() time.Time
	safety                      safety.Policy
	memories                    MemoryContext
	contextBuilder              *ContextBuilder
	asyncDispatch               bool
	policySource                PolicySource
	localProvider               Provider
	tools                       ToolExecutor
}

func NewService(store Store, characters *character.Service, provider Provider) *Service {
	return &Service{store: store, characters: characters, provider: provider, timeout: 30 * time.Second, timeBetweenBubbles: 80 * time.Millisecond, active: map[string]context.CancelFunc{}, now: time.Now, safety: safety.Policy{}, memories: noMemoryContext{}, contextBuilder: NewContextBuilder(store, DefaultRecentTokenBudget, DefaultSummaryTokenBudget), policySource: staticPolicySource{policy: reliability.Policy{UseFullRAG: true, ExtractMemory: true, PreferredModelClass: "primary", LongSkillsQueued: true}}, localProvider: DevelopmentProvider{}}
}
func (s *Service) SetMemoryContext(memories MemoryContext) {
	if memories != nil {
		s.memories = memories
	}
}

func (s *Service) SetContextBudgets(recentTokens, summaryTokens int) {
	s.contextBuilder = NewContextBuilder(s.store, recentTokens, summaryTokens)
}

func (s *Service) SetAsyncDispatch(enabled bool)         { s.asyncDispatch = enabled }
func (s *Service) SetToolExecutor(executor ToolExecutor) { s.tools = executor }
func (s *Service) SetTimeout(timeout time.Duration) {
	if timeout > 0 {
		s.timeout = timeout
	}
}
func (s *Service) SetPolicySource(source PolicySource) {
	if source != nil {
		s.policySource = source
	}
}

func (s *Service) Create(ctx context.Context, userID, characterID string) (Conversation, error) {
	if _, err := s.characters.Get(ctx, userID, characterID); err != nil {
		return Conversation{}, ErrNotFound
	}
	conversationID, err := id.New()
	if err != nil {
		return Conversation{}, err
	}
	now := s.now().UTC()
	item := Conversation{ID: conversationID, UserID: userID, CharacterID: characterID, Status: "active", CreatedAt: now, UpdatedAt: now, NextSequence: 1}
	if err = s.store.CreateConversation(ctx, item); err != nil {
		return Conversation{}, err
	}
	return item, nil
}
func (s *Service) List(ctx context.Context, userID string) ([]Conversation, error) {
	return s.store.ListConversations(ctx, userID)
}
func (s *Service) Get(ctx context.Context, userID, conversationID string) (Conversation, error) {
	return s.store.GetConversation(ctx, userID, conversationID)
}
func (s *Service) Delete(ctx context.Context, userID, conversationID string) error {
	return s.store.DeleteConversation(ctx, userID, conversationID, s.now().UTC())
}
func (s *Service) Messages(ctx context.Context, userID, conversationID string, after uint64, afterBubble, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.store.ListMessages(ctx, userID, conversationID, after, afterBubble, limit)
}

// RecentMessages returns the newest messages in chronological order. It is
// distinct from cursor pagination, which intentionally starts at the oldest
// matching row and must not be used to build the current Agent context.
func (s *Service) RecentMessages(ctx context.Context, userID, conversationID string, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if store, ok := s.store.(recentMessageStore); ok {
		return store.ListRecentMessages(ctx, userID, conversationID, limit)
	}

	recent := make([]Message, 0, limit)
	var after uint64
	var afterBubble int
	for {
		page, err := s.store.ListMessages(ctx, userID, conversationID, after, afterBubble, 200)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		recent = append(recent, page...)
		if len(recent) > limit {
			recent = recent[len(recent)-limit:]
		}
		last := page[len(page)-1]
		after, afterBubble = last.Sequence, last.Bubble
		if len(page) < 200 {
			break
		}
	}
	return recent, nil
}
func (s *Service) Job(ctx context.Context, userID, jobID string) (Job, error) {
	return s.store.GetJob(ctx, userID, jobID)
}
func (s *Service) Events(ctx context.Context, userID, jobID string, after uint64) ([]Event, error) {
	return s.store.ListEvents(ctx, userID, jobID, after, 100)
}

func (s *Service) Send(ctx context.Context, userID, conversationID, content string) (Message, Job, error) {
	if err := s.ValidateContent(content); err != nil {
		return Message{}, Job{}, err
	}
	content = strings.TrimSpace(content)
	messageID, err := id.New()
	if err != nil {
		return Message{}, Job{}, err
	}
	jobID, err := id.New()
	if err != nil {
		return Message{}, Job{}, err
	}
	now := s.now().UTC()
	message := Message{ID: messageID, ConversationID: conversationID, UserID: userID, Role: "user", Bubble: 1, Content: content, Status: "completed", CreatedAt: now, CompletedAt: &now}
	job := Job{ID: jobID, ConversationID: conversationID, UserMessageID: messageID, Status: "accepted", Attempt: 1, DeadlineAt: now.Add(s.timeout), CreatedAt: now}
	message, job, err = s.store.AcceptMessage(ctx, userID, conversationID, message, job)
	if err != nil {
		return Message{}, Job{}, err
	}
	_, _ = s.store.AppendEvent(ctx, job.ID, "accepted", map[string]any{"message": message, "job": job}, now)
	policy := s.policy()
	if policy.AcceptOnly {
		_, _ = s.store.AppendEvent(ctx, job.ID, "degraded_accept_only", map[string]any{"policy": policy}, now)
		return message, job, nil
	}
	if !s.asyncDispatch {
		s.start(userID, job.ID)
	}
	return message, job, nil
}

func (s *Service) ValidateContent(content string) error {
	content = strings.TrimSpace(content)
	if content == "" || len([]rune(content)) > 8000 {
		return fmt.Errorf("%w: content must contain 1-8000 characters", ErrValidation)
	}
	if err := s.safety.CheckInput(content); err != nil {
		return fmt.Errorf("%w: content rejected by safety policy", ErrValidation)
	}
	return nil
}

func (s *Service) Cancel(ctx context.Context, userID, jobID string) error {
	now := s.now().UTC()
	if err := s.store.RequestCancel(ctx, userID, jobID, now); err != nil {
		return err
	}
	s.mu.Lock()
	cancel := s.active[jobID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	status := "cancel_requested"
	if job, err := s.store.GetJob(ctx, userID, jobID); err == nil {
		status = job.Status
	}
	if status == "cancelled" {
		_, _ = s.store.AppendAssistantBubble(
			context.Background(), userID, jobID, 1,
			"这次操作已取消，原对话已保留。需要时可以点击“重试”。\n"+generationJobMarker(jobID, status), now,
		)
	}
	_, _ = s.store.AppendEvent(ctx, jobID, status, map[string]string{"status": status}, now)
	return nil
}
func (s *Service) Retry(ctx context.Context, userID, jobID string) (Job, error) {
	newID, err := id.New()
	if err != nil {
		return Job{}, err
	}
	now := s.now().UTC()
	job, err := s.store.CreateRetry(ctx, userID, jobID, Job{ID: newID, Status: "accepted", DeadlineAt: now.Add(s.timeout), CreatedAt: now})
	if err != nil {
		return Job{}, err
	}
	_, _ = s.store.AppendEvent(ctx, job.ID, "accepted", map[string]any{"job": job}, now)
	if !s.asyncDispatch {
		s.start(userID, job.ID)
	}
	return job, nil
}

func (s *Service) RunJob(ctx context.Context, jobID, workerID string, lease time.Duration) (bool, error) {
	store, ok := s.store.(DurableJobStore)
	if !ok {
		return false, ErrConflict
	}
	if s.policy().AcceptOnly {
		_, _ = s.store.AppendEvent(ctx, jobID, "degraded_accept_only", map[string]string{"status": "accepted"}, s.now().UTC())
		return false, nil
	}
	job, userID, err := store.ClaimGenerationJobByID(ctx, jobID, workerID, s.now().UTC(), lease, s.timeout)
	if errors.Is(err, ErrNoRunnableJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _ = s.store.AppendEvent(ctx, jobID, "running", map[string]string{"status": "running"}, s.now().UTC())
	s.runClaimed(ctx, userID, job)
	return true, nil
}

func (s *Service) RunNextJob(ctx context.Context, workerID string, lease time.Duration) (bool, error) {
	store, ok := s.store.(DurableJobStore)
	if !ok {
		return false, ErrConflict
	}
	if s.policy().AcceptOnly {
		return false, nil
	}
	job, userID, err := store.ClaimGenerationJob(ctx, workerID, s.now().UTC(), lease, s.timeout)
	if errors.Is(err, ErrNoRunnableJob) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _ = s.store.AppendEvent(ctx, job.ID, "running", map[string]string{"status": "running"}, s.now().UTC())
	s.runClaimed(ctx, userID, job)
	return true, nil
}

func (s *Service) RunReconciler(ctx context.Context, workerID string, lease, interval time.Duration) error {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, _ := s.RunNextJob(ctx, workerID, lease)
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) start(userID, jobID string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.active[jobID] = cancel
	s.mu.Unlock()
	go func() {
		defer func() { cancel(); s.mu.Lock(); delete(s.active, jobID); s.mu.Unlock() }()
		s.run(ctx, userID, jobID)
	}()
}
func (s *Service) run(parent context.Context, userID, jobID string) {
	job, err := s.store.GetJob(parent, userID, jobID)
	if err != nil {
		return
	}
	started := s.now().UTC()
	if err = s.store.MarkJobRunning(parent, userID, jobID, started); err != nil {
		if parent.Err() != nil {
			s.fail(userID, jobID, "cancelled", "cancelled", parent.Err())
		}
		return
	}
	_, _ = s.store.AppendEvent(parent, jobID, "running", map[string]string{"status": "running"}, started)
	s.runClaimed(parent, userID, job)
}

func (s *Service) runClaimed(parent context.Context, userID string, job Job) {
	jobID := job.ID
	policy := s.policy()
	if policy.AcceptOnly {
		s.deferJob(userID, jobID, "degraded_accept_only", "generation deferred by L3 accept-only policy", s.now().UTC().Add(30*time.Second))
		return
	}
	ctx, cancel := context.WithDeadline(parent, job.DeadlineAt)
	defer cancel()
	conv, err := s.store.GetConversation(ctx, userID, job.ConversationID)
	if err != nil {
		s.fail(userID, jobID, "failed", "conversation_unavailable", err)
		return
	}
	persona, err := s.characters.Get(ctx, userID, conv.CharacterID)
	if err != nil {
		s.fail(userID, jobID, "failed", "character_unavailable", err)
		return
	}
	contextResult, err := s.contextBuilder.Build(ctx, userID, conv.ID)
	if err != nil {
		s.fail(userID, jobID, "failed", "history_unavailable", err)
		return
	}
	history := contextResult.Messages
	if contextResult.SummariesAdded > 0 {
		_, _ = s.store.AppendEvent(ctx, jobID, "context_summarized", map[string]any{
			"summaries_added": contextResult.SummariesAdded,
			"summary_tokens":  contextResult.SummaryTokens,
			"recent_tokens":   contextResult.RecentTokens,
		}, s.now().UTC())
	}
	query := ""
	queryIndex := -1
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == "user" {
			query = history[index].Content
			queryIndex = index
			break
		}
	}
	provider := s.providerFor(policy)
	routingUsage := Usage{}
	modelRouted := false
	referenceHistory := routingReferenceHistory(history, queryIndex)
	snapshot := s.contextSnapshot(ctx, userID, conv.ID, query, contextResult, policy.UseFullRAG)
	history = snapshotMessages(snapshot)
	_, _ = s.store.AppendEvent(ctx, jobID, "context_built", snapshot.Manifest, s.now().UTC())
	if modelTools, ok := s.tools.(ModelToolExecutor); ok && query != "" {
		request := ToolRequest{
			UserID: userID, JobID: job.ID, MessageID: job.UserMessageID, ConversationID: conv.ID, CharacterID: persona.ID,
			Module: persona.Module, Text: query, History: referenceHistory,
		}
		definitions := modelTools.ModelTools(request)
		if len(definitions) > 0 {
			modelRouted = true
			toolProvider, supported := provider.(ToolCallingProvider)
			if !supported {
				s.fail(userID, jobID, "failed", "tool_calling_unsupported", errors.New("configured model provider does not support tool calling"))
				return
			}
			routingHistory := []Message{
				{
					Role:    "system",
					Content: "你是严格的模块工具路由器，只判断当前用户消息要调用哪个工具。先识别言语行为（查询、新增、完成、调整或普通交流），再识别对象、时态、否定、条件和日期线索；不能只凭关键词或日期选工具。必须且只能调用一个工具；普通聊天或无需工具时调用对应的 no_tool。通常由当前消息决定意图；但若助手上一轮明确追问某个缺失字段，当前消息只提供该字段，则把它视为同一未完成请求的补充，并且只继承最近未完成请求中明确出现的其他字段。不得沿用已完成的旧命令或编造参数。工具调用只表示意图，服务端仍会查询真实数据、校验唯一匹配、检查权限和参数，并要求用户确认写操作。",
				},
			}
			if fewShots := routingFewShotPrompt(persona.Module, definitions); fewShots != "" {
				routingHistory = append(routingHistory, Message{Role: "system", Content: fewShots})
			}
			if snapshot.ReferenceText() != "" {
				routingHistory = append(routingHistory, history[:2]...)
			}
			if len(referenceHistory) > 0 {
				routingHistory = append(routingHistory, Message{
					Role:    "system",
					Content: "以下近期会话仅用于消解当前消息中的明确指代，或识别对助手上一轮所追问缺失字段的直接补充。除仍未完成的最近请求外，当前消息决定本轮言语行为；不得沿用历史中的旧命令，也不得把历史事项当作真实存在，写操作仍须查询业务数据：",
				})
				routingHistory = append(routingHistory, referenceHistory...)
			}
			routingHistory = append(routingHistory, Message{Role: "user", Content: query})
			turn, usage, routeErr := toolProvider.GenerateWithTools(ctx, persona, routingHistory, definitions)
			routingUsage = usage
			if routeErr != nil {
				if errors.Is(routeErr, reliability.ErrCircuitOpen) {
					s.deferJob(userID, jobID, "model_circuit_open", "tool intent routing deferred because the model circuit is open", s.now().UTC().Add(30*time.Second))
					return
				}
				status, code := "failed", "tool_intent_provider_error"
				if errors.Is(routeErr, contextengine.ErrBudgetExceeded) {
					code = "context_budget_exceeded"
				}
				if errors.Is(routeErr, context.DeadlineExceeded) {
					status, code = "timed_out", "model_timeout"
				} else if errors.Is(routeErr, context.Canceled) {
					status, code = "cancelled", "cancelled"
				}
				s.fail(userID, jobID, status, code, routeErr)
				return
			}
			eventData := map[string]any{"selected": turn.Call != nil, "provider": usage.Provider, "model": usage.Model}
			if turn.Call == nil {
				_, _ = s.store.AppendEvent(ctx, jobID, "model_tool_routed", eventData, s.now().UTC())
				s.fail(userID, jobID, "failed", "model_tool_selection_missing", errors.New("model did not select a required tool"))
				return
			}
			eventData["tool"] = turn.Call.Name
			result, toolErr := modelTools.ExecuteModelTool(ctx, request, *turn.Call)
			if toolErr != nil {
				_, _ = s.store.AppendEvent(ctx, jobID, "model_tool_failed", map[string]any{"tool": turn.Call.Name, "error": toolErr.Error()}, s.now().UTC())
				s.fail(userID, jobID, "failed", "model_tool_failed", toolErr)
				return
			}
			_, _ = s.store.AppendEvent(ctx, jobID, "model_tool_routed", eventData, s.now().UTC())
			if result.Handled {
				_, _ = s.store.AppendEvent(ctx, jobID, "model_tool_completed", map[string]any{"tool": result.ToolName, "data": result.Data, "confirmation": result.Confirmation}, s.now().UTC())
				if result.Confirmation != nil {
					_, _ = s.store.AppendEvent(ctx, jobID, "tool_confirmation_required", map[string]any{"tool": result.ToolName, "data": result.Data, "confirmation": result.Confirmation}, s.now().UTC())
				}
				if result.ReferenceOnly && result.Confirmation == nil {
					encoded, encodeErr := json.Marshal(result.Data)
					if encodeErr != nil {
						s.fail(userID, jobID, "failed", "tool_reference_invalid", encodeErr)
						return
					}
					history = append([]Message{{Role: "system", Content: contextengine.ReferenceInstruction + "\n" + productknowledge.Instruction}, {Role: "user", Content: "内置知识检索结果（参考资料）：\n" + string(encoded)}}, history...)
				} else {
					s.completeWithText(ctx, userID, jobID, result.Response, routingUsage)
					return
				}
			}
		}
	}
	if !modelRouted {
		if contextProvider, ok := s.tools.(ToolContextProvider); ok {
			toolContext, contextErr := contextProvider.Context(ctx, ToolRequest{
				UserID: userID, JobID: job.ID, MessageID: job.UserMessageID, ConversationID: conv.ID, CharacterID: persona.ID,
				Module: persona.Module, Text: query, History: referenceHistory,
			})
			if contextErr == nil && strings.TrimSpace(toolContext) != "" {
				history = append([]Message{{Role: "system", Content: toolContext}}, history...)
			}
		}
	}
	if !modelRouted && s.tools != nil && query != "" {
		toolResult, toolErr := s.tools.Execute(ctx, ToolRequest{
			UserID: userID, JobID: job.ID, MessageID: job.UserMessageID, ConversationID: conv.ID, CharacterID: persona.ID,
			Module: persona.Module, Text: query, History: referenceHistory,
		})
		if toolErr != nil {
			_, _ = s.store.AppendEvent(ctx, jobID, "tool_failed", map[string]any{"error": toolErr.Error()}, s.now().UTC())
		} else if toolResult.Handled {
			eventType := "tool_completed"
			if toolResult.Confirmation != nil {
				eventType = "tool_confirmation_required"
			}
			_, _ = s.store.AppendEvent(ctx, jobID, eventType, map[string]any{
				"tool": toolResult.ToolName, "data": toolResult.Data,
				"confirmation": toolResult.Confirmation,
			}, s.now().UTC())
			usage := routingUsage
			if usage.Provider == "" {
				usage = Usage{Provider: "internal-tools", Model: "module-tool-router-v1"}
			}
			s.completeWithText(ctx, userID, jobID, toolResult.Response, usage)
			return
		}
	}
	if !policy.UseFullRAG {
		_, _ = s.store.AppendEvent(ctx, jobID, "rag_skipped", map[string]string{"reason": "degraded_policy"}, s.now().UTC())
	}
	_, _ = s.store.AppendEvent(ctx, jobID, "model_policy", map[string]string{"preferred_model_class": policy.PreferredModelClass}, s.now().UTC())
	text, usage, err := provider.Generate(ctx, persona, history)
	if err != nil {
		if errors.Is(err, reliability.ErrCircuitOpen) {
			s.deferJob(userID, jobID, "model_circuit_open", "generation deferred because the model circuit is open", s.now().UTC().Add(30*time.Second))
			return
		}
		status, code := "failed", "provider_error"
		if errors.Is(err, contextengine.ErrBudgetExceeded) {
			code = "context_budget_exceeded"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			status, code = "timed_out", "model_timeout"
		} else if errors.Is(err, context.Canceled) {
			status, code = "cancelled", "cancelled"
		}
		s.fail(userID, jobID, status, code, err)
		return
	}
	if s.cancelRequested(ctx, userID, jobID) {
		s.fail(userID, jobID, "cancelled", "cancelled", context.Canceled)
		return
	}
	if err = s.safety.CheckOutput(text); err != nil {
		s.fail(userID, jobID, "failed", "safety_blocked", err)
		return
	}
	usage = mergeUsage(routingUsage, usage)
	for index, bubble := range SplitBubbles(text) {
		if s.cancelRequested(ctx, userID, jobID) {
			s.fail(userID, jobID, "cancelled", "cancelled", context.Canceled)
			return
		}
		select {
		case <-ctx.Done():
			s.fail(userID, jobID, "cancelled", "cancelled", ctx.Err())
			return
		default:
		}
		message, err := s.store.AppendAssistantBubble(ctx, userID, jobID, index+1, bubble, s.now().UTC())
		if err != nil {
			s.fail(userID, jobID, "failed", "persist_failed", err)
			return
		}
		_, _ = s.store.AppendEvent(ctx, jobID, "bubble", message, s.now().UTC())
		if s.timeBetweenBubbles > 0 {
			timer := time.NewTimer(s.timeBetweenBubbles)
			select {
			case <-ctx.Done():
				timer.Stop()
				s.fail(userID, jobID, "cancelled", "cancelled", ctx.Err())
				return
			case <-timer.C:
			}
		}
	}
	completed := s.now().UTC()
	if err = s.store.FinishJob(context.Background(), userID, jobID, "completed", "", "", usage, completed); err != nil {
		return
	}
	status := "completed"
	if current, getErr := s.store.GetJob(context.Background(), userID, jobID); getErr == nil {
		status = current.Status
	}
	_, _ = s.store.AppendEvent(context.Background(), jobID, status, map[string]string{"status": status}, completed)
}

func mergeUsage(first, second Usage) Usage {
	if first.Provider == "" {
		return second
	}
	if second.Provider == "" {
		return first
	}
	return Usage{
		Provider: second.Provider, Model: second.Model,
		InputTokens: first.InputTokens + second.InputTokens, OutputTokens: first.OutputTokens + second.OutputTokens,
		EstimatedCostMicros: first.EstimatedCostMicros + second.EstimatedCostMicros,
		Latency:             first.Latency + second.Latency,
	}
}

func routingFewShotPrompt(module string, definitions []ModelToolDefinition) string {
	if module != "life" {
		return ""
	}
	available := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		available[definition.Name] = true
	}
	examples := make([]string, 0, 12)
	if available["life_prepare_task_completion"] {
		examples = append(examples,
			`用户：“我完成了8月10号的选课” → 完成事实，不是查询；life_prepare_task_completion，参数 {"title":"选课","date_hint":"8月10号"}`,
			`用户：“选课做完了” → life_prepare_task_completion，参数 {"title":"选课"}`,
			`用户：“把选课标记为完成” → life_prepare_task_completion，参数 {"title":"选课"}`,
		)
	}
	if available["life_prepare_task_completion"] && available["life_query_active_reminders"] {
		examples = append(examples,
			`用户：“8月10号有选课提醒吗” → 查询；life_query_active_reminders，参数 {}`,
			`用户：“我还没完成选课” → 否定完成，不能标记完成；life_query_active_reminders，参数 {}`,
		)
	}
	if available["life_prepare_today_plan"] {
		examples = append(examples, `用户：“把选课加入今日计划” → life_prepare_today_plan，参数 {"title":"选课"}`)
	}
	if available["life_prepare_ledger_entry"] {
		examples = append(examples,
			`最近对话：用户“打车花了36元” / 助手追问“请补充发生时间” / 用户：“今天” → 补充未完成账单；life_prepare_ledger_entry，参数 {}`,
		)
	}
	if available["life_prepare_reminder"] {
		examples = append(examples,
			`用户：“提醒我8月10号选课” → 新增未来提醒；life_prepare_reminder，参数 {"title":"选课","date_hint":"8月10号"}`,
			`最近对话：用户“提醒我提交报销材料” / 助手追问“请补充提醒日期” / 用户：“下周五” → 补充未完成提醒的日期；life_prepare_reminder，参数 {"title":"提交报销材料","date_hint":"下周五"}`,
			`最近对话：“25号提醒我选课” / 用户：“那天还要提醒查看邮件” → “那天”指向25号；life_prepare_reminder，参数 {"title":"查看邮件","date_hint":"25号"}`,
		)
	}
	if available["life_prepare_task_completion"] {
		examples = append(examples, `最近对话：助手问“需要我把8月10号选课提醒关掉吗？” / 用户：“需要” → 接受上轮提议；life_prepare_task_completion，参数 {"title":"选课","task_type":"reminder","date_hint":"8月10号"}`)
	}
	if available["life_prepare_schedule_change"] {
		examples = append(examples,
			`用户：“把选课提醒改到明天下午3点” → 调整已有事项；life_prepare_schedule_change，参数 {}`,
			`最近对话：用户“把选课提醒改一下” / 助手追问“请补充新的日期或时间” / 用户：“明天下午3点” → 补充未完成改期；life_prepare_schedule_change，参数 {}`,
		)
	}
	if available["life_no_tool"] {
		examples = append(examples, `用户：“我准备完成选课” → 将来意愿，不代表已经完成；life_no_tool，参数 {}`)
	}
	if len(examples) == 0 {
		return ""
	}
	return "生活助手语义路由对比示例。先区分言语行为，再判断对象；特别检查否定、时态、条件、多轮指代和对上一轮缺失字段的直接补充。只有助手刚刚明确追问且请求仍未完成时，才可继承最近请求中明确出现的日期或事项；不得继承已完成的旧命令。日期只是事项匹配线索，不能据此把完成陈述改判为查询。完成工具只启动‘查询候选→唯一匹配→请求确认’流程，绝不假设事项一定存在：\n" + strings.Join(examples, "\n")
}

func routingReferenceHistory(history []Message, queryIndex int) []Message {
	if queryIndex <= 0 || queryIndex >= len(history) {
		return nil
	}
	start := queryIndex - 8
	if start < 0 {
		start = 0
	}
	references := make([]Message, 0, queryIndex-start)
	for _, message := range history[start:queryIndex] {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		references = append(references, Message{Role: message.Role, Content: content})
	}
	return references
}

func (s *Service) completeWithText(ctx context.Context, userID, jobID, text string, usage Usage) {
	if err := s.safety.CheckOutput(text); err != nil {
		s.fail(userID, jobID, "failed", "safety_blocked", err)
		return
	}
	for index, bubble := range SplitBubbles(text) {
		message, err := s.store.AppendAssistantBubble(ctx, userID, jobID, index+1, bubble, s.now().UTC())
		if err != nil {
			s.fail(userID, jobID, "failed", "persist_failed", err)
			return
		}
		_, _ = s.store.AppendEvent(ctx, jobID, "bubble", message, s.now().UTC())
	}
	completed := s.now().UTC()
	if err := s.store.FinishJob(context.Background(), userID, jobID, "completed", "", "", usage, completed); err != nil {
		return
	}
	_, _ = s.store.AppendEvent(context.Background(), jobID, "completed", map[string]string{"status": "completed"}, completed)
}

func (s *Service) policy() reliability.Policy {
	if s.policySource == nil {
		return reliability.Policy{UseFullRAG: true, ExtractMemory: true, PreferredModelClass: "primary", LongSkillsQueued: true}
	}
	return s.policySource.Policy()
}

func (s *Service) providerFor(policy reliability.Policy) Provider {
	if policy.PreferredModelClass == "local_only" {
		return s.localProvider
	}
	return s.provider
}

func (s *Service) deferJob(userID, jobID, code, message string, availableAt time.Time) {
	now := s.now().UTC()
	if store, ok := s.store.(DeferrableJobStore); ok {
		if err := store.DeferGenerationJob(context.Background(), userID, jobID, code, message, availableAt, now); err != nil {
			_, _ = s.store.AppendEvent(context.Background(), jobID, "defer_failed", map[string]string{"error_code": code}, now)
			return
		}
	}
	_, _ = s.store.AppendEvent(context.Background(), jobID, "deferred", map[string]any{"status": "accepted", "error_code": code, "available_at": availableAt}, now)
}

type staticPolicySource struct{ policy reliability.Policy }

func (s staticPolicySource) Policy() reliability.Policy { return s.policy }

func (s *Service) cancelRequested(ctx context.Context, userID, jobID string) bool {
	job, err := s.store.GetJob(ctx, userID, jobID)
	return err == nil && (job.Status == "cancel_requested" || job.Status == "cancelled")
}
func (s *Service) Recover(ctx context.Context) error {
	if s.asyncDispatch {
		return nil
	}
	return s.store.RecoverInterrupted(ctx, s.now().UTC())
}
func (s *Service) fail(userID, jobID, status, code string, cause error) {
	now := s.now().UTC()
	if err := s.store.FinishJob(context.Background(), userID, jobID, status, code, cause.Error(), Usage{}, now); err != nil {
		return
	}
	if current, err := s.store.GetJob(context.Background(), userID, jobID); err == nil {
		status = current.Status
		if status == "cancelled" {
			code = "cancelled"
		}
	}
	feedback := "这次处理没有完成，原对话已保留。你可以点击“重试”再次执行。"
	if status == "timed_out" {
		feedback = "这次处理超时，原对话已保留。你可以点击“重试”再次执行。"
	} else if status == "cancelled" {
		feedback = "这次操作已取消，原对话已保留。需要时可以点击“重试”。"
	}
	_, _ = s.store.AppendAssistantBubble(
		context.Background(), userID, jobID, 1,
		feedback+"\n"+generationJobMarker(jobID, status), now,
	)
	_, _ = s.store.AppendEvent(context.Background(), jobID, status, map[string]string{"status": status, "error_code": code}, now)
}

func generationJobMarker(jobID, status string) string {
	return "<!--ai-generation-job:" + jobID + "|" + status + "-->"
}
func IsTerminal(status string) bool {
	return status == "completed" || status == "cancelled" || status == "failed" || status == "timed_out"
}
