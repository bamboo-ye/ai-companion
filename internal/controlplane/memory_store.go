package controlplane

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu          sync.RWMutex
	versions    map[string]Version
	deployments map[string]Deployment
	evaluations map[string]AgentEvaluationRun
	rollouts    map[string]AgentRollout
	rolloutData map[string]AgentRolloutMeasurements
	providers   []ProviderConnection
	models      []ModelCatalogEntry
	runtime     map[string]RuntimeConfigReport
}

func NewMemoryStore() *MemoryStore {
	store := &MemoryStore{
		versions: map[string]Version{}, deployments: map[string]Deployment{}, evaluations: map[string]AgentEvaluationRun{},
		rollouts: map[string]AgentRollout{}, rolloutData: map[string]AgentRolloutMeasurements{}, runtime: map[string]RuntimeConfigReport{},
		providers: []ProviderConnection{{
			Key: "openrouter-production", ProviderType: "openrouter", DisplayName: "OpenRouter Production",
			BaseURL: "https://openrouter.ai/api/v1", CredentialRef: "env:MODEL_API_KEY", Status: "active", Capabilities: json.RawMessage(`{}`),
		}},
		models: []ModelCatalogEntry{
			{ModelID: "deepseek/deepseek-v4-flash-0731", ProviderKey: "openrouter-production", DisplayName: "DeepSeek V4 Flash 0731", ModelClass: "standard", ContextWindow: 131072, MaxOutputTokens: 32768, PromptPrice: .3, CompletionPrice: 2.5, Status: "approved", Capabilities: json.RawMessage(`{}`)},
			{ModelID: "openai/gpt-5-mini", ProviderKey: "openrouter-production", DisplayName: "OpenAI GPT-5 mini", ModelClass: "quality", ContextWindow: 400000, MaxOutputTokens: 32768, PromptPrice: .3, CompletionPrice: 2.5, Status: "approved", Capabilities: json.RawMessage(`{}`)},
			{ModelID: "openai/gpt-oss-20b:free", ProviderKey: "openrouter-production", DisplayName: "GPT OSS 20B Free", ModelClass: "free", ContextWindow: 131072, MaxOutputTokens: 32768, ZeroPrice: true, Status: "approved", Capabilities: json.RawMessage(`{}`)},
			{ModelID: "google/gemma-4-31b-it:free", ProviderKey: "openrouter-production", DisplayName: "Gemma 4 31B IT Free", ModelClass: "free", ContextWindow: 131072, MaxOutputTokens: 32768, ZeroPrice: true, Status: "approved", Capabilities: json.RawMessage(`{}`)},
		},
	}
	seedMemoryConfiguration(store)
	return store
}

func (s *MemoryStore) UpsertRuntimeConfigReport(_ context.Context, item RuntimeConfigReport) (RuntimeConfigReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := item.InstanceID + "\x00" + item.Kind + "\x00" + item.Key
	if current, exists := s.runtime[key]; exists {
		item.StartedAt = current.StartedAt
	}
	s.runtime[key] = item
	return item, nil
}

func (s *MemoryStore) ListRuntimeConfigReports(_ context.Context, environment string, since time.Time, limit int) ([]RuntimeConfigReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]RuntimeConfigReport, 0)
	for _, item := range s.runtime {
		if item.Environment == environment && !item.LastSeenAt.Before(since) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Service != items[j].Service {
			return items[i].Service < items[j].Service
		}
		if items[i].InstanceID != items[j].InstanceID {
			return items[i].InstanceID < items[j].InstanceID
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Key < items[j].Key
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) ListConfigVersions(_ context.Context, kind, key string, limit int) ([]Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Version, 0)
	for _, item := range s.versions {
		if item.Kind == kind && (key == "" || item.Key == key) {
			items = append(items, cloneVersion(item))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Key == items[j].Key {
			return items[i].Version > items[j].Version
		}
		return items[i].Key < items[j].Key
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) GetConfigVersion(_ context.Context, id string) (Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.versions[id]
	if !ok {
		return Version{}, ErrNotFound
	}
	return cloneVersion(item), nil
}

func (s *MemoryStore) CreateConfigVersion(_ context.Context, item Version) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := 0
	for _, existing := range s.versions {
		if existing.Kind == item.Kind && existing.Key == item.Key && existing.Version > latest {
			latest = existing.Version
		}
	}
	if latest != item.BaseVersion {
		return Version{}, ErrConflict
	}
	item.Version = latest + 1
	s.versions[item.ID] = cloneVersion(item)
	return cloneVersion(item), nil
}

func (s *MemoryStore) TransitionConfigVersion(_ context.Context, id, from, to, actor string, now time.Time) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.versions[id]
	if !ok {
		return Version{}, ErrNotFound
	}
	if item.Status != from || actor == "" {
		return Version{}, ErrConflict
	}
	item.Status, item.UpdatedAt = to, now
	switch to {
	case StatusValidated:
		item.ValidatedBy, item.ValidatedAt = actor, timePointer(now)
	case StatusSubmitted:
		item.SubmittedBy, item.SubmittedAt = actor, timePointer(now)
	}
	s.versions[id] = cloneVersion(item)
	return cloneVersion(item), nil
}

func (s *MemoryStore) PublishConfigVersion(_ context.Context, id, environment, actor string, now time.Time) (Version, Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.versions[id]
	if !ok {
		return Version{}, Deployment{}, ErrNotFound
	}
	if item.Status != StatusSubmitted || actor == item.CreatedBy || actor == item.SubmittedBy {
		return Version{}, Deployment{}, ErrConflict
	}
	key := deploymentKey(environment, item.Kind, item.Key)
	current, exists := s.deployments[key]
	if exists {
		previous := s.versions[current.Version.ID]
		previous.Status, previous.UpdatedAt = StatusSuperseded, now
		s.versions[previous.ID] = previous
	}
	item.Status, item.PublishedBy, item.PublishedAt, item.UpdatedAt = StatusPublished, actor, timePointer(now), now
	s.versions[id] = cloneVersion(item)
	revision := 1
	if exists {
		revision = current.Revision + 1
	}
	deployment := Deployment{Environment: environment, Kind: item.Kind, Key: item.Key, Revision: revision, DeployedBy: actor, DeployedAt: now, Version: cloneVersion(item)}
	s.deployments[key] = deployment
	return cloneVersion(item), deployment, nil
}

func (s *MemoryStore) RollbackConfigDeployment(_ context.Context, kind, key string, targetVersion int, environment, actor, _ string, now time.Time) (Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var target Version
	for _, item := range s.versions {
		if item.Kind == kind && item.Key == key && item.Version == targetVersion {
			target = item
			break
		}
	}
	if target.ID == "" {
		return Deployment{}, ErrNotFound
	}
	deploymentID := deploymentKey(environment, kind, key)
	current, ok := s.deployments[deploymentID]
	if !ok || current.Version.ID == target.ID {
		return Deployment{}, ErrConflict
	}
	previous := s.versions[current.Version.ID]
	previous.Status, previous.UpdatedAt = StatusSuperseded, now
	s.versions[previous.ID] = previous
	target.Status, target.PublishedBy, target.PublishedAt, target.UpdatedAt = StatusPublished, actor, timePointer(now), now
	s.versions[target.ID] = target
	deployment := Deployment{Environment: environment, Kind: kind, Key: key, Revision: current.Revision + 1, DeployedBy: actor, DeployedAt: now, Version: cloneVersion(target)}
	s.deployments[deploymentID] = deployment
	return deployment, nil
}

func (s *MemoryStore) ListActiveConfigDeployments(_ context.Context, kind, environment string) ([]Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byKey := map[string]Deployment{}
	for _, item := range s.deployments {
		if item.Kind != kind || (item.Environment != environment && item.Environment != "default") {
			continue
		}
		current, exists := byKey[item.Key]
		if !exists || (current.Environment == "default" && item.Environment == environment) {
			item.Version = cloneVersion(item.Version)
			byKey[item.Key] = item
		}
	}
	items := make([]Deployment, 0, len(byKey))
	for _, item := range byKey {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (s *MemoryStore) ListProviderConnections(context.Context) ([]ProviderConnection, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ProviderConnection(nil), s.providers...), nil
}

func (s *MemoryStore) ListModelCatalog(context.Context) ([]ModelCatalogEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ModelCatalogEntry(nil), s.models...), nil
}

func (s *MemoryStore) CreateAgentEvaluationRun(_ context.Context, run AgentEvaluationRun) (AgentEvaluationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.versions[run.AgentVersionID]; !exists {
		return AgentEvaluationRun{}, ErrNotFound
	}
	if _, exists := s.evaluations[run.ID]; exists || run.Status != EvaluationStatusRunning {
		return AgentEvaluationRun{}, ErrConflict
	}
	s.evaluations[run.ID] = cloneAgentEvaluationRun(run)
	return cloneAgentEvaluationRun(run), nil
}

func (s *MemoryStore) CompleteAgentEvaluationRun(
	_ context.Context,
	runID, fromStatus, decision string,
	summary AgentEvaluationSummary,
	results []AgentEvaluationCaseResult,
	now time.Time,
) (AgentEvaluationRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, exists := s.evaluations[runID]
	if !exists {
		return AgentEvaluationRun{}, ErrNotFound
	}
	if run.Status != fromStatus || (decision != EvaluationDecisionPass && decision != EvaluationDecisionFail) {
		return AgentEvaluationRun{}, ErrConflict
	}
	run.Status, run.Decision, run.Summary = EvaluationStatusCompleted, decision, summary
	run.Results, run.CompletedAt = append([]AgentEvaluationCaseResult(nil), results...), timePointer(now)
	s.evaluations[runID] = cloneAgentEvaluationRun(run)
	return cloneAgentEvaluationRun(run), nil
}

func (s *MemoryStore) GetAgentEvaluationRun(_ context.Context, runID string) (AgentEvaluationRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, exists := s.evaluations[runID]
	if !exists {
		return AgentEvaluationRun{}, ErrNotFound
	}
	return cloneAgentEvaluationRun(run), nil
}

func (s *MemoryStore) ListAgentEvaluationRuns(_ context.Context, versionID string, limit int) ([]AgentEvaluationRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AgentEvaluationRun, 0)
	for _, run := range s.evaluations {
		if run.AgentVersionID == versionID {
			items = append(items, cloneAgentEvaluationRun(run))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].StartedAt.After(items[j].StartedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) LatestPassingAgentEvaluationRun(_ context.Context, versionID, fingerprint string) (AgentEvaluationRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest AgentEvaluationRun
	for _, run := range s.evaluations {
		if run.AgentVersionID == versionID && run.AgentFingerprint == fingerprint &&
			run.Status == EvaluationStatusCompleted && run.Decision == EvaluationDecisionPass &&
			(latest.ID == "" || run.CompletedAt != nil && (latest.CompletedAt == nil || run.CompletedAt.After(*latest.CompletedAt))) {
			latest = run
		}
	}
	if latest.ID == "" {
		return AgentEvaluationRun{}, ErrNotFound
	}
	return cloneAgentEvaluationRun(latest), nil
}

func (s *MemoryStore) CreateAgentRollout(_ context.Context, rollout AgentRollout) (AgentRollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.versions[rollout.AgentVersionID]; !exists {
		return AgentRollout{}, ErrNotFound
	}
	for _, existing := range s.rollouts {
		if existing.Environment == rollout.Environment &&
			(existing.Status == AgentRolloutStatusRunning || existing.Status == AgentRolloutStatusReady) {
			return AgentRollout{}, ErrConflict
		}
	}
	rollout.Revision = 1
	s.rollouts[rollout.ID] = cloneAgentRollout(rollout)
	return cloneAgentRollout(rollout), nil
}

func (s *MemoryStore) GetAgentRollout(_ context.Context, rolloutID string) (AgentRollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rollout, exists := s.rollouts[rolloutID]
	if !exists {
		return AgentRollout{}, ErrNotFound
	}
	return cloneAgentRollout(rollout), nil
}

func (s *MemoryStore) ListAgentRollouts(_ context.Context, agentKey string, limit int) ([]AgentRollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AgentRollout, 0)
	for _, rollout := range s.rollouts {
		if rollout.AgentKey == agentKey {
			items = append(items, cloneAgentRollout(rollout))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) ActiveAgentRollout(_ context.Context, environment string) (AgentRollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, rollout := range s.rollouts {
		if rollout.Environment == environment &&
			(rollout.Status == AgentRolloutStatusRunning || rollout.Status == AgentRolloutStatusReady) {
			return cloneAgentRollout(rollout), nil
		}
	}
	return AgentRollout{}, ErrNotFound
}

func (s *MemoryStore) LatestAgentRolloutForVersion(_ context.Context, versionID, fingerprint string) (AgentRollout, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest AgentRollout
	for _, rollout := range s.rollouts {
		if rollout.AgentVersionID == versionID && rollout.AgentFingerprint == fingerprint &&
			(latest.ID == "" || rollout.CreatedAt.After(latest.CreatedAt)) {
			latest = rollout
		}
	}
	if latest.ID == "" {
		return AgentRollout{}, ErrNotFound
	}
	return cloneAgentRollout(latest), nil
}

func (s *MemoryStore) MeasureAgentRollout(_ context.Context, rollout AgentRollout, _, _ time.Time) (AgentRolloutMeasurements, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rolloutData[rollout.ID], nil
}

func (s *MemoryStore) SetAgentRolloutMeasurements(rolloutID string, measurements AgentRolloutMeasurements) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rolloutData[rolloutID] = measurements
}

func (s *MemoryStore) UpdateAgentRollout(
	_ context.Context,
	rolloutID string,
	revision int,
	status, decision string,
	summary AgentRolloutSummary,
	violations []AgentRolloutViolation,
	actor, _ string,
	now time.Time,
) (AgentRollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rollout, exists := s.rollouts[rolloutID]
	if !exists {
		return AgentRollout{}, ErrNotFound
	}
	if rollout.Revision != revision {
		return AgentRollout{}, ErrConflict
	}
	rollout.Status, rollout.Decision, rollout.Summary = status, decision, summary
	rollout.Violations = append([]AgentRolloutViolation(nil), violations...)
	rollout.UpdatedBy, rollout.UpdatedAt, rollout.Revision = actor, now, rollout.Revision+1
	rollout.EvaluatedAt = timePointer(now)
	if status == AgentRolloutStatusPaused || status == AgentRolloutStatusAborted || status == AgentRolloutStatusPromoted {
		rollout.CompletedAt = timePointer(now)
	}
	s.rollouts[rolloutID] = cloneAgentRollout(rollout)
	return cloneAgentRollout(rollout), nil
}

func (s *MemoryStore) PromoteAgentRollout(
	ctx context.Context,
	rolloutID string,
	revision int,
	environment, actor string,
	now time.Time,
) (Version, Deployment, AgentRollout, error) {
	rollout, err := s.GetAgentRollout(ctx, rolloutID)
	if err != nil {
		return Version{}, Deployment{}, AgentRollout{}, err
	}
	if rollout.Revision != revision || rollout.Status != AgentRolloutStatusReady || rollout.Decision != AgentRolloutDecisionPass {
		return Version{}, Deployment{}, AgentRollout{}, ErrConflict
	}
	version, deployment, err := s.PublishConfigVersion(ctx, rollout.AgentVersionID, environment, actor, now)
	if err != nil {
		return Version{}, Deployment{}, AgentRollout{}, err
	}
	updated, err := s.UpdateAgentRollout(ctx, rolloutID, revision, AgentRolloutStatusPromoted,
		AgentRolloutDecisionPass, rollout.Summary, rollout.Violations, actor, "rollout promoted", now)
	return version, deployment, updated, err
}

func deploymentKey(environment, kind, key string) string { return environment + ":" + kind + ":" + key }
func timePointer(value time.Time) *time.Time             { return &value }

func cloneVersion(item Version) Version {
	item.Payload = append(json.RawMessage(nil), item.Payload...)
	return item
}

func cloneAgentEvaluationRun(run AgentEvaluationRun) AgentEvaluationRun {
	payload, _ := json.Marshal(run)
	var cloned AgentEvaluationRun
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}

func seedMemoryConfiguration(store *MemoryStore) {
	now := time.Now().UTC()
	plans := []struct {
		id, key, name string
		documents     int64
		skillRuns     int64
		workspaces    int64
		classes       []string
	}{
		{"10000000-0000-4000-8000-000000000001", "free", "Free", 10, 20, 3, []string{"free"}},
		{"10000000-0000-4000-8000-000000000002", "pro", "Pro", 200, 1000, 20, []string{"standard", "quality"}},
		{"10000000-0000-4000-8000-000000000003", "team", "Team", 1000, 5000, 100, []string{"standard", "quality"}},
	}
	for _, seed := range plans {
		payload, _ := json.Marshal(BillingPlanPayload{
			DisplayName: seed.name, Status: "active", EffectiveMode: "immediate",
			Limits:              []ResourceLimit{{Resource: "documents_active", Limit: seed.documents}, {Resource: "skill_runs_monthly", Limit: seed.skillRuns}, {Resource: "workspaces", Limit: seed.workspaces}},
			AllowedModelClasses: seed.classes, AllowedSkills: []string{},
		})
		item := Version{
			ID: seed.id, Kind: KindBillingPlan, Key: seed.key, Version: 1,
			SchemaVersion: "billing-plan-v1", Status: StatusPublished, Payload: payload,
			Fingerprint: fingerprint(KindBillingPlan, seed.key, payload), Reason: "Bootstrap default plan",
			CreatedBy: "system", ValidatedBy: "system", SubmittedBy: "system", PublishedBy: "system",
			CreatedAt: now, UpdatedAt: now, ValidatedAt: timePointer(now), SubmittedAt: timePointer(now), PublishedAt: timePointer(now),
		}
		store.versions[item.ID] = item
		store.deployments[deploymentKey("default", item.Kind, item.Key)] = Deployment{Environment: "default", Kind: item.Kind, Key: item.Key, Revision: 1, DeployedBy: "system", DeployedAt: now, Version: item}
	}
	roles := map[string]ModelRoleConfig{}
	for _, role := range requiredModelRoles {
		modelID, maxTokens := "deepseek/deepseek-v4-flash-0731", 1024
		models := []string{modelID}
		if role == "companion_responder" {
			models = []string{"openai/gpt-oss-20b:free", "google/gemma-4-31b-it:free"}
		}
		if role == "translation" {
			maxTokens = 8000
		}
		roles[role] = ModelRoleConfig{Models: models, MaxTokens: maxTokens, ReasoningEffort: "low", TimeoutMS: 30000, AttemptTimeoutMS: 15000}
	}
	payload, _ := json.Marshal(ModelProfilePayload{
		ProviderConnection: "openrouter-production", ConfigVersion: "bootstrap-v1", Roles: roles,
		ProviderPolicy: ProviderPolicy{Sort: "price", AllowFallbacks: true, RequireParameters: true, DataCollection: "deny", MaxPromptPrice: .3, MaxCompletionPrice: 2.5},
	})
	item := Version{
		ID: "20000000-0000-4000-8000-000000000001", Kind: KindModelProfile, Key: "production-default", Version: 1,
		SchemaVersion: "model-profile-v1", Status: StatusPublished, Payload: payload,
		Fingerprint: fingerprint(KindModelProfile, "production-default", payload), Reason: "Bootstrap default model profile",
		CreatedBy: "system", ValidatedBy: "system", SubmittedBy: "system", PublishedBy: "system",
		CreatedAt: now, UpdatedAt: now, ValidatedAt: timePointer(now), SubmittedAt: timePointer(now), PublishedAt: timePointer(now),
	}
	store.versions[item.ID] = item
	store.deployments[deploymentKey("default", item.Kind, item.Key)] = Deployment{Environment: "default", Kind: item.Kind, Key: item.Key, Revision: 1, DeployedBy: "system", DeployedAt: now, Version: item}
}
