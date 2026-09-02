package skill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/chatattachment"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound       = errors.New("skill resource not found")
	ErrValidation     = errors.New("skill validation failed")
	ErrConflict       = errors.New("skill state conflict")
	ErrIdempotencyKey = errors.New("idempotency key is required")
	ErrDisabled       = errors.New("skill is disabled")
	ErrNoQueuedRun    = errors.New("no queued skill run available")
)

type Manifest struct {
	Name                 string         `json:"name"`
	Version              string         `json:"version"`
	DisplayName          string         `json:"display_name"`
	Description          string         `json:"description"`
	Category             string         `json:"category"`
	RiskLevel            string         `json:"risk_level"`
	RequiresConfirmation bool           `json:"requires_confirmation"`
	Enabled              bool           `json:"enabled"`
	ToolName             string         `json:"tool_name"`
	AllowedTools         []string       `json:"allowed_tools"`
	StateGraph           []string       `json:"state_graph"`
	TimeoutMS            int            `json:"timeout_ms"`
	MaxSteps             int            `json:"max_steps"`
	MaxInputBytes        int            `json:"max_input_bytes"`
	MaxOutputFileBytes   int            `json:"max_output_file_bytes"`
	MaxCostMicros        int64          `json:"max_cost_micros"`
	ExecutionMode        string         `json:"execution_mode"`
	InputSchema          map[string]any `json:"input_schema"`
	OutputSchema         map[string]any `json:"output_schema"`
	RepairPolicies       []RepairPolicy `json:"repair_policies,omitempty"`
}

type RepairPolicy struct {
	OperatorID          string `json:"operator_id"`
	FieldPath           string `json:"field_path"`
	Extension           string `json:"extension,omitempty"`
	SemanticsPreserving bool   `json:"semantics_preserving"`
	Preflight           bool   `json:"preflight"`
}

type Run struct {
	ID                   string          `json:"id"`
	UserID               string          `json:"-"`
	SkillName            string          `json:"skill_name"`
	SkillVersion         string          `json:"skill_version"`
	ExecutionMode        string          `json:"execution_mode"`
	Status               string          `json:"status"`
	CurrentState         string          `json:"current_state"`
	RiskLevel            string          `json:"risk_level"`
	RequiresConfirmation bool            `json:"requires_confirmation"`
	Input                json.RawMessage `json:"input"`
	Output               json.RawMessage `json:"output,omitempty"`
	ConversationID       string          `json:"conversation_id,omitempty"`
	OriginMessageID      string          `json:"origin_message_id,omitempty"`
	CreateKey            string          `json:"-"`
	ConfirmationKey      string          `json:"-"`
	LastAction           string          `json:"-"`
	LastActionKey        string          `json:"-"`
	Attempt              int             `json:"attempt"`
	MaxSteps             int             `json:"max_steps"`
	TimeoutMS            int             `json:"timeout_ms"`
	MaxInputBytes        int             `json:"max_input_bytes"`
	MaxCostMicros        int64           `json:"max_cost_micros"`
	ErrorCode            string          `json:"error_code,omitempty"`
	ErrorMessage         string          `json:"error_message,omitempty"`
	Revision             int             `json:"revision"`
	Steps                []Step          `json:"steps"`
	Files                []GeneratedFile `json:"files"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	AvailableAt          time.Time       `json:"-"`
	WorkerID             string          `json:"-"`
	LeaseExpiresAt       *time.Time      `json:"-"`
	DeliveryContent      string          `json:"-"`
}

type Step struct {
	ID           string          `json:"id"`
	Sequence     int             `json:"sequence"`
	State        string          `json:"state"`
	Status       string          `json:"status"`
	ToolName     string          `json:"tool_name,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type GeneratedFile struct {
	ID         string    `json:"id"`
	RunID      string    `json:"run_id"`
	Name       string    `json:"name"`
	MediaType  string    `json:"media_type"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	StorageKey string    `json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

type FileOutput struct {
	Name      string
	MediaType string
	Data      []byte
}

type ToolResult struct {
	Output map[string]any
	Files  []FileOutput
}

type Handler interface {
	Execute(context.Context, map[string]any) (ToolResult, error)
}

type HandlerFunc func(context.Context, map[string]any) (ToolResult, error)

func (f HandlerFunc) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	return f(ctx, input)
}

type Definition struct {
	Manifest Manifest
	Handler  Handler
}

type Registry struct {
	mu          sync.RWMutex
	definitions map[string]Definition
}

func NewRegistry() *Registry { return &Registry{definitions: make(map[string]Definition)} }

func (r *Registry) Register(definition Definition) error {
	manifest := definition.Manifest
	if manifest.Name == "" || manifest.Version == "" || manifest.ToolName == "" || definition.Handler == nil {
		return fmt.Errorf("%w: incomplete skill definition", ErrValidation)
	}
	if manifest.RiskLevel != "none" && manifest.RiskLevel != "low" && manifest.RiskLevel != "medium" && manifest.RiskLevel != "high" {
		return fmt.Errorf("%w: invalid risk level", ErrValidation)
	}
	if (manifest.RiskLevel == "medium" || manifest.RiskLevel == "high") && !manifest.RequiresConfirmation {
		return fmt.Errorf("%w: risky skills require confirmation", ErrValidation)
	}
	if manifest.TimeoutMS <= 0 {
		manifest.TimeoutMS = 10_000
	}
	if manifest.MaxSteps <= 0 {
		manifest.MaxSteps = 8
	}
	if manifest.MaxSteps < 5 {
		return fmt.Errorf("%w: max_steps must be at least 5", ErrValidation)
	}
	seenRepairPolicies := make(map[string]struct{}, len(manifest.RepairPolicies))
	for index := range manifest.RepairPolicies {
		policy := &manifest.RepairPolicies[index]
		policy.OperatorID = strings.TrimSpace(policy.OperatorID)
		policy.FieldPath = strings.TrimSpace(policy.FieldPath)
		policy.Extension = strings.TrimSpace(policy.Extension)
		if policy.OperatorID == "" || !strings.HasPrefix(policy.FieldPath, "/") || !policy.SemanticsPreserving {
			return fmt.Errorf("%w: invalid repair policy", ErrValidation)
		}
		policyKey := policy.OperatorID + "\x00" + policy.FieldPath
		if _, exists := seenRepairPolicies[policyKey]; exists {
			return fmt.Errorf("%w: duplicate repair policy", ErrValidation)
		}
		seenRepairPolicies[policyKey] = struct{}{}
	}
	if manifest.MaxInputBytes <= 0 {
		manifest.MaxInputBytes = 64 << 10
	}
	if manifest.MaxOutputFileBytes <= 0 {
		manifest.MaxOutputFileBytes = 10 << 20
	}
	if manifest.MaxCostMicros < 0 {
		return fmt.Errorf("%w: max_cost_micros cannot be negative", ErrValidation)
	}
	if manifest.ExecutionMode == "" {
		manifest.ExecutionMode = "inline"
	}
	if manifest.ExecutionMode != "inline" && manifest.ExecutionMode != "worker" {
		return fmt.Errorf("%w: execution_mode must be inline or worker", ErrValidation)
	}
	if len(manifest.AllowedTools) == 0 {
		manifest.AllowedTools = []string{manifest.ToolName}
	}
	allowed := false
	for _, toolName := range manifest.AllowedTools {
		if toolName == manifest.ToolName {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("%w: primary tool is not in the allowlist", ErrValidation)
	}
	if len(manifest.StateGraph) == 0 {
		manifest.StateGraph = []string{"receive", "validate", "plan", "confirm", "execute", "deliver"}
	}
	definition.Manifest = manifest
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.definitions[manifest.Name]; exists {
		return fmt.Errorf("%w: duplicate skill %s", ErrConflict, manifest.Name)
	}
	r.definitions[manifest.Name] = definition
	return nil
}

func (r *Registry) Get(name string) (Definition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definition, ok := r.definitions[name]
	if !ok {
		return Definition{}, ErrNotFound
	}
	return definition, nil
}

func (r *Registry) List() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Manifest, 0, len(r.definitions))
	for _, definition := range r.definitions {
		items = append(items, definition.Manifest)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func (r *Registry) SetEnabled(name string, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	definition, ok := r.definitions[name]
	if !ok {
		return ErrNotFound
	}
	definition.Manifest.Enabled = enabled
	r.definitions[name] = definition
	return nil
}

type Store interface {
	CreateSkillRun(context.Context, Run) error
	GetSkillRun(context.Context, string, string) (Run, error)
	FindSkillRunByCreateKey(context.Context, string, string) (Run, error)
	ListSkillRuns(context.Context, string, int) ([]Run, error)
	SaveSkillRun(context.Context, Run, int, []Step, []GeneratedFile) error
	ShareGeneratedFileWithWorkspace(context.Context, string, string, string, string, time.Time) error
	ListWorkspaceGeneratedFiles(context.Context, string, int) ([]GeneratedFile, error)
	GetWorkspaceGeneratedFile(context.Context, string, string) (GeneratedFile, error)
	ListSkillSettings(context.Context, string) (map[string]bool, error)
	SetSkillEnabled(context.Context, string, string, bool, time.Time) error
	RecoverInterruptedSkillRuns(context.Context, time.Time) (int, error)
	ClaimSkillRun(context.Context, string, time.Time, time.Duration) (Run, error)
	ClaimSkillRunByID(context.Context, string, string, time.Time, time.Duration) (Run, error)
	RenewSkillRunLease(context.Context, string, string, int, time.Time, time.Duration) error
}

type FileStore interface {
	Put(context.Context, string, string, string, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type Service struct {
	store    Store
	files    FileStore
	registry *Registry
	now      func() time.Time
	queue    bool
}

func NewService(store Store, files FileStore, registry *Registry) *Service {
	return &Service{store: store, files: files, registry: registry, now: time.Now}
}

func (s *Service) SetWorkerQueue(enabled bool) { s.queue = enabled }

func (s *Service) Skills(ctx context.Context, userID string) ([]Manifest, error) {
	settings, err := s.store.ListSkillSettings(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := s.registry.List()
	for index := range items {
		if enabled, exists := settings[items[index].Name]; exists {
			items[index].Enabled = items[index].Enabled && enabled
		}
	}
	return items, nil
}

func (s *Service) SetEnabled(ctx context.Context, userID, skillName string, enabled bool) (Manifest, error) {
	definition, err := s.registry.Get(skillName)
	if err != nil {
		return Manifest{}, err
	}
	if enabled && !definition.Manifest.Enabled {
		return Manifest{}, ErrDisabled
	}
	if err = s.store.SetSkillEnabled(ctx, userID, skillName, enabled, s.now().UTC()); err != nil {
		return Manifest{}, err
	}
	definition.Manifest.Enabled = definition.Manifest.Enabled && enabled
	return definition.Manifest, nil
}

func (s *Service) ManifestForUser(ctx context.Context, userID, skillName string) (Manifest, error) {
	definition, err := s.definitionForUser(ctx, userID, skillName)
	if err != nil {
		return Manifest{}, err
	}
	return definition.Manifest, nil
}

func (s *Service) Recover(ctx context.Context) (int, error) {
	return s.store.RecoverInterruptedSkillRuns(ctx, s.now().UTC())
}

func (s *Service) Claim(ctx context.Context, workerID string, lease time.Duration) (Run, error) {
	if strings.TrimSpace(workerID) == "" || lease <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.ClaimSkillRun(ctx, workerID, s.now().UTC(), lease)
}

func (s *Service) ClaimByID(ctx context.Context, runID, workerID string, lease time.Duration) (Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(workerID) == "" || lease <= 0 {
		return Run{}, ErrValidation
	}
	return s.store.ClaimSkillRunByID(ctx, runID, workerID, s.now().UTC(), lease)
}

func (s *Service) RenewLease(ctx context.Context, run Run, workerID string, lease time.Duration) error {
	return s.store.RenewSkillRunLease(ctx, run.ID, workerID, run.Revision, s.now().UTC(), lease)
}

func (s *Service) ExecuteClaimed(ctx context.Context, run Run, workerID string) (Run, error) {
	if run.Status != "running" || run.ExecutionMode != "worker" || run.WorkerID != workerID {
		return Run{}, ErrConflict
	}
	definition, err := s.registry.Get(run.SkillName)
	if err != nil {
		return Run{}, err
	}
	return s.execute(ctx, run, definition)
}

func (s *Service) shouldQueue(manifest Manifest) bool {
	return s.queue && manifest.ExecutionMode == "worker"
}

func (s *Service) definitionForUser(ctx context.Context, userID, skillName string) (Definition, error) {
	definition, err := s.registry.Get(skillName)
	if err != nil {
		return Definition{}, err
	}
	if !definition.Manifest.Enabled {
		return Definition{}, ErrDisabled
	}
	settings, err := s.store.ListSkillSettings(ctx, userID)
	if err != nil {
		return Definition{}, err
	}
	if enabled, exists := settings[skillName]; exists && !enabled {
		return Definition{}, ErrDisabled
	}
	return definition, nil
}

func (s *Service) Start(ctx context.Context, userID, skillName, createKey string, input map[string]any) (Run, bool, error) {
	return s.start(ctx, userID, skillName, createKey, input, "", "")
}

func (s *Service) StartForConversation(
	ctx context.Context,
	userID, skillName, createKey string,
	input map[string]any,
	conversationID, originMessageID string,
) (Run, bool, error) {
	if strings.TrimSpace(conversationID) == "" || strings.TrimSpace(originMessageID) == "" {
		return Run{}, false, ErrValidation
	}
	return s.start(
		ctx, userID, skillName, createKey, input,
		strings.TrimSpace(conversationID), strings.TrimSpace(originMessageID),
	)
}

func (s *Service) start(
	ctx context.Context,
	userID, skillName, createKey string,
	input map[string]any,
	conversationID, originMessageID string,
) (Run, bool, error) {
	if strings.TrimSpace(createKey) == "" || len(createKey) > 191 {
		return Run{}, false, ErrIdempotencyKey
	}
	definition, err := s.definitionForUser(ctx, userID, skillName)
	if err != nil {
		return Run{}, false, err
	}
	if err = validateObject(input, definition.Manifest.InputSchema); err != nil {
		return Run{}, false, err
	}
	if existing, findErr := s.store.FindSkillRunByCreateKey(ctx, userID, createKey); findErr == nil {
		if existing.SkillName != skillName {
			return Run{}, false, ErrConflict
		}
		if conversationID != "" && (existing.ConversationID != conversationID || existing.OriginMessageID != originMessageID) {
			return Run{}, false, ErrConflict
		}
		return existing, false, nil
	} else if !errors.Is(findErr, ErrNotFound) {
		return Run{}, false, findErr
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Run{}, false, fmt.Errorf("%w: invalid input", ErrValidation)
	}
	if len(payload) > definition.Manifest.MaxInputBytes {
		return Run{}, false, fmt.Errorf("%w: input exceeds skill budget", ErrValidation)
	}
	runID, err := id.New()
	if err != nil {
		return Run{}, false, err
	}
	now := s.now().UTC()
	status, state := "running", "execute"
	if definition.Manifest.RequiresConfirmation {
		status, state = "waiting_confirmation", "confirm"
	} else if s.shouldQueue(definition.Manifest) {
		status, state = "queued", "queued"
	}
	run := Run{
		ID: runID, UserID: userID, SkillName: definition.Manifest.Name, SkillVersion: definition.Manifest.Version, ExecutionMode: definition.Manifest.ExecutionMode,
		Status: status, CurrentState: state, RiskLevel: definition.Manifest.RiskLevel,
		RequiresConfirmation: definition.Manifest.RequiresConfirmation, Input: payload,
		ConversationID: conversationID, OriginMessageID: originMessageID, CreateKey: createKey,
		Attempt: 1, MaxSteps: definition.Manifest.MaxSteps, TimeoutMS: definition.Manifest.TimeoutMS,
		MaxInputBytes: definition.Manifest.MaxInputBytes, MaxCostMicros: definition.Manifest.MaxCostMicros,
		Revision: 1, Steps: []Step{}, Files: []GeneratedFile{}, CreatedAt: now, UpdatedAt: now, AvailableAt: now,
	}
	for _, stateName := range []string{"receive", "validate", "plan"} {
		step, stepErr := completedStep(len(run.Steps)+1, stateName, "", nil, nil, now)
		if stepErr != nil {
			return Run{}, false, stepErr
		}
		run.Steps = append(run.Steps, step)
	}
	if err = s.store.CreateSkillRun(ctx, run); err != nil {
		if errors.Is(err, ErrConflict) {
			existing, reloadErr := s.store.FindSkillRunByCreateKey(ctx, userID, createKey)
			return existing, false, reloadErr
		}
		return Run{}, false, err
	}
	if run.Status == "running" {
		run, err = s.execute(ctx, run, definition)
	}
	return run, true, err
}

func (s *Service) Confirm(ctx context.Context, userID, runID, key string) (Run, error) {
	if strings.TrimSpace(key) == "" || len(key) > 191 {
		return Run{}, ErrIdempotencyKey
	}
	run, err := s.store.GetSkillRun(ctx, userID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.ConfirmationKey != "" {
		if run.ConfirmationKey == key {
			return run, nil
		}
		return Run{}, ErrConflict
	}
	if run.Status != "waiting_confirmation" || !run.RequiresConfirmation {
		return Run{}, ErrConflict
	}
	definition, err := s.definitionForUser(ctx, userID, run.SkillName)
	if err != nil {
		return Run{}, err
	}
	if len(run.Steps)+3 > run.MaxSteps {
		return Run{}, ErrConflict
	}
	expected := run.Revision
	run.ConfirmationKey = key
	run.Status, run.CurrentState, run.UpdatedAt = "running", "execute", s.now().UTC()
	if s.shouldQueue(definition.Manifest) {
		run.Status, run.CurrentState, run.AvailableAt = "queued", "queued", run.UpdatedAt
	}
	step, _ := completedStep(len(run.Steps)+1, "confirm", "", nil, nil, run.UpdatedAt)
	run.Steps = append(run.Steps, step)
	run.Revision++
	if err = s.store.SaveSkillRun(ctx, run, expected, []Step{step}, nil); err != nil {
		if errors.Is(err, ErrConflict) {
			latest, reloadErr := s.store.GetSkillRun(ctx, userID, runID)
			if reloadErr == nil && latest.ConfirmationKey == key {
				return latest, nil
			}
		}
		return Run{}, err
	}
	if run.Status == "queued" {
		return run, nil
	}
	return s.execute(ctx, run, definition)
}

func (s *Service) Cancel(ctx context.Context, userID, runID, key string) (Run, error) {
	if strings.TrimSpace(key) == "" || len(key) > 191 {
		return Run{}, ErrIdempotencyKey
	}
	run, err := s.store.GetSkillRun(ctx, userID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.LastAction == "cancel" && run.LastActionKey == key {
		return run, nil
	}
	if isTerminal(run.Status) {
		return Run{}, ErrConflict
	}
	if len(run.Steps)+1 > run.MaxSteps {
		return Run{}, ErrConflict
	}
	expected := run.Revision
	now := s.now().UTC()
	run.Status, run.CurrentState, run.LastAction, run.LastActionKey = "cancelled", "cancelled", "cancel", key
	run.WorkerID, run.LeaseExpiresAt = "", nil
	run.UpdatedAt, run.CompletedAt = now, &now
	step, _ := completedStep(len(run.Steps)+1, "cancel", "", nil, nil, now)
	run.Steps = append(run.Steps, step)
	run.Revision++
	if err = s.store.SaveSkillRun(ctx, run, expected, []Step{step}, nil); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Service) Retry(ctx context.Context, userID, runID, key string) (Run, error) {
	if strings.TrimSpace(key) == "" || len(key) > 191 {
		return Run{}, ErrIdempotencyKey
	}
	run, err := s.store.GetSkillRun(ctx, userID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.LastAction == "retry" && run.LastActionKey == key {
		return run, nil
	}
	if run.Status != "failed" && run.Status != "cancelled" {
		return Run{}, ErrConflict
	}
	definition, err := s.definitionForUser(ctx, userID, run.SkillName)
	if err != nil {
		return Run{}, err
	}
	requiredSteps := 3
	if run.RequiresConfirmation {
		requiredSteps = 4
	}
	if len(run.Steps)+requiredSteps > run.MaxSteps {
		return Run{}, ErrConflict
	}
	expected := run.Revision
	now := s.now().UTC()
	run.Attempt++
	run.ErrorCode, run.ErrorMessage, run.LastAction, run.LastActionKey = "", "", "retry", key
	run.DeliveryContent = ""
	run.CompletedAt = nil
	run.Status, run.CurrentState = "running", "execute"
	if run.RequiresConfirmation {
		run.Status, run.CurrentState, run.ConfirmationKey = "waiting_confirmation", "confirm", ""
	} else if s.shouldQueue(definition.Manifest) {
		run.Status, run.CurrentState, run.AvailableAt = "queued", "queued", now
	}
	run.WorkerID, run.LeaseExpiresAt = "", nil
	run.UpdatedAt = now
	step, _ := completedStep(len(run.Steps)+1, "retry", "", nil, nil, now)
	run.Steps = append(run.Steps, step)
	run.Revision++
	if err = s.store.SaveSkillRun(ctx, run, expected, []Step{step}, nil); err != nil {
		return Run{}, err
	}
	if run.Status == "running" {
		return s.execute(ctx, run, definition)
	}
	return run, nil
}

// RepairRetry starts a new attempt for the same logical Skill Run after a
// policy-authorized, semantics-preserving argument repair. The existing run ID
// is retained so task history, chat delivery, and manual retry all share one
// lineage. CAS persistence arbitrates races with user-triggered retries.
func (s *Service) RepairRetry(ctx context.Context, userID, runID, key, operatorID string, input map[string]any) (Run, error) {
	if strings.TrimSpace(key) == "" || len(key) > 191 {
		return Run{}, ErrIdempotencyKey
	}
	operatorID = strings.TrimSpace(operatorID)
	if operatorID == "" || input == nil {
		return Run{}, ErrValidation
	}
	run, err := s.store.GetSkillRun(ctx, userID, runID)
	if err != nil {
		return Run{}, err
	}
	if run.LastAction == "repair_retry" && run.LastActionKey == key {
		return run, nil
	}
	if run.Status != "failed" {
		return Run{}, ErrConflict
	}
	failure, ok := toolFailureFromOutput(run.Output)
	if !ok || (!failure.Repairable && !failure.RetrySameInput) || failure.SideEffectState != "none" {
		return Run{}, ErrValidation
	}
	definition, err := s.definitionForUser(ctx, userID, run.SkillName)
	if err != nil {
		return Run{}, err
	}
	if err = validateRepairRetry(definition.Manifest, failure, operatorID, run.Input, input); err != nil {
		return Run{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) == 0 || len(encoded) > run.MaxInputBytes {
		return Run{}, ErrValidation
	}
	if len(run.Steps)+3 > run.MaxSteps {
		return Run{}, ErrConflict
	}
	expected := run.Revision
	now := s.now().UTC()
	before := sha256.Sum256(run.Input)
	after := sha256.Sum256(encoded)
	auditInput, _ := json.Marshal(map[string]any{
		"operator_id":      operatorID,
		"failure_code":     failure.Code,
		"before_args_hash": hex.EncodeToString(before[:]),
		"after_args_hash":  hex.EncodeToString(after[:]),
		"field_paths":      failure.FieldPaths,
	})
	run.Input = encoded
	run.Output = nil
	run.Attempt++
	run.ErrorCode, run.ErrorMessage = "", ""
	run.DeliveryContent = ""
	run.LastAction, run.LastActionKey = "repair_retry", key
	run.CompletedAt = nil
	run.Status, run.CurrentState = "running", "execute"
	if s.shouldQueue(definition.Manifest) {
		run.Status, run.CurrentState, run.AvailableAt = "queued", "queued", now
	}
	run.WorkerID, run.LeaseExpiresAt = "", nil
	run.UpdatedAt = now
	step, _ := completedStep(len(run.Steps)+1, "repair", operatorID, auditInput, nil, now)
	run.Steps = append(run.Steps, step)
	run.Revision++
	if err = s.store.SaveSkillRun(ctx, run, expected, []Step{step}, nil); err != nil {
		return Run{}, err
	}
	if run.Status == "running" {
		return s.execute(ctx, run, definition)
	}
	return run, nil
}

func validateRepairRetry(manifest Manifest, failure ToolFailure, operatorID string, oldInput json.RawMessage, newInput map[string]any) error {
	if operatorID == "transport.retry" {
		if !failure.RetrySameInput || failure.SideEffectState != "none" {
			return ErrValidation
		}
		var previous map[string]any
		if json.Unmarshal(oldInput, &previous) != nil || !reflect.DeepEqual(previous, newInput) {
			return ErrValidation
		}
		if err := validateObject(newInput, manifest.InputSchema); err != nil {
			return fmt.Errorf("%w: retry input: %v", ErrValidation, err)
		}
		return nil
	}
	allowedByFailure := false
	for _, allowed := range failure.AllowedRepairs {
		if strings.TrimSpace(allowed) == operatorID {
			allowedByFailure = true
			break
		}
	}
	if !allowedByFailure {
		return ErrValidation
	}
	var policy *RepairPolicy
	for index := range manifest.RepairPolicies {
		if manifest.RepairPolicies[index].OperatorID == operatorID {
			policy = &manifest.RepairPolicies[index]
			break
		}
	}
	if policy == nil || !policy.SemanticsPreserving || !containsString(failure.FieldPaths, policy.FieldPath) {
		return ErrValidation
	}
	if !strings.HasPrefix(policy.FieldPath, "/") || strings.Contains(strings.TrimPrefix(policy.FieldPath, "/"), "/") {
		return ErrValidation
	}
	field := strings.TrimPrefix(policy.FieldPath, "/")
	var previous map[string]any
	if json.Unmarshal(oldInput, &previous) != nil {
		return ErrValidation
	}
	previousComparable, nextComparable := cloneAnyMap(previous), cloneAnyMap(newInput)
	delete(previousComparable, field)
	delete(nextComparable, field)
	if !reflect.DeepEqual(previousComparable, nextComparable) {
		return ErrValidation
	}
	switch operatorID {
	case "remove_optional_filename":
		if _, exists := newInput[field]; exists {
			return ErrValidation
		}
	case "filename.safe_basename":
		value, ok := newInput[field].(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" || strings.ContainsAny(value, "/\\") || value == "." || value == ".." || len([]byte(value)) > 240 {
			return ErrValidation
		}
		if policy.Extension != "" && !strings.HasSuffix(strings.ToLower(value), strings.ToLower(policy.Extension)) {
			return ErrValidation
		}
	default:
		return ErrValidation
	}
	if err := validateObject(newInput, manifest.InputSchema); err != nil {
		return fmt.Errorf("%w: repaired input: %v", ErrValidation, err)
	}
	return nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) Get(ctx context.Context, userID, runID string) (Run, error) {
	return s.store.GetSkillRun(ctx, userID, runID)
}

func (s *Service) List(ctx context.Context, userID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.store.ListSkillRuns(ctx, userID, limit)
}

func (s *Service) ListForConversation(ctx context.Context, userID, conversationID string, limit int) ([]Run, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return s.List(ctx, userID, limit)
	}
	items, err := s.store.ListSkillRuns(ctx, userID, 200)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	filtered := make([]Run, 0, min(limit, len(items)))
	for _, item := range items {
		if item.ConversationID == conversationID {
			filtered = append(filtered, item)
			if len(filtered) == limit {
				break
			}
		}
	}
	return filtered, nil
}

func (s *Service) Download(ctx context.Context, userID, runID, fileID string) (GeneratedFile, []byte, error) {
	run, err := s.store.GetSkillRun(ctx, userID, runID)
	if err != nil {
		return GeneratedFile{}, nil, err
	}
	for _, file := range run.Files {
		if file.ID == fileID {
			data, readErr := s.files.Get(ctx, file.StorageKey)
			return file, data, readErr
		}
	}
	return GeneratedFile{}, nil, ErrNotFound
}

func (s *Service) ShareFileWithWorkspace(ctx context.Context, userID, workspaceID, runID, fileID string) error {
	workspaceID = strings.TrimSpace(workspaceID)
	runID = strings.TrimSpace(runID)
	fileID = strings.TrimSpace(fileID)
	if workspaceID == "" || runID == "" || fileID == "" {
		return ErrValidation
	}
	return s.store.ShareGeneratedFileWithWorkspace(ctx, userID, workspaceID, runID, fileID, s.now().UTC())
}

func (s *Service) ListWorkspaceFiles(ctx context.Context, workspaceID string) ([]GeneratedFile, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrValidation
	}
	return s.store.ListWorkspaceGeneratedFiles(ctx, workspaceID, 200)
}

func (s *Service) DownloadWorkspaceFile(ctx context.Context, workspaceID, fileID string) (GeneratedFile, []byte, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	fileID = strings.TrimSpace(fileID)
	if workspaceID == "" || fileID == "" {
		return GeneratedFile{}, nil, ErrValidation
	}
	file, err := s.store.GetWorkspaceGeneratedFile(ctx, workspaceID, fileID)
	if err != nil {
		return GeneratedFile{}, nil, err
	}
	data, err := s.files.Get(ctx, file.StorageKey)
	return file, data, err
}

func (s *Service) execute(ctx context.Context, run Run, definition Definition) (Run, error) {
	run.Output = nil
	if len(run.Steps)+2 > run.MaxSteps {
		return s.fail(ctx, run, definition.Manifest.ToolName, "step_budget_exceeded", errors.New("skill step budget exceeded"))
	}
	var input map[string]any
	if err := json.Unmarshal(run.Input, &input); err != nil {
		return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_input", err)
	}
	timeout := time.Duration(run.TimeoutMS) * time.Millisecond
	executeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := s.now().UTC()
	result, executeErr := definition.Handler.Execute(executeCtx, input)
	if executeErr != nil {
		if ctx.Err() != nil {
			if run.WorkerID == "" {
				code := "tool_cancelled"
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					code = "tool_timeout"
				}
				return s.fail(context.Background(), run, definition.Manifest.ToolName, code, executeErr)
			}
			return Run{}, ctx.Err()
		}
		code := "tool_failed"
		if errors.Is(executeCtx.Err(), context.DeadlineExceeded) {
			code = "tool_timeout"
		}
		if failure := toolFailureFromError(executeErr, code); failure.Code != "" {
			code = failure.Code
		}
		return s.fail(ctx, run, definition.Manifest.ToolName, code, executeErr)
	}
	persistModelAuditOutput(&run, result.Output)
	if run.MaxCostMicros > 0 {
		cost, ok := modelCostMicros(result.Output)
		if !ok {
			return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_model_usage", errors.New("model usage cost is required by the skill budget"))
		}
		if cost > run.MaxCostMicros {
			return s.fail(ctx, run, definition.Manifest.ToolName, "model_cost_exceeded", fmt.Errorf("model cost %d exceeds skill budget %d", cost, run.MaxCostMicros))
		}
	}
	if code, ok := modelOperationError(result.Output); ok {
		return s.fail(ctx, run, definition.Manifest.ToolName, "model_operation_failed", errors.New(code))
	}
	if err := validateObject(result.Output, definition.Manifest.OutputSchema); err != nil {
		return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_output", err)
	}
	output, err := json.Marshal(result.Output)
	if err != nil {
		return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_output", err)
	}
	now := s.now().UTC()
	newFiles := make([]GeneratedFile, 0, len(result.Files))
	for _, item := range result.Files {
		if len([]rune(item.Name)) == 0 || len([]rune(item.Name)) > 255 || item.MediaType == "" {
			return s.fail(ctx, run, definition.Manifest.ToolName, "invalid_file", errors.New("generated file metadata is invalid"))
		}
		if len(item.Data) > definition.Manifest.MaxOutputFileBytes {
			failure := ToolFailure{
				Code: "output_file_too_large", Category: "resource_limit", Phase: "post_execution",
				Message: "生成文件超过该技能允许的大小。", SideEffectState: "unknown",
				SafeDetails: map[string]any{
					"actual_bytes": len(item.Data), "limit_bytes": definition.Manifest.MaxOutputFileBytes,
				},
			}
			return s.fail(ctx, run, definition.Manifest.ToolName, failure.Code, NewToolExecutionError(failure, errors.New("generated file exceeds skill output budget")))
		}
		fileID, idErr := id.New()
		if idErr != nil {
			return s.fail(ctx, run, definition.Manifest.ToolName, "file_id_failed", idErr)
		}
		storageKey, writeErr := s.files.Put(ctx, run.UserID, run.ID, fileID, item.Data)
		if writeErr != nil {
			return s.fail(ctx, run, definition.Manifest.ToolName, "file_write_failed", writeErr)
		}
		digest := sha256.Sum256(item.Data)
		newFiles = append(newFiles, GeneratedFile{ID: fileID, RunID: run.ID, Name: item.Name, MediaType: item.MediaType, SizeBytes: int64(len(item.Data)), SHA256: hex.EncodeToString(digest[:]), StorageKey: storageKey, CreatedAt: now})
	}
	expected := run.Revision
	run.Output, run.Status, run.CurrentState = output, "succeeded", "deliver"
	run.WorkerID, run.LeaseExpiresAt = "", nil
	run.ErrorCode, run.ErrorMessage, run.UpdatedAt, run.CompletedAt = "", "", now, &now
	executeStep, _ := completedStep(len(run.Steps)+1, "execute", definition.Manifest.ToolName, run.Input, output, started)
	executeStep.CompletedAt = &now
	deliverStep, _ := completedStep(len(run.Steps)+2, "deliver", "", nil, output, now)
	newSteps := []Step{executeStep, deliverStep}
	run.Steps = append(run.Steps, newSteps...)
	run.Files = append(run.Files, newFiles...)
	run.DeliveryContent = retryDeliveryContent(run)
	run.Revision++
	if err = s.store.SaveSkillRun(ctx, run, expected, newSteps, newFiles); err != nil {
		for _, file := range newFiles {
			_ = s.files.Delete(ctx, file.StorageKey)
		}
		return Run{}, err
	}
	return run, nil
}

func persistModelAuditOutput(run *Run, output map[string]any) {
	usage, hasUsage := output["model_usage"]
	if !hasUsage {
		return
	}
	audit := map[string]any{"model_usage": usage}
	if operationError, ok := output["operation_error"]; ok {
		audit["operation_error"] = operationError
	}
	encoded, err := json.Marshal(audit)
	if err == nil {
		run.Output = encoded
	}
}

func modelOperationError(output map[string]any) (string, bool) {
	operationError, ok := output["operation_error"].(map[string]any)
	if !ok {
		return "", false
	}
	code, ok := operationError["code"].(string)
	code = strings.TrimSpace(code)
	if !ok || code == "" || len(code) > 128 {
		return "invalid_operation_error", true
	}
	return code, true
}

func modelCostMicros(output map[string]any) (int64, bool) {
	usage, ok := output["model_usage"].(map[string]any)
	if !ok {
		return 0, false
	}
	switch value := usage["cost_micros"].(type) {
	case int:
		return int64(value), value >= 0
	case int64:
		return value, value >= 0
	case float64:
		if value < 0 || value > math.MaxInt64 || value != math.Trunc(value) {
			return 0, false
		}
		return int64(value), true
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil && parsed >= 0
	default:
		return 0, false
	}
}

func (s *Service) fail(ctx context.Context, run Run, toolName, code string, cause error) (Run, error) {
	expected := run.Revision
	now := s.now().UTC()
	failure := toolFailureFromError(cause, code)
	run.Status, run.CurrentState, run.ErrorCode, run.ErrorMessage = "failed", "failed", failure.Code, failure.Message
	if strings.TrimSpace(run.ErrorMessage) == "" {
		run.ErrorMessage = "工具执行失败。"
	}
	run.Output = failureOutput(run.Output, failure)
	run.WorkerID, run.LeaseExpiresAt = "", nil
	run.UpdatedAt, run.CompletedAt = now, &now
	run.DeliveryContent = retryDeliveryContent(run)
	newSteps := make([]Step, 0, 1)
	if len(run.Steps) < run.MaxSteps {
		stepID, _ := id.New()
		step := Step{ID: stepID, Sequence: len(run.Steps) + 1, State: "execute", Status: "failed", ToolName: toolName, Input: run.Input, Output: run.Output, ErrorCode: failure.Code, ErrorMessage: run.ErrorMessage, StartedAt: now, CompletedAt: &now}
		run.Steps = append(run.Steps, step)
		newSteps = append(newSteps, step)
	}
	run.Revision++
	if err := s.store.SaveSkillRun(ctx, run, expected, newSteps, nil); err != nil {
		return Run{}, err
	}
	return run, nil
}

func retryDeliveryContent(run Run) string {
	if run.Attempt < 2 || run.ConversationID == "" || run.OriginMessageID == "" {
		return ""
	}
	marker := chatattachment.SkillRunMarker(run.ID, run.Attempt, run.Status)
	if run.Status == "failed" || run.Status == "cancelled" {
		message := "任务重试失败：" + run.SkillName
		if detail := strings.TrimSpace(run.ErrorMessage); detail != "" {
			message += "\n" + detail
		}
		return strings.TrimSpace(message + "\n" + marker)
	}
	if run.Status != "succeeded" {
		return ""
	}
	lines := []string{"任务重试已完成：" + run.SkillName}
	var output map[string]any
	if len(run.Output) > 0 && json.Unmarshal(run.Output, &output) == nil {
		switch {
		case strings.TrimSpace(fmt.Sprint(output["translated_text"])) != "<nil>" && strings.TrimSpace(fmt.Sprint(output["translated_text"])) != "":
			lines = append(lines, strings.TrimSpace(fmt.Sprint(output["translated_text"])))
		case strings.TrimSpace(fmt.Sprint(output["body"])) != "<nil>" && strings.TrimSpace(fmt.Sprint(output["body"])) != "":
			subject := strings.TrimSpace(fmt.Sprint(output["subject"]))
			if subject != "" && subject != "<nil>" {
				lines = append(lines, "主题："+subject)
			}
			lines = append(lines, strings.TrimSpace(fmt.Sprint(output["body"])))
		default:
			if encoded, err := json.MarshalIndent(output, "", "  "); err == nil {
				lines = append(lines, truncateDelivery(string(encoded), 6000))
			}
		}
	}
	for _, file := range run.Files {
		lines = append(lines, chatattachment.GeneratedFileMarker(run.ID, file.ID, file.Name))
	}
	lines = append(lines, marker)
	return strings.TrimSpace(strings.Join(lines, "\n\n"))
}

func truncateDelivery(value string, limit int) string {
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit]) + "\n…"
}

func completedStep(sequence int, state, tool string, input, output json.RawMessage, now time.Time) (Step, error) {
	stepID, err := id.New()
	if err != nil {
		return Step{}, err
	}
	completed := now
	return Step{ID: stepID, Sequence: sequence, State: state, Status: "succeeded", ToolName: tool, Input: input, Output: output, StartedAt: now, CompletedAt: &completed}, nil
}

func isTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func validateObject(input map[string]any, schema map[string]any) error {
	if schema == nil {
		return nil
	}
	required, _ := schema["required"].([]string)
	if required == nil {
		if raw, ok := schema["required"].([]any); ok {
			for _, value := range raw {
				if text, ok := value.(string); ok {
					required = append(required, text)
				}
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for _, field := range required {
		value, exists := input[field]
		if !exists || value == nil || (fmt.Sprint(value) == "" && isStringProperty(properties[field])) {
			return fmt.Errorf("%w: %s is required", ErrValidation, field)
		}
	}
	for field, value := range input {
		property, exists := properties[field]
		if !exists {
			if additional, explicit := schema["additionalProperties"].(bool); explicit && !additional {
				return fmt.Errorf("%w: unknown field %s", ErrValidation, field)
			}
			continue
		}
		if value == nil {
			continue
		}
		definition, _ := property.(map[string]any)
		switch definition["type"] {
		case "string":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%w: %s must be a string", ErrValidation, field)
			}
		case "array":
			kind := reflect.TypeOf(value).Kind()
			if kind != reflect.Array && kind != reflect.Slice {
				return fmt.Errorf("%w: %s must be an array", ErrValidation, field)
			}
		case "integer":
			number, ok := value.(float64)
			if !ok || number != float64(int64(number)) {
				return fmt.Errorf("%w: %s must be an integer", ErrValidation, field)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%w: %s must be a boolean", ErrValidation, field)
			}
		}
	}
	return nil
}

func isStringProperty(value any) bool {
	definition, _ := value.(map[string]any)
	return definition["type"] == "string"
}
