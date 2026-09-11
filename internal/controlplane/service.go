package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound   = errors.New("configuration not found")
	ErrConflict   = errors.New("configuration conflict")
	ErrValidation = errors.New("configuration validation failed")
	ErrApproval   = errors.New("configuration approval separation required")
	ErrEvaluation = errors.New("passing Agent evaluation required")
	ErrRollout    = errors.New("passing Agent rollout required")
)

const (
	KindBillingPlan     = "billing_plan"
	KindModelProfile    = "model_profile"
	KindAgentDefinition = "agent_definition"
	KindPrompt          = "prompt"

	StatusDraft      = "draft"
	StatusValidated  = "validated"
	StatusSubmitted  = "submitted"
	StatusPublished  = "published"
	StatusSuperseded = "superseded"
)

type Version struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Key           string          `json:"key"`
	Version       int             `json:"version"`
	BaseVersion   int             `json:"base_version"`
	SchemaVersion string          `json:"schema_version"`
	Status        string          `json:"status"`
	Payload       json.RawMessage `json:"payload"`
	Fingerprint   string          `json:"fingerprint"`
	Reason        string          `json:"reason"`
	CreatedBy     string          `json:"created_by"`
	ValidatedBy   string          `json:"validated_by,omitempty"`
	SubmittedBy   string          `json:"submitted_by,omitempty"`
	PublishedBy   string          `json:"published_by,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
	ValidatedAt   *time.Time      `json:"validated_at,omitempty"`
	SubmittedAt   *time.Time      `json:"submitted_at,omitempty"`
	PublishedAt   *time.Time      `json:"published_at,omitempty"`
}

type Deployment struct {
	Environment string    `json:"environment"`
	Kind        string    `json:"kind"`
	Key         string    `json:"key"`
	Revision    int       `json:"revision"`
	DeployedBy  string    `json:"deployed_by"`
	DeployedAt  time.Time `json:"deployed_at"`
	Version     Version   `json:"version"`
}

type ProviderConnection struct {
	Key           string          `json:"key"`
	ProviderType  string          `json:"provider_type"`
	DisplayName   string          `json:"display_name"`
	BaseURL       string          `json:"base_url"`
	CredentialRef string          `json:"credential_ref"`
	Status        string          `json:"status"`
	Capabilities  json.RawMessage `json:"capabilities"`
}

type ModelCatalogEntry struct {
	ModelID         string          `json:"model_id"`
	ProviderKey     string          `json:"provider_key"`
	DisplayName     string          `json:"display_name"`
	ModelClass      string          `json:"model_class"`
	ContextWindow   int             `json:"context_window"`
	MaxOutputTokens int             `json:"max_output_tokens"`
	PromptPrice     float64         `json:"prompt_price"`
	CompletionPrice float64         `json:"completion_price"`
	ZeroPrice       bool            `json:"zero_price"`
	Status          string          `json:"status"`
	Capabilities    json.RawMessage `json:"capabilities"`
}

type CreateInput struct {
	Kind        string
	Key         string
	BaseVersion int
	Payload     json.RawMessage
	Reason      string
	Actor       string
}

type Store interface {
	ListConfigVersions(context.Context, string, string, int) ([]Version, error)
	GetConfigVersion(context.Context, string) (Version, error)
	CreateConfigVersion(context.Context, Version) (Version, error)
	TransitionConfigVersion(context.Context, string, string, string, string, time.Time) (Version, error)
	PublishConfigVersion(context.Context, string, string, string, time.Time) (Version, Deployment, error)
	RollbackConfigDeployment(context.Context, string, string, int, string, string, string, time.Time) (Deployment, error)
	ListActiveConfigDeployments(context.Context, string, string) ([]Deployment, error)
	ListProviderConnections(context.Context) ([]ProviderConnection, error)
	ListModelCatalog(context.Context) ([]ModelCatalogEntry, error)
	CreateAgentEvaluationRun(context.Context, AgentEvaluationRun) (AgentEvaluationRun, error)
	CompleteAgentEvaluationRun(context.Context, string, string, string, AgentEvaluationSummary, []AgentEvaluationCaseResult, time.Time) (AgentEvaluationRun, error)
	GetAgentEvaluationRun(context.Context, string) (AgentEvaluationRun, error)
	ListAgentEvaluationRuns(context.Context, string, int) ([]AgentEvaluationRun, error)
	LatestPassingAgentEvaluationRun(context.Context, string, string) (AgentEvaluationRun, error)
	CreateAgentRollout(context.Context, AgentRollout) (AgentRollout, error)
	GetAgentRollout(context.Context, string) (AgentRollout, error)
	ListAgentRollouts(context.Context, string, int) ([]AgentRollout, error)
	ActiveAgentRollout(context.Context, string) (AgentRollout, error)
	LatestAgentRolloutForVersion(context.Context, string, string) (AgentRollout, error)
	MeasureAgentRollout(context.Context, AgentRollout, time.Time, time.Time) (AgentRolloutMeasurements, error)
	UpdateAgentRollout(context.Context, string, int, string, string, AgentRolloutSummary, []AgentRolloutViolation, string, string, time.Time) (AgentRollout, error)
	PromoteAgentRollout(context.Context, string, int, string, string, time.Time) (Version, Deployment, AgentRollout, error)
}

type Service struct {
	store       Store
	environment string
	now         func() time.Time
}

func NewService(store Store, environment string) *Service {
	environment = strings.ToLower(strings.TrimSpace(environment))
	if environment == "" {
		environment = "development"
	}
	return &Service{store: store, environment: environment, now: time.Now}
}

func (s *Service) List(ctx context.Context, kind, key string, limit int) ([]Version, error) {
	kind, key = normalizeKind(kind), normalizeKey(key)
	if kind == "" || (key != "" && !validKey(key)) {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return s.store.ListConfigVersions(ctx, kind, key, limit)
}

func (s *Service) Get(ctx context.Context, id string) (Version, error) {
	if strings.TrimSpace(id) == "" {
		return Version{}, ErrValidation
	}
	return s.store.GetConfigVersion(ctx, strings.TrimSpace(id))
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Version, error) {
	input.Kind, input.Key = normalizeKind(input.Kind), normalizeKey(input.Key)
	input.Actor, input.Reason = strings.TrimSpace(input.Actor), strings.TrimSpace(input.Reason)
	if input.Kind == "" || !validKey(input.Key) || input.BaseVersion < 0 || input.Actor == "" || input.Reason == "" || len([]rune(input.Reason)) > 512 {
		return Version{}, ErrValidation
	}
	payload, err := s.validatePayload(ctx, input.Kind, input.Key, input.Payload)
	if err != nil {
		return Version{}, err
	}
	versionID, err := id.New()
	if err != nil {
		return Version{}, err
	}
	now := s.now().UTC()
	item := Version{
		ID: versionID, Kind: input.Kind, Key: input.Key, BaseVersion: input.BaseVersion,
		SchemaVersion: schemaVersion(input.Kind), Status: StatusDraft, Payload: payload,
		Fingerprint: fingerprint(input.Kind, input.Key, payload), Reason: input.Reason,
		CreatedBy: input.Actor, CreatedAt: now, UpdatedAt: now,
	}
	return s.store.CreateConfigVersion(ctx, item)
}

func (s *Service) Validate(ctx context.Context, versionID, actor string) (Version, error) {
	item, err := s.Get(ctx, versionID)
	if err != nil {
		return Version{}, err
	}
	if item.Status != StatusDraft {
		return Version{}, ErrConflict
	}
	if _, err = s.validatePayload(ctx, item.Kind, item.Key, item.Payload); err != nil {
		return Version{}, err
	}
	return s.store.TransitionConfigVersion(ctx, item.ID, StatusDraft, StatusValidated, strings.TrimSpace(actor), s.now().UTC())
}

func (s *Service) Submit(ctx context.Context, versionID, actor string) (Version, error) {
	if strings.TrimSpace(actor) == "" {
		return Version{}, ErrValidation
	}
	item, err := s.Get(ctx, versionID)
	if err != nil {
		return Version{}, err
	}
	if item.Status != StatusValidated {
		return Version{}, ErrConflict
	}
	if item.Kind == KindAgentDefinition {
		if _, err = s.store.LatestPassingAgentEvaluationRun(ctx, item.ID, item.Fingerprint); err != nil {
			if errors.Is(err, ErrNotFound) {
				return Version{}, ErrEvaluation
			}
			return Version{}, err
		}
	}
	return s.store.TransitionConfigVersion(ctx, item.ID, StatusValidated, StatusSubmitted, strings.TrimSpace(actor), s.now().UTC())
}

func (s *Service) Publish(ctx context.Context, versionID, actor string) (Version, Deployment, error) {
	actor = strings.TrimSpace(actor)
	item, err := s.Get(ctx, versionID)
	if err != nil {
		return Version{}, Deployment{}, err
	}
	if item.Status != StatusSubmitted {
		return Version{}, Deployment{}, ErrConflict
	}
	if actor == "" || actor == item.CreatedBy || actor == item.SubmittedBy {
		return Version{}, Deployment{}, ErrApproval
	}
	if _, err = s.validatePayload(ctx, item.Kind, item.Key, item.Payload); err != nil {
		return Version{}, Deployment{}, err
	}
	var rollout AgentRollout
	if item.Kind == KindAgentDefinition {
		if _, err = s.store.LatestPassingAgentEvaluationRun(ctx, item.ID, item.Fingerprint); err != nil {
			if errors.Is(err, ErrNotFound) {
				return Version{}, Deployment{}, ErrEvaluation
			}
			return Version{}, Deployment{}, err
		}
		if err = s.validateAgentActivation(ctx, item); err != nil {
			return Version{}, Deployment{}, err
		}
		deployments, activeErr := s.Active(ctx, KindAgentDefinition)
		if activeErr != nil {
			return Version{}, Deployment{}, activeErr
		}
		for _, deployment := range deployments {
			if deployment.Key != item.Key || deployment.Version.ID == item.ID {
				continue
			}
			rollout, err = s.store.LatestAgentRolloutForVersion(ctx, item.ID, item.Fingerprint)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					return Version{}, Deployment{}, ErrRollout
				}
				return Version{}, Deployment{}, err
			}
			if rollout.Status != AgentRolloutStatusReady || rollout.Decision != AgentRolloutDecisionPass {
				return Version{}, Deployment{}, ErrRollout
			}
			break
		}
	}
	if rollout.ID != "" {
		published, deployment, _, promoteErr := s.store.PromoteAgentRollout(
			ctx, rollout.ID, rollout.Revision, s.environment, actor, s.now().UTC(),
		)
		return published, deployment, promoteErr
	}
	return s.store.PublishConfigVersion(ctx, item.ID, s.environment, actor, s.now().UTC())
}

func (s *Service) validateAgentActivation(ctx context.Context, item Version) error {
	var candidate AgentDefinitionPayload
	if err := json.Unmarshal(item.Payload, &candidate); err != nil {
		return validationError("decode Agent definition for publish: %v", err)
	}
	if _, err := s.ActiveModelRuntime(ctx, candidate.ModelProfile); err != nil {
		return validationError("Agent model_profile %s is not an active deployment", candidate.ModelProfile)
	}
	deployments, err := s.Active(ctx, KindAgentDefinition)
	if err != nil {
		return err
	}
	for _, deployment := range deployments {
		if deployment.Key == item.Key {
			continue
		}
		var active AgentDefinitionPayload
		if err = json.Unmarshal(deployment.Version.Payload, &active); err != nil {
			return validationError("decode active Agent definition: %v", err)
		}
		for _, module := range candidate.Modules {
			if containsAgentModule(active.Modules, module) {
				return validationError(
					"module %s is already assigned to active Agent %s", module, deployment.Key,
				)
			}
		}
	}
	return nil
}

func (s *Service) Rollback(ctx context.Context, kind, key string, targetVersion int, actor, reason string) (Deployment, error) {
	kind, key, actor, reason = normalizeKind(kind), normalizeKey(key), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if kind == "" || !validKey(key) || targetVersion <= 0 || actor == "" || reason == "" || len([]rune(reason)) > 512 {
		return Deployment{}, ErrValidation
	}
	if kind == KindAgentDefinition {
		if rollout, rolloutErr := s.store.ActiveAgentRollout(ctx, s.environment); rolloutErr == nil && rollout.AgentKey == key {
			return Deployment{}, ErrConflict
		} else if rolloutErr != nil && !errors.Is(rolloutErr, ErrNotFound) {
			return Deployment{}, rolloutErr
		}
	}
	versions, err := s.store.ListConfigVersions(ctx, kind, key, 200)
	if err != nil {
		return Deployment{}, err
	}
	for _, item := range versions {
		if item.Version == targetVersion {
			if item.Status != StatusPublished && item.Status != StatusSuperseded {
				return Deployment{}, ErrConflict
			}
			if item.CreatedBy == actor || item.PublishedBy == actor {
				return Deployment{}, ErrApproval
			}
			if item.Kind == KindAgentDefinition {
				if err = s.validateAgentActivation(ctx, item); err != nil {
					return Deployment{}, err
				}
			}
			return s.store.RollbackConfigDeployment(ctx, kind, key, targetVersion, s.environment, actor, reason, s.now().UTC())
		}
	}
	return Deployment{}, ErrNotFound
}

func (s *Service) Active(ctx context.Context, kind string) ([]Deployment, error) {
	kind = normalizeKind(kind)
	if kind == "" {
		return nil, ErrValidation
	}
	return s.store.ListActiveConfigDeployments(ctx, kind, s.environment)
}

func (s *Service) Providers(ctx context.Context) ([]ProviderConnection, error) {
	return s.store.ListProviderConnections(ctx)
}

func (s *Service) Models(ctx context.Context) ([]ModelCatalogEntry, error) {
	return s.store.ListModelCatalog(ctx)
}

func (s *Service) validatePayload(ctx context.Context, kind, key string, raw json.RawMessage) (json.RawMessage, error) {
	switch kind {
	case KindBillingPlan:
		return validateBillingPlan(key, raw)
	case KindModelProfile:
		providers, err := s.store.ListProviderConnections(ctx)
		if err != nil {
			return nil, err
		}
		models, err := s.store.ListModelCatalog(ctx)
		if err != nil {
			return nil, err
		}
		return validateModelProfile(raw, providers, models, s.environment == "production")
	case KindAgentDefinition:
		return s.resolveAgentDefinitionPrompts(ctx, raw)
	case KindPrompt:
		return validatePrompt(raw)
	default:
		return nil, ErrValidation
	}
}

// CompileAgentDefinition resolves active prompt references and then performs
// the same normalization and graph checks used by persisted Agent versions.
func (s *Service) CompileAgentDefinition(ctx context.Context, key string, raw json.RawMessage) (AgentDefinitionCompilation, error) {
	resolved, err := s.resolveAgentDefinitionPrompts(ctx, raw)
	if err != nil {
		return AgentDefinitionCompilation{}, err
	}
	return CompileAgentDefinition(key, resolved)
}

func (s *Service) resolveAgentDefinitionPrompts(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var definition AgentDefinitionPayload
	if err := decodeStrict(raw, &definition); err != nil {
		return nil, validationError("invalid Agent definition payload: %v", err)
	}
	var active []Deployment
	for index := range definition.Nodes {
		node := &definition.Nodes[index]
		if node.Prompt == nil {
			continue
		}
		promptKey := normalizeKey(node.Prompt.Key)
		if !validKey(promptKey) {
			return nil, validationError("Agent node %s references an invalid prompt key", node.Key)
		}
		var promptVersion Version
		if strings.TrimSpace(node.Prompt.VersionID) == "" && node.Prompt.Version == 0 && strings.TrimSpace(node.Prompt.Fingerprint) == "" {
			if active == nil {
				var err error
				active, err = s.Active(ctx, KindPrompt)
				if err != nil {
					return nil, err
				}
			}
			for _, deployment := range active {
				if deployment.Key == promptKey {
					promptVersion = deployment.Version
					break
				}
			}
			if promptVersion.ID == "" {
				return nil, validationError("Agent node %s references prompt %s without an active deployment", node.Key, promptKey)
			}
		} else {
			var err error
			promptVersion, err = s.Get(ctx, strings.TrimSpace(node.Prompt.VersionID))
			if err != nil {
				return nil, err
			}
			if promptVersion.Kind != KindPrompt || promptVersion.Key != promptKey || promptVersion.Version != node.Prompt.Version ||
				promptVersion.Fingerprint != strings.ToLower(strings.TrimSpace(node.Prompt.Fingerprint)) ||
				(promptVersion.Status != StatusPublished && promptVersion.Status != StatusSuperseded) {
				return nil, validationError("Agent node %s prompt reference does not match an immutable published version", node.Key)
			}
		}
		var prompt PromptPayload
		if err := json.Unmarshal(promptVersion.Payload, &prompt); err != nil {
			return nil, validationError("decode prompt %s: %v", promptKey, err)
		}
		node.Prompt = &AgentPromptReference{
			Key: promptKey, VersionID: promptVersion.ID, Version: promptVersion.Version, Fingerprint: promptVersion.Fingerprint,
		}
		node.PromptTemplate = prompt.Template
	}
	resolved, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	return validateAgentDefinition(resolved)
}

func BillingPlans(deployments []Deployment) ([]billing.Plan, error) {
	plans := make([]billing.Plan, 0, len(deployments))
	for _, deployment := range deployments {
		if deployment.Kind != KindBillingPlan {
			continue
		}
		var payload BillingPlanPayload
		if err := json.Unmarshal(deployment.Version.Payload, &payload); err != nil {
			return nil, err
		}
		limits := map[string]int64{}
		for _, limit := range payload.Limits {
			limits[limit.Resource] = limit.Limit
		}
		agentRuns, modelCost := int64(-1), int64(-1)
		if value, exists := limits["agent_runs_monthly"]; exists {
			agentRuns = value
		}
		if value, exists := limits["model_cost_micros_monthly"]; exists {
			modelCost = value
		}
		plans = append(plans, billing.Plan{
			Code: deployment.Key, DisplayName: payload.DisplayName, Status: payload.Status,
			Limits: billing.Limits{
				Documents: int(limits["documents_active"]), SkillRunsPerMonth: int(limits["skill_runs_monthly"]), Workspaces: int(limits["workspaces"]),
				AgentRunsPerMonth: int(agentRuns), ModelCostMicrosMonthly: int(modelCost),
			}, UpdatedAt: deployment.DeployedAt,
		})
	}
	return plans, nil
}

func normalizeKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value != KindBillingPlan && value != KindModelProfile && value != KindAgentDefinition && value != KindPrompt {
		return ""
	}
	return value
}

func normalizeKey(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func validKey(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func schemaVersion(kind string) string {
	if kind == KindBillingPlan {
		return "billing-plan-v1"
	}
	if kind == KindAgentDefinition {
		return "agent-definition-v1"
	}
	if kind == KindPrompt {
		return "prompt-v1"
	}
	return "model-profile-v1"
}

func fingerprint(kind, key string, payload []byte) string {
	digest := sha256.Sum256(append([]byte(kind+":"+key+":"), payload...))
	return hex.EncodeToString(digest[:])
}

func validationError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}
