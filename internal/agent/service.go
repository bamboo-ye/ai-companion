package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound   = errors.New("agent run not found")
	ErrConflict   = errors.New("agent run state conflict")
	ErrValidation = errors.New("agent run validation failed")
)

const (
	GraphName         = "ai-companion-supervisor"
	GraphVersion      = "3.71.0"
	DefaultRunTimeout = 15 * time.Minute
)

type Run struct {
	ID                          string          `json:"id"`
	ThreadID                    string          `json:"thread_id"`
	UserID                      string          `json:"user_id"`
	ConversationID              string          `json:"conversation_id"`
	CharacterID                 string          `json:"character_id"`
	Module                      string          `json:"module"`
	GraphName                   string          `json:"graph_name"`
	GraphVersion                string          `json:"graph_version"`
	AgentDefinitionKey          string          `json:"agent_definition_key,omitempty"`
	AgentDefinitionVersionID    string          `json:"agent_definition_version_id,omitempty"`
	AgentDefinitionVersion      int             `json:"agent_definition_version,omitempty"`
	AgentDefinitionRevision     int             `json:"agent_definition_revision,omitempty"`
	AgentDefinitionFingerprint  string          `json:"agent_definition_fingerprint,omitempty"`
	AgentDefinitionModelProfile string          `json:"agent_definition_model_profile,omitempty"`
	AgentDefinitionSnapshot     json.RawMessage `json:"agent_definition_snapshot,omitempty"`
	ModelProfileKey             string          `json:"model_profile_key,omitempty"`
	ModelProfileVersionID       string          `json:"model_profile_version_id,omitempty"`
	ModelProfileRevision        int             `json:"model_profile_revision,omitempty"`
	ModelProfileConfigVersion   string          `json:"model_profile_config_version,omitempty"`
	ModelProfileFingerprint     string          `json:"model_profile_fingerprint,omitempty"`
	ModelProfileSnapshot        json.RawMessage `json:"model_profile_snapshot,omitempty"`
	Status                      string          `json:"status"`
	IdempotencyKey              string          `json:"-"`
	Input                       json.RawMessage `json:"input"`
	Output                      json.RawMessage `json:"output,omitempty"`
	Resume                      json.RawMessage `json:"resume_resolution,omitempty"`
	ErrorCode                   string          `json:"error_code,omitempty"`
	ErrorMessage                string          `json:"error_message,omitempty"`
	AvailableAt                 time.Time       `json:"available_at"`
	DeadlineAt                  time.Time       `json:"deadline_at"`
	LeaseOwner                  string          `json:"-"`
	LeaseExpiresAt              *time.Time      `json:"-"`
	Revision                    int             `json:"revision"`
	CreatedAt                   time.Time       `json:"created_at"`
	UpdatedAt                   time.Time       `json:"updated_at"`
	CompletedAt                 *time.Time      `json:"completed_at,omitempty"`
	OTelTraceParent             string          `json:"otel_traceparent,omitempty"`
	OTelTraceState              string          `json:"otel_tracestate,omitempty"`
}

// RunModelProfileSnapshot is the immutable, secret-free model routing contract
// captured when a Run is accepted. A resumed Run keeps using this exact
// snapshot even after the active control-plane deployment changes.
type RunModelProfileSnapshot struct {
	ProfileKey    string            `json:"profile_key"`
	VersionID     string            `json:"version_id"`
	Version       int               `json:"version"`
	Revision      int               `json:"revision"`
	ConfigVersion string            `json:"config_version"`
	Fingerprint   string            `json:"fingerprint"`
	Variables     map[string]string `json:"variables"`
}

type RunModelProfileResolver func(context.Context) (RunModelProfileSnapshot, error)

// RunAgentDefinitionSnapshot is the immutable, validated Agent Studio DSL
// captured when a Run is accepted. The worker may safely resume the Run after
// a newer definition is published because it never re-resolves active state.
type RunAgentDefinitionSnapshot struct {
	Key          string          `json:"key"`
	VersionID    string          `json:"version_id"`
	Version      int             `json:"version"`
	Revision     int             `json:"revision"`
	Fingerprint  string          `json:"fingerprint"`
	ModelProfile string          `json:"model_profile"`
	Definition   json.RawMessage `json:"definition"`
}

type RunAgentDefinitionResolver func(context.Context, string, string) (RunAgentDefinitionSnapshot, bool, error)
type RunModelProfileKeyResolver func(context.Context, string) (RunModelProfileSnapshot, error)

type Event struct {
	ID        int64           `json:"id"`
	RunID     string          `json:"run_id"`
	Sequence  int64           `json:"sequence"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type CreateInput struct {
	UserID         string
	ConversationID string
	CharacterID    string
	Module         string
	IdempotencyKey string
	Payload        map[string]any
}

type Store interface {
	CreateAgentRun(context.Context, Run) (Run, bool, error)
	GetAgentRun(context.Context, string) (Run, error)
	GetActiveAgentRun(context.Context, string, string) (Run, error)
	ClaimAgentRun(context.Context, string, string, time.Time, time.Duration) (Run, error)
	ClaimNextAgentRun(context.Context, string, time.Time, time.Duration) (Run, error)
	DeferAgentRun(context.Context, string, string, int, time.Time, string, time.Time) (Run, error)
	PauseAgentRun(context.Context, string, string, int, json.RawMessage, time.Time) (Run, error)
	SuspendAgentRunForTool(context.Context, string, string, int, json.RawMessage, time.Time, time.Time) (Run, error)
	WakeAgentRunsForTool(context.Context, string, string, time.Time, int) ([]Run, error)
	ResolveAgentRun(context.Context, string, string, bool, string, time.Time) (Run, bool, error)
	CancelAgentRun(context.Context, string, string, time.Time) (Run, bool, error)
	FinalizeAgentRunCancellation(context.Context, string, string, int, time.Time) (Run, error)
	TimeoutAgentRun(context.Context, string, string, int, string, string, time.Time) (Run, error)
	ExpireAgentRuns(context.Context, time.Time, int) (int, error)
	CompleteAgentRun(context.Context, string, string, int, json.RawMessage, time.Time) (Run, error)
	FailAgentRun(context.Context, string, string, int, string, string, time.Time) (Run, error)
	ListAgentRunEvents(context.Context, string, int64, int) ([]Event, error)
}

type Service struct {
	store                   Store
	now                     func() time.Time
	runTimeout              time.Duration
	modelProfileResolver    RunModelProfileResolver
	modelProfileKeyResolver RunModelProfileKeyResolver
	agentDefinitionResolver RunAgentDefinitionResolver
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, runTimeout: DefaultRunTimeout}
}

func NewServiceWithClock(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now, runTimeout: DefaultRunTimeout}
}

func (s *Service) SetRunTimeout(timeout time.Duration) {
	if timeout > 0 {
		s.runTimeout = timeout
	}
}

func (s *Service) SetModelProfileResolver(resolver RunModelProfileResolver) {
	s.modelProfileResolver = resolver
	s.modelProfileKeyResolver = nil
}

func (s *Service) SetModelProfileKeyResolver(resolver RunModelProfileKeyResolver) {
	s.modelProfileKeyResolver = resolver
	s.modelProfileResolver = nil
}

func (s *Service) SetAgentDefinitionResolver(resolver RunAgentDefinitionResolver) {
	s.agentDefinitionResolver = resolver
}

func (s *Service) ActiveForConversation(ctx context.Context, userID, conversationID string) (Run, error) {
	userID = strings.TrimSpace(userID)
	conversationID = strings.TrimSpace(conversationID)
	if userID == "" || conversationID == "" {
		return Run{}, ErrValidation
	}
	return s.store.GetActiveAgentRun(ctx, userID, conversationID)
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Run, bool, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.ConversationID = strings.TrimSpace(input.ConversationID)
	input.CharacterID = strings.TrimSpace(input.CharacterID)
	input.Module = strings.TrimSpace(input.Module)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.UserID == "" || input.ConversationID == "" || input.CharacterID == "" {
		return Run{}, false, fmt.Errorf("%w: user, conversation and character are required", ErrValidation)
	}
	if input.Module != "companion" && input.Module != "life" && input.Module != "work" {
		return Run{}, false, fmt.Errorf("%w: unsupported module", ErrValidation)
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return Run{}, false, fmt.Errorf("%w: input payload is not JSON serializable", ErrValidation)
	}
	runID, err := id.New()
	if err != nil {
		return Run{}, false, err
	}
	now := s.now().UTC()
	item := Run{
		ID: runID, ThreadID: runID,
		UserID: input.UserID, ConversationID: input.ConversationID, CharacterID: input.CharacterID,
		Module: input.Module, GraphName: GraphName, GraphVersion: GraphVersion,
		Status: "accepted", IdempotencyKey: input.IdempotencyKey, Input: payload,
		AvailableAt: now, DeadlineAt: now.Add(s.runTimeout),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err = s.captureAgentDefinition(ctx, &item); err != nil {
		return Run{}, false, err
	}
	if err = s.captureModelProfile(ctx, &item); err != nil {
		return Run{}, false, err
	}
	return s.store.CreateAgentRun(ctx, item)
}

func (s *Service) captureModelProfile(ctx context.Context, run *Run) error {
	if s == nil || run == nil || (s.modelProfileResolver == nil && s.modelProfileKeyResolver == nil) {
		return nil
	}
	var snapshot RunModelProfileSnapshot
	var err error
	if s.modelProfileKeyResolver != nil {
		snapshot, err = s.modelProfileKeyResolver(ctx, run.AgentDefinitionModelProfile)
	} else {
		snapshot, err = s.modelProfileResolver(ctx)
	}
	if err != nil {
		return fmt.Errorf("resolve model profile for Agent Run: %w", err)
	}
	snapshot.ProfileKey = strings.TrimSpace(snapshot.ProfileKey)
	snapshot.VersionID = strings.TrimSpace(snapshot.VersionID)
	snapshot.ConfigVersion = strings.TrimSpace(snapshot.ConfigVersion)
	snapshot.Fingerprint = strings.ToLower(strings.TrimSpace(snapshot.Fingerprint))
	if snapshot.ProfileKey == "" || snapshot.VersionID == "" || snapshot.Version <= 0 ||
		snapshot.Revision <= 0 || snapshot.ConfigVersion == "" ||
		!validConfigurationFingerprint(snapshot.Fingerprint) {
		return fmt.Errorf("%w: active model profile snapshot is incomplete", ErrValidation)
	}
	variables := make(map[string]string, len(snapshot.Variables))
	for key, value := range snapshot.Variables {
		key = strings.TrimSpace(key)
		upper := strings.ToUpper(key)
		if key == "" || !strings.HasPrefix(key, "MODEL_") ||
			strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') ||
			unsafeModelVariableName(upper) ||
			len(key) > 128 || len(value) > 4096 {
			return fmt.Errorf("%w: model profile snapshot contains an unsafe runtime variable", ErrValidation)
		}
		variables[key] = value
	}
	if variables["MODEL_CONFIG_VERSION"] != snapshot.ConfigVersion || len(variables) == 0 {
		return fmt.Errorf("%w: model profile snapshot version does not match its variables", ErrValidation)
	}
	snapshot.Variables = variables
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode model profile snapshot: %w", err)
	}
	run.ModelProfileKey = snapshot.ProfileKey
	run.ModelProfileVersionID = snapshot.VersionID
	run.ModelProfileRevision = snapshot.Revision
	run.ModelProfileConfigVersion = snapshot.ConfigVersion
	run.ModelProfileFingerprint = snapshot.Fingerprint
	run.ModelProfileSnapshot = encoded
	return nil
}

func (s *Service) captureAgentDefinition(ctx context.Context, run *Run) error {
	if s == nil || run == nil || s.agentDefinitionResolver == nil {
		return nil
	}
	snapshot, found, err := s.agentDefinitionResolver(ctx, run.Module, run.UserID)
	if err != nil {
		return fmt.Errorf("resolve Agent definition for Run: %w", err)
	}
	if !found {
		return nil
	}
	snapshot.Key = strings.TrimSpace(snapshot.Key)
	snapshot.VersionID = strings.TrimSpace(snapshot.VersionID)
	snapshot.Fingerprint = strings.ToLower(strings.TrimSpace(snapshot.Fingerprint))
	snapshot.ModelProfile = strings.TrimSpace(snapshot.ModelProfile)
	if snapshot.Key == "" || snapshot.VersionID == "" || snapshot.Version <= 0 ||
		snapshot.Revision <= 0 || snapshot.ModelProfile == "" ||
		!validConfigurationFingerprint(snapshot.Fingerprint) || len(snapshot.Definition) == 0 ||
		len(snapshot.Definition) > 1<<20 {
		return fmt.Errorf("%w: active Agent definition snapshot is incomplete", ErrValidation)
	}
	var definition struct {
		Modules      []string `json:"modules"`
		ModelProfile string   `json:"model_profile"`
		Budget       struct {
			TimeoutMS int `json:"timeout_ms"`
		} `json:"budget"`
	}
	if err = json.Unmarshal(snapshot.Definition, &definition); err != nil ||
		strings.TrimSpace(definition.ModelProfile) != snapshot.ModelProfile ||
		!containsString(definition.Modules, run.Module) ||
		definition.Budget.TimeoutMS < 1000 || definition.Budget.TimeoutMS > 900000 {
		return fmt.Errorf("%w: active Agent definition snapshot does not match the Run", ErrValidation)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("%w: Agent definition snapshot is not serializable", ErrValidation)
	}
	run.AgentDefinitionKey = snapshot.Key
	run.AgentDefinitionVersionID = snapshot.VersionID
	run.AgentDefinitionVersion = snapshot.Version
	run.AgentDefinitionRevision = snapshot.Revision
	run.AgentDefinitionFingerprint = snapshot.Fingerprint
	run.AgentDefinitionModelProfile = snapshot.ModelProfile
	run.AgentDefinitionSnapshot = encoded
	run.DeadlineAt = run.CreatedAt.Add(time.Duration(definition.Budget.TimeoutMS) * time.Millisecond)
	return nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == expected {
			return true
		}
	}
	return false
}

func unsafeModelVariableName(name string) bool {
	return name == "MODEL_API_KEY" || strings.HasSuffix(name, "_SECRET") ||
		strings.HasSuffix(name, "_PASSWORD") || strings.HasSuffix(name, "_ACCESS_TOKEN") ||
		strings.HasSuffix(name, "_AUTH_TOKEN") || strings.HasSuffix(name, "_CREDENTIAL")
}

func validConfigurationFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (s *Service) Get(ctx context.Context, runID string) (Run, error) {
	if strings.TrimSpace(runID) == "" {
		return Run{}, ErrValidation
	}
	return s.store.GetAgentRun(ctx, runID)
}

func (s *Service) GetForUser(ctx context.Context, userID, runID string) (Run, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(runID) == "" {
		return Run{}, ErrValidation
	}
	item, err := s.store.GetAgentRun(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	if item.UserID != userID {
		return Run{}, ErrNotFound
	}
	return item, nil
}

// RetryChat creates a new run for the same persisted user message. The
// original failed/cancelled exchange remains immutable, while the idempotency
// key prevents a network replay from starting duplicate work.
func (s *Service) RetryChat(ctx context.Context, userID, runID, idempotencyKey string) (Run, bool, error) {
	return s.RetryChatWithPayload(ctx, userID, runID, idempotencyKey, nil)
}

// RetryChatWithPayload retries a terminal chat run while allowing the HTTP
// boundary to repair trusted attachment metadata that was absent from legacy
// input. A nil payload preserves the original retry behavior.
func (s *Service) RetryChatWithPayload(
	ctx context.Context,
	userID, runID, idempotencyKey string,
	payload map[string]any,
) (Run, bool, error) {
	userID = strings.TrimSpace(userID)
	runID = strings.TrimSpace(runID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if userID == "" || runID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return Run{}, false, ErrValidation
	}
	prior, err := s.GetForUser(ctx, userID, runID)
	if err != nil {
		return Run{}, false, err
	}
	if prior.Status != "failed" && prior.Status != "timed_out" && prior.Status != "cancelled" {
		return Run{}, false, ErrConflict
	}
	var original map[string]any
	if err = json.Unmarshal(prior.Input, &original); err != nil || original == nil {
		return Run{}, false, ErrValidation
	}
	if payload == nil {
		payload = original
	} else if strings.TrimSpace(fmt.Sprint(payload["message_id"])) == "" ||
		fmt.Sprint(payload["message_id"]) != fmt.Sprint(original["message_id"]) {
		return Run{}, false, ErrValidation
	}
	return s.Create(ctx, CreateInput{
		UserID: prior.UserID, ConversationID: prior.ConversationID,
		CharacterID: prior.CharacterID, Module: prior.Module,
		IdempotencyKey: "chat-retry:" + idempotencyKey, Payload: payload,
	})
}

func (s *Service) Claim(ctx context.Context, runID, owner string, lease time.Duration) (Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || lease <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.ClaimAgentRun(ctx, runID, owner, s.now().UTC(), lease)
}

func (s *Service) ClaimNext(ctx context.Context, owner string, lease time.Duration) (Run, error) {
	if strings.TrimSpace(owner) == "" || lease <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.ClaimNextAgentRun(ctx, owner, s.now().UTC(), lease)
}

func (s *Service) Defer(ctx context.Context, runID, owner string, revision int, delay time.Duration, reason string) (Run, error) {
	if revision <= 0 || delay < 0 {
		return Run{}, ErrValidation
	}
	now := s.now().UTC()
	return s.store.DeferAgentRun(ctx, runID, owner, revision, now.Add(delay), reason, now)
}

func (s *Service) Complete(ctx context.Context, runID, owner string, revision int, output map[string]any) (Run, error) {
	if revision <= 0 {
		return Run{}, ErrValidation
	}
	payload, err := json.Marshal(output)
	if err != nil {
		return Run{}, ErrValidation
	}
	return s.store.CompleteAgentRun(ctx, runID, owner, revision, payload, s.now().UTC())
}

func (s *Service) PauseForApproval(ctx context.Context, runID, owner string, revision int, output map[string]any) (Run, error) {
	if revision <= 0 {
		return Run{}, ErrValidation
	}
	payload, err := json.Marshal(output)
	if err != nil {
		return Run{}, ErrValidation
	}
	return s.store.PauseAgentRun(ctx, runID, owner, revision, payload, s.now().UTC())
}

func (s *Service) SuspendForTool(ctx context.Context, runID, owner string, revision int, output map[string]any, delay time.Duration) (Run, error) {
	if revision <= 0 || delay < 0 {
		return Run{}, ErrValidation
	}
	payload, err := json.Marshal(output)
	if err != nil {
		return Run{}, ErrValidation
	}
	now := s.now().UTC()
	return s.store.SuspendAgentRunForTool(ctx, runID, owner, revision, payload, now.Add(delay), now)
}

// WakeForTool advances waiting runs immediately after a durable tool task
// reaches a terminal state. The scheduled polling resume remains in the
// outbox as a recovery fallback; the state transition is idempotent.
func (s *Service) WakeForTool(ctx context.Context, userID, taskID string, limit int) ([]Run, error) {
	userID = strings.TrimSpace(userID)
	taskID = strings.TrimSpace(taskID)
	if userID == "" || taskID == "" || len(taskID) > 128 {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return s.store.WakeAgentRunsForTool(ctx, taskID, userID, s.now().UTC(), limit)
}

func (s *Service) ResolveApproval(ctx context.Context, userID, runID string, approved bool, idempotencyKey string) (Run, bool, error) {
	userID = strings.TrimSpace(userID)
	runID = strings.TrimSpace(runID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if userID == "" || runID == "" || idempotencyKey == "" || len(idempotencyKey) > 128 {
		return Run{}, false, ErrValidation
	}
	return s.store.ResolveAgentRun(ctx, runID, userID, approved, idempotencyKey, s.now().UTC())
}

func (s *Service) Cancel(ctx context.Context, userID, runID string) (Run, bool, error) {
	userID = strings.TrimSpace(userID)
	runID = strings.TrimSpace(runID)
	if userID == "" || runID == "" {
		return Run{}, false, ErrValidation
	}
	return s.store.CancelAgentRun(ctx, runID, userID, s.now().UTC())
}

func (s *Service) FinalizeCancellation(ctx context.Context, runID, owner string, revision int) (Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || revision <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.FinalizeAgentRunCancellation(ctx, runID, owner, revision, s.now().UTC())
}

func (s *Service) Timeout(ctx context.Context, runID, owner string, revision int) (Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || revision <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.TimeoutAgentRun(
		ctx, runID, owner, revision,
		"run_timeout", "Agent run exceeded its total deadline", s.now().UTC(),
	)
}

func (s *Service) TimeoutForRetryDeadline(ctx context.Context, runID, owner string, revision int) (Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(owner) == "" || revision <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.TimeoutAgentRun(
		ctx, runID, owner, revision,
		"execution_retry_deadline_exhausted",
		"Agent execution retry cannot complete before the total deadline",
		s.now().UTC(),
	)
}

func (s *Service) ExpireDue(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.ExpireAgentRuns(ctx, s.now().UTC(), limit)
}

func (s *Service) Fail(ctx context.Context, runID, owner string, revision int, code, message string) (Run, error) {
	code, message = strings.TrimSpace(code), strings.TrimSpace(message)
	if revision <= 0 || code == "" || message == "" {
		return Run{}, ErrValidation
	}
	return s.store.FailAgentRun(ctx, runID, owner, revision, code, message, s.now().UTC())
}

func (s *Service) Events(ctx context.Context, runID string, after int64, limit int) ([]Event, error) {
	if strings.TrimSpace(runID) == "" || after < 0 {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return s.store.ListAgentRunEvents(ctx, runID, after, limit)
}

func IsTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}
