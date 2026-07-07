package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/platform/id"
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

type Provider interface {
	Generate(context.Context, character.Character, []Message) (string, Usage, error)
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
	Observe(context.Context, string, string, string, string) error
	Recall(context.Context, string, string, int) ([]string, error)
}
type noMemoryContext struct{}

func (noMemoryContext) Observe(context.Context, string, string, string, string) error { return nil }
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

func (s *Service) SetAsyncDispatch(enabled bool) { s.asyncDispatch = enabled }
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
	item := Conversation{ID: conversationID, UserID: userID, CharacterID: characterID, CreatedAt: now, UpdatedAt: now, NextSequence: 1}
	if err = s.store.CreateConversation(ctx, item); err != nil {
		return Conversation{}, err
	}
	return item, nil
}
func (s *Service) List(ctx context.Context, userID string) ([]Conversation, error) {
	return s.store.ListConversations(ctx, userID)
}
func (s *Service) Messages(ctx context.Context, userID, conversationID string, after uint64, afterBubble, limit int) ([]Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.store.ListMessages(ctx, userID, conversationID, after, afterBubble, limit)
}
func (s *Service) Job(ctx context.Context, userID, jobID string) (Job, error) {
	return s.store.GetJob(ctx, userID, jobID)
}
func (s *Service) Events(ctx context.Context, userID, jobID string, after uint64) ([]Event, error) {
	return s.store.ListEvents(ctx, userID, jobID, after, 100)
}

func (s *Service) Send(ctx context.Context, userID, conversationID, content string) (Message, Job, error) {
	content = strings.TrimSpace(content)
	if content == "" || len([]rune(content)) > 8000 {
		return Message{}, Job{}, fmt.Errorf("%w: content must contain 1-8000 characters", ErrValidation)
	}
	if err := s.safety.CheckInput(content); err != nil {
		return Message{}, Job{}, fmt.Errorf("%w: content rejected by safety policy", ErrValidation)
	}
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
		if policy.ExtractMemory {
			if err = s.memories.Observe(ctx, userID, conversationID, message.ID, content); err != nil {
				_, _ = s.store.AppendEvent(ctx, job.ID, "memory_failed", map[string]string{"error": "memory_observation_failed"}, now)
			}
		} else {
			_, _ = s.store.AppendEvent(ctx, job.ID, "memory_skipped", map[string]string{"reason": "degraded_policy"}, now)
		}
		s.start(userID, job.ID)
	}
	return message, job, nil
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
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == "user" {
			query = history[index].Content
			break
		}
	}
	var recalled []string
	if policy.UseFullRAG {
		recalled, _ = s.memories.Recall(ctx, userID, query, 8)
	} else {
		_, _ = s.store.AppendEvent(ctx, jobID, "rag_skipped", map[string]string{"reason": "degraded_policy"}, s.now().UTC())
	}
	if len(recalled) > 0 {
		history = append([]Message{{Role: "system", Content: "用户已确认的长期记忆：\n- " + strings.Join(recalled, "\n- ")}}, history...)
	}
	provider := s.providerFor(policy)
	_, _ = s.store.AppendEvent(ctx, jobID, "model_policy", map[string]string{"preferred_model_class": policy.PreferredModelClass}, s.now().UTC())
	text, usage, err := provider.Generate(ctx, persona, history)
	if err != nil {
		if errors.Is(err, reliability.ErrCircuitOpen) {
			s.deferJob(userID, jobID, "model_circuit_open", "generation deferred because the model circuit is open", s.now().UTC().Add(30*time.Second))
			return
		}
		status, code := "failed", "provider_error"
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
	_, _ = s.store.AppendEvent(context.Background(), jobID, status, map[string]string{"status": status, "error_code": code}, now)
}
func IsTerminal(status string) bool {
	return status == "completed" || status == "cancelled" || status == "failed" || status == "timed_out"
}
