package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/controlplane"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestConfigurationStepUpAllowsDevelopmentManagementKey(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/ops/config-versions/version-1/publish", nil)
	request = request.WithContext(context.WithValue(request.Context(), operatorContextKey{}, operatorAuth{
		Actor: "operator", Role: "admin", Legacy: true,
	}))

	development := &Server{}
	if allowed := development.requireConfigurationStepUpMFA(httptest.NewRecorder(), request); !allowed {
		t.Fatal("development management key should satisfy the configuration step-up gate")
	}

	production := &Server{operatorMFARequired: true}
	response := httptest.NewRecorder()
	if allowed := production.requireConfigurationStepUpMFA(response, request); allowed || response.Code != http.StatusForbidden {
		t.Fatalf("production management key gate = %t, status = %d", allowed, response.Code)
	}
}

func TestOperatorRuntimeConvergenceReportsLoadedAPIRevisions(t *testing.T) {
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test-api", Environment: "development",
		AuthTokenSecret: "configuration-runtime-secret-with-enough-entropy", OperatorToken: "runtime-ops-token",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetBillingStore(billing.NewMemoryStore())
	if err := server.SetControlPlaneStore(controlplane.NewMemoryStore()); err != nil {
		t.Fatal(err)
	}
	if err := server.SyncRuntimeConfiguration(context.Background(), "api-instance-1", "test-api", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	response := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/configuration/convergence", "runtime-ops-token", "runtime-admin", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"converged":3`) || !strings.Contains(response.Body.String(), `"instance_id":"api-instance-1"`) {
		t.Fatalf("convergence=%d %s", response.Code, response.Body.String())
	}
}

func TestOperatorBillingPlanWorkflowHotReloadsCatalog(t *testing.T) {
	now := time.Now().UTC()
	secretA, secretB := "JBSWY3DPEHPK3PXP", "KRSXG5DSNFXGOIDB"
	adminA, err := opsauth.BootstrapAccount("config-admin-a", "Config Admin A", "admin", "config-token-a", secretA, true, now)
	if err != nil {
		t.Fatal(err)
	}
	adminB, err := opsauth.BootstrapAccount("config-admin-b", "Config Admin B", "admin", "config-token-b", secretB, true, now)
	if err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test", Environment: "test",
		AuthTokenSecret: "configuration-test-secret-with-enough-entropy", OperatorMFARequired: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperatorAuthStore(opsauth.NewMemoryStore(adminA, adminB))
	server.SetBillingStore(billing.NewMemoryStore())
	if err = server.SetControlPlaneStore(controlplane.NewMemoryStore()); err != nil {
		t.Fatal(err)
	}
	server.SetAgentSandbox(&recordingAgentSandbox{})
	otpA, _ := opsauth.GenerateTOTP(secretA, time.Now().UTC())
	otpB, _ := opsauth.GenerateTOTP(secretB, time.Now().UTC())

	created := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/billing/plans/free/versions", "config-token-a", "", otpA, map[string]any{
		"base_version": 1, "display_name": "Free", "status": "active", "effective_mode": "immediate",
		"limits": []map[string]any{
			{"resource": "documents_active", "limit": 12},
			{"resource": "skill_runs_monthly", "limit": 33},
			{"resource": "workspaces", "limit": 4},
		},
		"allowed_model_classes": []string{"free"}, "allowed_skills": []string{},
		"reason": "increase test free quota",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	var envelope struct {
		Version controlplane.Version `json:"version"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &envelope); err != nil || envelope.Version.ID == "" {
		t.Fatalf("decode created=%#v, %v", envelope, err)
	}
	versionPath := "/v1/ops/config-versions/" + envelope.Version.ID
	validated := performOperatorJSONWithOTP(t, server, http.MethodPost, versionPath+"/validate", "config-token-a", "", otpA, nil)
	if validated.Code != http.StatusOK || !strings.Contains(validated.Body.String(), `"valid":true`) {
		t.Fatalf("validated=%d %s", validated.Code, validated.Body.String())
	}
	submitted := performOperatorJSONWithOTP(t, server, http.MethodPost, versionPath+"/submit", "config-token-a", "", otpA, nil)
	if submitted.Code != http.StatusOK || !strings.Contains(submitted.Body.String(), `"status":"submitted"`) {
		t.Fatalf("submitted=%d %s", submitted.Code, submitted.Body.String())
	}
	sameAuthor := performOperatorJSONWithOTP(t, server, http.MethodPost, versionPath+"/publish", "config-token-a", "", otpA, nil)
	if sameAuthor.Code != http.StatusForbidden || !strings.Contains(sameAuthor.Body.String(), "configuration_approval_separation_required") {
		t.Fatalf("same author publish=%d %s", sameAuthor.Code, sameAuthor.Body.String())
	}
	published := performOperatorJSONWithOTP(t, server, http.MethodPost, versionPath+"/publish", "config-token-b", "", otpB, nil)
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"runtime_applied":true`) || !strings.Contains(published.Body.String(), `"status":"published"`) {
		t.Fatalf("published=%d %s", published.Code, published.Body.String())
	}
	summary, err := server.billing.Summary(context.Background(), "11111111-1111-4111-8111-111111111111")
	if err != nil || summary.Plan.Limits.SkillRunsPerMonth != 33 || summary.Plan.Limits.Documents != 12 || summary.Plan.Limits.Workspaces != 4 {
		t.Fatalf("billing hot reload summary=%#v, %v", summary, err)
	}
	listed := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/billing/plans", "config-token-a", "", otpA, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"skill_runs_monthly"`) || !strings.Contains(listed.Body.String(), `"revision":1`) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	catalog := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/model/catalog", "config-token-a", "", otpA, nil)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), "openai/gpt-5-mini") {
		t.Fatalf("catalog=%d %s", catalog.Code, catalog.Body.String())
	}
	promptDraft := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/prompts/trusted-answer/versions", "config-token-a", "", otpA, map[string]any{
		"base_version": 0, "display_name": "Trusted Answer", "description": "Shared answer policy",
		"template": "Answer {{user_message}} with trusted context only.", "variables": []string{"user_message"},
		"reason": "create independently governed prompt",
	})
	var promptEnvelope struct {
		Version controlplane.Version `json:"version"`
	}
	if promptDraft.Code != http.StatusCreated || json.Unmarshal(promptDraft.Body.Bytes(), &promptEnvelope) != nil || promptEnvelope.Version.ID == "" {
		t.Fatalf("prompt draft=%d %s", promptDraft.Code, promptDraft.Body.String())
	}
	promptVersionPath := "/v1/ops/config-versions/" + promptEnvelope.Version.ID
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, promptVersionPath+"/validate", "config-token-a", "", otpA, nil); response.Code != http.StatusOK {
		t.Fatalf("validate prompt=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, promptVersionPath+"/submit", "config-token-a", "", otpA, nil); response.Code != http.StatusOK {
		t.Fatalf("submit prompt=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, promptVersionPath+"/publish", "config-token-b", "", otpB, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Prompt") {
		t.Fatalf("publish prompt=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/prompts", "config-token-a", "", otpA, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"key":"trusted-answer"`) {
		t.Fatalf("list prompts=%d %s", response.Code, response.Body.String())
	}
	agentCompile := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agents/daily-assistant/compile", "config-token-a", "", otpA, map[string]any{
		"display_name": "Daily Assistant", "description": "Studio smoke graph",
		"modules": []string{"life"}, "model_profile": "production-default", "entry_node": "route",
		"nodes": []map[string]any{
			{"key": "route", "type": "router", "model_role": "router", "prompt_template": "Route the user request."},
			{"key": "answer", "type": "model", "model_role": "responder", "prompt": map[string]any{"key": "trusted-answer"}},
			{"key": "done", "type": "end"},
		},
		"edges": []map[string]any{
			{"from": "route", "to": "answer", "condition": "direct"},
			{"from": "route", "to": "done", "condition": "no_response"},
			{"from": "answer", "to": "done"},
		},
		"budget": map[string]any{"max_steps": 8, "max_model_calls": 4, "max_tool_calls": 0, "max_total_tokens": 8000, "timeout_ms": 60000},
	})
	if agentCompile.Code != http.StatusOK || !strings.Contains(agentCompile.Body.String(), `"compiler_version":"agent-studio-langgraph/v1"`) || !strings.Contains(agentCompile.Body.String(), `"executable":true`) || !strings.Contains(agentCompile.Body.String(), promptEnvelope.Version.ID) {
		t.Fatalf("agent compile=%d %s", agentCompile.Code, agentCompile.Body.String())
	}
	agentDraft := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agents/daily-assistant/versions", "config-token-a", "", otpA, map[string]any{
		"base_version": 0, "display_name": "Daily Assistant", "description": "Studio smoke graph",
		"modules": []string{"life"}, "model_profile": "production-default", "entry_node": "route",
		"nodes": []map[string]any{
			{"key": "route", "type": "router", "model_role": "router", "prompt_template": "Route the user request."},
			{"key": "answer", "type": "model", "model_role": "responder", "prompt_template": "Answer safely."},
			{"key": "done", "type": "end"},
		},
		"edges": []map[string]any{
			{"from": "route", "to": "answer", "condition": "direct"},
			{"from": "route", "to": "done", "condition": "no_response"},
			{"from": "answer", "to": "done"},
		},
		"budget": map[string]any{"max_steps": 8, "max_model_calls": 4, "max_tool_calls": 0, "max_total_tokens": 8000, "timeout_ms": 60000},
		"reason": "create Studio smoke graph",
	})
	if agentDraft.Code != http.StatusCreated {
		t.Fatalf("agent draft=%d %s", agentDraft.Code, agentDraft.Body.String())
	}
	var agentEnvelope struct {
		Version controlplane.Version `json:"version"`
	}
	if err = json.Unmarshal(agentDraft.Body.Bytes(), &agentEnvelope); err != nil || agentEnvelope.Version.ID == "" {
		t.Fatalf("decode Agent draft=%#v, %v", agentEnvelope, err)
	}
	agentVersionPath := "/v1/ops/config-versions/" + agentEnvelope.Version.ID
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, agentVersionPath+"/validate", "config-token-a", "", otpA, nil); response.Code != http.StatusOK {
		t.Fatalf("validate Agent=%d %s", response.Code, response.Body.String())
	}
	evaluation := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agent-versions/"+agentEnvelope.Version.ID+"/evaluations", "config-token-a", "", otpA, map[string]any{
		"schema_version": "agent-eval-suite-v1", "name": "studio-smoke", "version": "v1",
		"cases": []map[string]any{{
			"schema_version": "agent-eval-case-v1", "id": "direct-answer", "tags": []string{"direct"},
			"given":  map[string]any{"module": "life", "user_message": "合成评测", "tools": []any{}},
			"expect": map[string]any{"status": "completed", "outcome": "completed", "max_model_calls": 4, "max_steps": 8, "response_contains": []string{"合成测试"}},
			"tape":   map[string]any{},
		}},
	})
	if evaluation.Code != http.StatusCreated || !strings.Contains(evaluation.Body.String(), `"decision":"pass"`) {
		t.Fatalf("evaluate Agent=%d %s", evaluation.Code, evaluation.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, agentVersionPath+"/submit", "config-token-a", "", otpA, nil); response.Code != http.StatusOK {
		t.Fatalf("submit Agent=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, agentVersionPath+"/publish", "config-token-b", "", otpB, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Studio") {
		t.Fatalf("publish Agent=%d %s", response.Code, response.Body.String())
	}
	listedAgents := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/agents", "config-token-a", "", otpA, nil)
	if listedAgents.Code != http.StatusOK || !strings.Contains(listedAgents.Body.String(), `"key":"daily-assistant"`) {
		t.Fatalf("listed Agents=%d %s", listedAgents.Code, listedAgents.Body.String())
	}
	agentCandidate := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agents/daily-assistant/versions", "config-token-a", "", otpA, map[string]any{
		"base_version": 1, "display_name": "Daily Assistant v2", "description": "Studio rollout graph",
		"modules": []string{"life"}, "model_profile": "production-default", "entry_node": "route",
		"nodes": []map[string]any{
			{"key": "route", "type": "router", "model_role": "router", "prompt_template": "Route the user request."},
			{"key": "answer", "type": "model", "model_role": "responder", "prompt_template": "Answer safely."},
			{"key": "done", "type": "end"},
		},
		"edges": []map[string]any{
			{"from": "route", "to": "answer", "condition": "direct"},
			{"from": "route", "to": "done", "condition": "no_response"},
			{"from": "answer", "to": "done"},
		},
		"budget": map[string]any{"max_steps": 8, "max_model_calls": 4, "max_tool_calls": 0, "max_total_tokens": 8000, "timeout_ms": 60000},
		"reason": "exercise controlled rollout API",
	})
	var candidateEnvelope struct {
		Version controlplane.Version `json:"version"`
	}
	if agentCandidate.Code != http.StatusCreated || json.Unmarshal(agentCandidate.Body.Bytes(), &candidateEnvelope) != nil {
		t.Fatalf("Agent candidate=%d %s", agentCandidate.Code, agentCandidate.Body.String())
	}
	candidatePath := "/v1/ops/config-versions/" + candidateEnvelope.Version.ID
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, candidatePath+"/validate", "config-token-a", "", otpA, nil); response.Code != http.StatusOK {
		t.Fatalf("validate Agent candidate=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agent-versions/"+candidateEnvelope.Version.ID+"/evaluations", "config-token-a", "", otpA, map[string]any{
		"schema_version": "agent-eval-suite-v1", "name": "studio-smoke", "version": "v2",
		"cases": []map[string]any{{
			"schema_version": "agent-eval-case-v1", "id": "direct-answer", "tags": []string{"direct"},
			"given":  map[string]any{"module": "life", "user_message": "合成评测", "tools": []any{}},
			"expect": map[string]any{"status": "completed", "outcome": "completed", "max_model_calls": 4, "max_steps": 8, "response_contains": []string{"合成测试"}}, "tape": map[string]any{},
		}},
	}); response.Code != http.StatusCreated {
		t.Fatalf("evaluate Agent candidate=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, candidatePath+"/submit", "config-token-a", "", otpA, nil); response.Code != http.StatusOK {
		t.Fatalf("submit Agent candidate=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, candidatePath+"/publish", "config-token-b", "", otpB, nil); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "agent_rollout_required") {
		t.Fatalf("publish without rollout=%d %s", response.Code, response.Body.String())
	}
	started := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agent-versions/"+candidateEnvelope.Version.ID+"/rollouts", "config-token-b", "", otpB, map[string]any{"traffic_percent": 10, "policy": map[string]any{}})
	var rolloutEnvelope struct {
		Rollout controlplane.AgentRollout `json:"rollout"`
	}
	if started.Code != http.StatusCreated || json.Unmarshal(started.Body.Bytes(), &rolloutEnvelope) != nil || rolloutEnvelope.Rollout.Status != controlplane.AgentRolloutStatusRunning {
		t.Fatalf("start rollout=%d %s", started.Code, started.Body.String())
	}
	rolloutPath := "/v1/ops/agent-rollouts/" + rolloutEnvelope.Rollout.ID
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, rolloutPath+"/refresh", "config-token-a", "", otpA, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"decision":"collecting"`) {
		t.Fatalf("refresh rollout=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/agents/daily-assistant/rollouts", "config-token-a", "", otpA, nil); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), rolloutEnvelope.Rollout.ID) {
		t.Fatalf("list rollouts=%d %s", response.Code, response.Body.String())
	}
	if response := performOperatorJSONWithOTP(t, server, http.MethodPost, rolloutPath+"/abort", "config-token-b", "", otpB, map[string]any{"reason": "finish HTTP rollout smoke test"}); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"aborted"`) {
		t.Fatalf("abort rollout=%d %s", response.Code, response.Body.String())
	}
}
