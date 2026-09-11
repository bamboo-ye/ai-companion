package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestBillingPlanWorkflowPublishAndRollback(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	first := createPlanVersion(t, ctx, service, 0, 25, "author-a")
	first = validateAndSubmit(t, ctx, service, first, "author-a")
	if _, _, err := service.Publish(ctx, first.ID, "author-a"); !errors.Is(err, ErrApproval) {
		t.Fatalf("same-author publish error = %v", err)
	}
	publishedOne, deploymentOne, err := service.Publish(ctx, first.ID, "approver-b")
	if err != nil || publishedOne.Status != StatusPublished || deploymentOne.Revision != 1 {
		t.Fatalf("Publish(first) = %#v, %#v, %v", publishedOne, deploymentOne, err)
	}

	second := createPlanVersion(t, ctx, service, 1, 40, "author-a")
	second = validateAndSubmit(t, ctx, service, second, "author-a")
	_, deploymentTwo, err := service.Publish(ctx, second.ID, "approver-c")
	if err != nil || deploymentTwo.Version.Version != 2 || deploymentTwo.Revision != 2 {
		t.Fatalf("Publish(second) = %#v, %v", deploymentTwo, err)
	}
	rolledBack, err := service.Rollback(ctx, KindBillingPlan, "smoke-plan", 1, "approver-d", "restore prior quota")
	if err != nil || rolledBack.Version.Version != 1 || rolledBack.Revision != 3 {
		t.Fatalf("Rollback() = %#v, %v", rolledBack, err)
	}
	plans, err := BillingPlans([]Deployment{rolledBack})
	if err != nil || len(plans) != 1 || plans[0].Limits.SkillRunsPerMonth != 25 {
		t.Fatalf("BillingPlans() = %#v, %v", plans, err)
	}
}

func TestModelProfileValidationUsesApprovedCatalog(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "production")
	payload := validModelProfilePayload(t)
	item, err := service.Create(ctx, CreateInput{
		Kind: KindModelProfile, Key: "test-profile", Payload: payload,
		Actor: "author-a", Reason: "test governed routing",
	})
	if err != nil || item.Fingerprint == "" {
		t.Fatalf("Create(valid model profile) = %#v, %v", item, err)
	}

	var decoded ModelProfilePayload
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	router := decoded.Roles["router"]
	router.Models = []string{"unapproved/model-latest"}
	decoded.Roles["router"] = router
	invalid, _ := json.Marshal(decoded)
	if _, err = service.Create(ctx, CreateInput{
		Kind: KindModelProfile, Key: "unsafe", Payload: invalid,
		Actor: "author-a", Reason: "must be rejected",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create(unapproved model) error = %v", err)
	}
}

func TestAgentDefinitionValidationAcceptsBoundedDeclarativeGraph(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	payload := validAgentDefinitionPayload(t)
	item, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "daily-assistant", Payload: payload,
		Actor: "author-a", Reason: "create first Studio graph",
	})
	if err != nil || item.SchemaVersion != "agent-definition-v1" || item.Fingerprint == "" {
		t.Fatalf("Create(Agent definition) = %#v, %v", item, err)
	}
	var decoded AgentDefinitionPayload
	if err = json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Edges = decoded.Edges[:1]
	invalid, _ := json.Marshal(decoded)
	if _, err = service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "broken-agent", Payload: invalid,
		Actor: "author-a", Reason: "must reject disconnected graph",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create(invalid Agent definition) error = %v", err)
	}
}

func TestPromptWorkflowPinsAgentNodeToPublishedVersion(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	promptOne := createPromptVersion(t, ctx, service, 0, "回答 {{user_message}}，只使用可信上下文。", "author-a")
	promptOne = validateAndSubmit(t, ctx, service, promptOne, "author-a")
	if _, _, err := service.Publish(ctx, promptOne.ID, "approver-b"); err != nil {
		t.Fatal(err)
	}

	var definition AgentDefinitionPayload
	if err := json.Unmarshal(validAgentDefinitionPayload(t), &definition); err != nil {
		t.Fatal(err)
	}
	definition.Nodes[2].PromptTemplate = ""
	definition.Nodes[2].Prompt = &AgentPromptReference{Key: "trusted-answer"}
	raw, _ := json.Marshal(definition)
	agent, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "prompt-bound-agent", Payload: raw,
		Actor: "author-c", Reason: "bind independently governed prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored AgentDefinitionPayload
	if err = json.Unmarshal(agent.Payload, &stored); err != nil {
		t.Fatal(err)
	}
	bound := stored.Nodes[2]
	if bound.Prompt == nil || bound.Prompt.VersionID != promptOne.ID || bound.Prompt.Version != 1 ||
		bound.Prompt.Fingerprint != promptOne.Fingerprint || bound.PromptTemplate != "回答 {{user_message}}，只使用可信上下文。" {
		t.Fatalf("persisted prompt binding = %#v", bound)
	}

	promptTwo := createPromptVersion(t, ctx, service, 1, "使用最新可信上下文回答 {{user_message}}。", "author-d")
	promptTwo = validateAndSubmit(t, ctx, service, promptTwo, "author-d")
	if _, _, err = service.Publish(ctx, promptTwo.ID, "approver-e"); err != nil {
		t.Fatal(err)
	}
	compiled, err := service.CompileAgentDefinition(ctx, agent.Key, agent.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Definition.Nodes[2].Prompt.VersionID != promptOne.ID ||
		compiled.Definition.Nodes[2].PromptTemplate != "回答 {{user_message}}，只使用可信上下文。" {
		t.Fatalf("historical Agent prompt changed after prompt publish: %#v", compiled.Definition.Nodes[2])
	}
}

func TestPromptValidationRejectsUndeclaredVariable(t *testing.T) {
	service := NewService(NewMemoryStore(), "test")
	payload, _ := json.Marshal(PromptPayload{
		DisplayName: "Broken", Template: "Hello {{missing}}", Variables: []string{"user_message"},
	})
	if _, err := service.Create(context.Background(), CreateInput{
		Kind: KindPrompt, Key: "broken-prompt", Payload: payload, Actor: "author-a", Reason: "must reject unknown variable",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create(prompt with undeclared variable) error = %v", err)
	}
}

func TestAgentDefinitionValidationRejectsAmbiguousExecutableEdges(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	var payload AgentDefinitionPayload
	if err := json.Unmarshal(validAgentDefinitionPayload(t), &payload); err != nil {
		t.Fatal(err)
	}
	payload.Edges[1].Condition = "needs_tool"
	duplicateCondition, _ := json.Marshal(payload)
	if _, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "duplicate-route", Payload: duplicateCondition,
		Actor: "author-a", Reason: "must reject ambiguous route",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create(duplicate router condition) error = %v", err)
	}

	if err := json.Unmarshal(validAgentDefinitionPayload(t), &payload); err != nil {
		t.Fatal(err)
	}
	payload.Edges = append(payload.Edges, AgentEdgeConfig{From: "answer", To: "use-tool"})
	ambiguousLinearNode, _ := json.Marshal(payload)
	if _, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "ambiguous-answer", Payload: ambiguousLinearNode,
		Actor: "author-a", Reason: "must reject ambiguous linear node",
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create(multiple model edges) error = %v", err)
	}
}

func TestCompileAgentDefinitionReturnsExecutableManifestWithoutPersisting(t *testing.T) {
	compiled, err := CompileAgentDefinition("daily-assistant", validAgentDefinitionPayload(t))
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.Executable || compiled.CompilerVersion != "agent-studio-langgraph/v1" ||
		compiled.EntryNode != "route" || len(compiled.Nodes) != 4 || len(compiled.Fingerprint) != 64 {
		t.Fatalf("CompileAgentDefinition() = %#v", compiled)
	}
	if !compiled.Capabilities["durable_checkpoints"] || !compiled.Capabilities["tool_approvals"] {
		t.Fatalf("CompileAgentDefinition() capabilities = %#v", compiled.Capabilities)
	}
	if len(compiled.Nodes[0].Outgoing) != 2 || compiled.Nodes[0].Outgoing[0].Condition == "" {
		t.Fatalf("CompileAgentDefinition() route = %#v", compiled.Nodes[0])
	}
}

func TestActiveAgentRuntimeResolvesPublishedModuleDefinition(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	item, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "daily-assistant", Payload: validAgentDefinitionPayload(t),
		Actor: "author-a", Reason: "bind Studio graph to runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	item = validateAndSubmit(t, ctx, service, item, "author-a")
	if _, _, err = service.Publish(ctx, item.ID, "approver-b"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.ActiveAgentRuntime(ctx, "life")
	if err != nil || snapshot.Key != "daily-assistant" || snapshot.VersionID != item.ID ||
		snapshot.ModelProfile != "production-default" || snapshot.Definition.Budget.MaxModelCalls != 6 {
		t.Fatalf("ActiveAgentRuntime() = %#v, %v", snapshot, err)
	}
	if _, err = service.ActiveAgentRuntime(ctx, "companion"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ActiveAgentRuntime(unassigned) error = %v", err)
	}
	competing, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "competing-assistant", Payload: validAgentDefinitionPayload(t),
		Actor: "author-c", Reason: "must not steal an assigned module",
	})
	if err != nil {
		t.Fatal(err)
	}
	competing = validateAndSubmit(t, ctx, service, competing, "author-c")
	if _, _, err = service.Publish(ctx, competing.ID, "approver-d"); !errors.Is(err, ErrValidation) {
		t.Fatalf("Publish(overlapping Agent) error = %v", err)
	}
}

func createPlanVersion(t *testing.T, ctx context.Context, service *Service, baseVersion, skillRuns int, actor string) Version {
	t.Helper()
	payload, _ := json.Marshal(BillingPlanPayload{
		DisplayName: "Free", Status: "active", EffectiveMode: "immediate",
		Limits: []ResourceLimit{
			{Resource: "documents_active", Limit: 10},
			{Resource: "skill_runs_monthly", Limit: int64(skillRuns)},
			{Resource: "workspaces", Limit: 3},
		},
		AllowedModelClasses: []string{"free"}, AllowedSkills: []string{},
	})
	item, err := service.Create(ctx, CreateInput{
		Kind: KindBillingPlan, Key: "smoke-plan", BaseVersion: baseVersion,
		Payload: payload, Actor: actor, Reason: "adjust free plan test quota",
	})
	if err != nil {
		t.Fatalf("Create(plan) error = %v", err)
	}
	return item
}

func createPromptVersion(t *testing.T, ctx context.Context, service *Service, baseVersion int, template, actor string) Version {
	t.Helper()
	payload, _ := json.Marshal(PromptPayload{
		DisplayName: "Trusted answer", Description: "Prompt used by a versioned Agent node",
		Template: template, Variables: []string{"user_message"},
	})
	item, err := service.Create(ctx, CreateInput{
		Kind: KindPrompt, Key: "trusted-answer", BaseVersion: baseVersion,
		Payload: payload, Actor: actor, Reason: "version the trusted answer prompt",
	})
	if err != nil {
		t.Fatalf("Create(prompt) error = %v", err)
	}
	return item
}

func validateAndSubmit(t *testing.T, ctx context.Context, service *Service, item Version, actor string) Version {
	t.Helper()
	validated, err := service.Validate(ctx, item.ID, actor)
	if err != nil || validated.Status != StatusValidated {
		t.Fatalf("Validate() = %#v, %v", validated, err)
	}
	if validated.Kind == KindAgentDefinition {
		if run, evaluationErr := service.EvaluateAgentVersion(ctx, validated.ID, passingEvaluationSuite(), actor, deterministicEvaluationSandbox{}); evaluationErr != nil || run.Decision != EvaluationDecisionPass {
			t.Fatalf("EvaluateAgentVersion() = %#v, %v", run, evaluationErr)
		}
	}
	submitted, err := service.Submit(ctx, item.ID, actor)
	if err != nil || submitted.Status != StatusSubmitted {
		t.Fatalf("Submit() = %#v, %v", submitted, err)
	}
	return submitted
}

func validModelProfilePayload(t *testing.T) json.RawMessage {
	t.Helper()
	roles := map[string]ModelRoleConfig{}
	for _, role := range requiredModelRoles {
		model := "deepseek/deepseek-v4-flash-0731"
		if role == "companion_responder" {
			model = "openai/gpt-oss-20b:free"
		}
		maxTokens := 1024
		if role == "translation" {
			maxTokens = 8000
		}
		roles[role] = ModelRoleConfig{Models: []string{model}, MaxTokens: maxTokens, ReasoningEffort: "low", TimeoutMS: 30000, AttemptTimeoutMS: 15000}
	}
	payload, err := json.Marshal(ModelProfilePayload{
		ProviderConnection: "openrouter-production", ConfigVersion: "test-profile-v1", Roles: roles,
		ProviderPolicy: ProviderPolicy{Sort: "price", AllowFallbacks: true, RequireParameters: true, DataCollection: "deny", MaxPromptPrice: .3, MaxCompletionPrice: 2.5},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func validAgentDefinitionPayload(t *testing.T) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(AgentDefinitionPayload{
		DisplayName: "Daily Assistant", Description: "A bounded Studio graph",
		Modules: []string{"life", "work"}, ModelProfile: "production-default",
		EntryNode: "route",
		Nodes: []AgentNodeConfig{
			{Key: "route", Type: "router", ModelRole: "router", PromptTemplate: "Classify {{ user_message }}."},
			{Key: "use-tool", Type: "tool", Tools: []string{"life_query_today_plan"}},
			{Key: "answer", Type: "model", ModelRole: "responder", PromptTemplate: "Answer using trusted observations."},
			{Key: "done", Type: "end"},
		},
		Edges: []AgentEdgeConfig{
			{From: "route", To: "use-tool", Condition: "needs_tool"},
			{From: "route", To: "answer", Condition: "direct"},
			{From: "use-tool", To: "answer"},
			{From: "answer", To: "done"},
		},
		Budget: AgentBudgetConfig{MaxSteps: 12, MaxModelCalls: 6, MaxToolCalls: 3, MaxTotalTokens: 16000, TimeoutMS: 120000},
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
