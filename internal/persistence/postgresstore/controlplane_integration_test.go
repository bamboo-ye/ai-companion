package postgresstore

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func TestConfigurationControlPlaneWorkflow(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	suffix, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	key := "smoke-" + suffix[:8]
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE resource_type='agent_rollout' AND resource_id IN (SELECT id FROM ops.agent_rollouts WHERE agent_key=$1)`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.agent_rollouts WHERE agent_key=$1`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE resource_type='agent_evaluation_run' AND resource_id IN (SELECT id FROM ops.evaluation_runs WHERE agent_key=$1)`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.evaluation_runs WHERE agent_key=$1`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE resource_type='config_version' AND resource_id IN (SELECT id FROM ops.config_versions WHERE config_key=$1)`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.runtime_config_reports WHERE config_key=$1`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.config_deployments WHERE config_key=$1`, key)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.config_versions WHERE config_key=$1`, key)
	})

	service := controlplane.NewService(store, "integration-test")
	payload, _ := json.Marshal(controlplane.BillingPlanPayload{
		DisplayName: "Smoke Plan", Status: "active", EffectiveMode: "immediate",
		Limits: []controlplane.ResourceLimit{
			{Resource: "documents_active", Limit: 15},
			{Resource: "skill_runs_monthly", Limit: 45},
			{Resource: "workspaces", Limit: 5},
		},
		AllowedModelClasses: []string{"free"}, AllowedSkills: []string{},
	})
	version, err := service.Create(ctx, controlplane.CreateInput{
		Kind: controlplane.KindBillingPlan, Key: key, Payload: payload,
		Actor: "integration-author", Reason: "control plane smoke test",
	})
	if err != nil || version.Version != 1 || version.Status != controlplane.StatusDraft {
		t.Fatalf("Create() = %#v, %v", version, err)
	}
	if version, err = service.Validate(ctx, version.ID, "integration-author"); err != nil || version.Status != controlplane.StatusValidated {
		t.Fatalf("Validate() = %#v, %v", version, err)
	}
	if version, err = service.Submit(ctx, version.ID, "integration-author"); err != nil || version.Status != controlplane.StatusSubmitted {
		t.Fatalf("Submit() = %#v, %v", version, err)
	}
	published, deployment, err := service.Publish(ctx, version.ID, "integration-approver")
	if err != nil || published.Status != controlplane.StatusPublished || deployment.Revision != 1 {
		t.Fatalf("Publish() = %#v, %#v, %v", published, deployment, err)
	}
	active, err := service.Active(ctx, controlplane.KindBillingPlan)
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	found := false
	for _, item := range active {
		if item.Key == key && item.Version.ID == version.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("published deployment %s missing from %#v", key, active)
	}
	report, err := service.ReportRuntimeConfig(ctx, controlplane.RuntimeConfigReport{
		InstanceID: "integration-api", Service: "integration-api", Kind: controlplane.KindBillingPlan,
		Key: key, VersionID: version.ID, Revision: deployment.Revision, Fingerprint: version.Fingerprint,
		Status: controlplane.RuntimeStatusApplied, StartedAt: time.Now().UTC().Add(-time.Minute),
	})
	if err != nil || report.VersionID != version.ID {
		t.Fatalf("ReportRuntimeConfig() = %#v, %v", report, err)
	}
	convergence, err := service.RuntimeConvergence(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("RuntimeConvergence() error = %v", err)
	}
	converged := false
	for _, state := range convergence.Instances {
		if state.InstanceID == report.InstanceID && state.Key == key && state.Convergence == "converged" {
			converged = true
		}
	}
	if !converged {
		t.Fatalf("runtime report did not converge: %#v", convergence)
	}
	providers, err := service.Providers(ctx)
	if err != nil || len(providers) == 0 || providers[0].CredentialRef == "" {
		t.Fatalf("Providers() = %#v, %v", providers, err)
	}
	models, err := service.Models(ctx)
	if err != nil || len(models) < 3 {
		t.Fatalf("Models() = %#v, %v", models, err)
	}
	runtimeSnapshot, err := service.ActiveModelRuntime(ctx, controlplane.DefaultModelProfileKey)
	if err != nil || runtimeSnapshot.VersionID == "" || runtimeSnapshot.Variables["MODEL_PROVIDER"] != "openrouter" {
		t.Fatalf("ActiveModelRuntime() = %#v, %v", runtimeSnapshot, err)
	}
	agentPayload, _ := json.Marshal(controlplane.AgentDefinitionPayload{
		DisplayName: "Integration Agent", Modules: []string{"work"},
		ModelProfile: controlplane.DefaultModelProfileKey, EntryNode: "answer",
		Nodes: []controlplane.AgentNodeConfig{
			{Key: "answer", Type: "model", ModelRole: "responder", PromptTemplate: "Answer the request."},
			{Key: "done", Type: "end"},
		},
		Edges:  []controlplane.AgentEdgeConfig{{From: "answer", To: "done"}},
		Budget: controlplane.AgentBudgetConfig{MaxSteps: 4, MaxModelCalls: 2, MaxToolCalls: 0, MaxTotalTokens: 4096, TimeoutMS: 30000},
	})
	agentVersion, err := service.Create(ctx, controlplane.CreateInput{
		Kind: controlplane.KindAgentDefinition, Key: key, Payload: agentPayload,
		Actor: "integration-author", Reason: "Agent Studio persistence smoke test",
	})
	if err != nil || agentVersion.SchemaVersion != "agent-definition-v1" {
		t.Fatalf("Create(Agent definition) = %#v, %v", agentVersion, err)
	}
	agentVersion, err = service.Validate(ctx, agentVersion.ID, "integration-author")
	if err == nil {
		_, err = service.EvaluateAgentVersion(ctx, agentVersion.ID, integrationEvaluationSuite("work"), "integration-author", integrationEvaluationSandbox{})
	}
	if err == nil {
		agentVersion, err = service.Submit(ctx, agentVersion.ID, "integration-author")
	}
	if err == nil {
		agentVersion, _, err = service.Publish(ctx, agentVersion.ID, "integration-approver")
	}
	if err != nil || agentVersion.Status != controlplane.StatusPublished {
		t.Fatalf("Publish(Agent definition) = %#v, %v", agentVersion, err)
	}
	candidate, err := service.Create(ctx, controlplane.CreateInput{
		Kind: controlplane.KindAgentDefinition, Key: key, BaseVersion: 1, Payload: agentPayload,
		Actor: "integration-author", Reason: "Agent rollout persistence smoke test",
	})
	if err == nil {
		candidate, err = service.Validate(ctx, candidate.ID, "integration-author")
	}
	if err == nil {
		_, err = service.EvaluateAgentVersion(ctx, candidate.ID, integrationEvaluationSuite("work"), "integration-author", integrationEvaluationSandbox{})
	}
	if err == nil {
		candidate, err = service.Submit(ctx, candidate.ID, "integration-author")
	}
	policy := controlplane.DefaultAgentRolloutPolicy()
	policy.MinimumSampleSize = 1
	policy.ObservationWindowSeconds = 60
	var rollout controlplane.AgentRollout
	if err == nil {
		rollout, err = service.StartAgentRollout(ctx, candidate.ID, controlplane.StartAgentRolloutInput{
			TrafficPercent: 10, Policy: policy, Actor: "integration-approver",
		})
	}
	if err == nil {
		rollout, err = service.RefreshAgentRollout(ctx, rollout.ID, "integration-support")
	}
	if err != nil || rollout.Status != controlplane.AgentRolloutStatusRunning || rollout.Decision != controlplane.AgentRolloutDecisionCollecting {
		t.Fatalf("RefreshAgentRollout(empty sample) = %#v, %v", rollout, err)
	}
	rollout, err = store.UpdateAgentRollout(ctx, rollout.ID, rollout.Revision,
		controlplane.AgentRolloutStatusReady, controlplane.AgentRolloutDecisionPass,
		controlplane.AgentRolloutSummary{Candidate: controlplane.AgentRolloutMetrics{SampleSize: 1, Completed: 1}},
		[]controlplane.AgentRolloutViolation{}, "integration-support", "verified fixture metrics", time.Now().UTC())
	if err == nil {
		candidate, _, err = service.Publish(ctx, candidate.ID, "integration-approver")
	}
	if err != nil || candidate.Status != controlplane.StatusPublished {
		t.Fatalf("PromoteAgentRollout() = %#v, %#v, %v", candidate, rollout, err)
	}
	loadedRollout, err := service.AgentRollout(ctx, rollout.ID)
	if err != nil || loadedRollout.Status != controlplane.AgentRolloutStatusPromoted {
		t.Fatalf("AgentRollout(promoted) = %#v, %v", loadedRollout, err)
	}
}

type integrationEvaluationSandbox struct{}

func (integrationEvaluationSandbox) Run(_ context.Context, _ controlplane.AgentSandboxRequest) (controlplane.AgentSandboxReport, error) {
	return controlplane.AgentSandboxReport{
		SchemaVersion: "agent-studio-synthetic-run/v1", RunID: "integration-sandbox",
		Mode: "synthetic", Status: "completed", Outcome: "completed", Response: "integration synthetic answer",
		Safety:     map[string]any{"side_effects": false, "production_data_access": false, "external_model_calls": 0, "external_tool_calls": 0},
		Budget:     map[string]any{"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}},
		NodeTrace:  []map[string]any{{"node": "studio__answer", "status": "completed"}},
		ModelCalls: []map[string]any{{"role": "responder"}}, ToolCalls: []map[string]any{},
		ToolSelections: []map[string]any{}, Interrupts: []map[string]any{}, Recovery: map[string]any{},
	}, nil
}

func integrationEvaluationSuite(modules ...string) controlplane.AgentEvaluationSuite {
	maxCalls, maxSteps := 2, 4
	suite := controlplane.AgentEvaluationSuite{
		SchemaVersion: controlplane.AgentEvaluationSuiteSchema, Name: "integration-smoke", Version: "v1",
		Cases: []controlplane.AgentEvaluationCase{},
	}
	for _, module := range modules {
		suite.Cases = append(suite.Cases, controlplane.AgentEvaluationCase{
			SchemaVersion: controlplane.AgentEvaluationCaseSchema, ID: "synthetic-answer-" + module, Tags: []string{"synthetic"},
			Given:  controlplane.AgentEvaluationGiven{Module: module, UserMessage: "integration evaluation", Tools: []map[string]any{}},
			Expect: controlplane.AgentEvaluationExpect{Status: "completed", Outcome: []byte(`"completed"`), MaxModelCalls: &maxCalls, MaxSteps: &maxSteps, ResponseContains: []string{"synthetic"}},
			Tape:   map[string]any{},
		})
	}
	return suite
}
