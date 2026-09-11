package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

type recordingAgentSandbox struct {
	requests []controlplane.AgentSandboxRequest
}

func (s *recordingAgentSandbox) Run(_ context.Context, input controlplane.AgentSandboxRequest) (controlplane.AgentSandboxReport, error) {
	s.requests = append(s.requests, input)
	return controlplane.AgentSandboxReport{
		SchemaVersion: "agent-studio-synthetic-run/v1",
		RunID:         "sandbox-test",
		Mode:          "synthetic",
		Status:        "completed",
		Outcome:       "completed",
		Response:      "合成测试已完成。",
		Safety:        map[string]any{"side_effects": false, "external_model_calls": 0, "external_tool_calls": 0},
		Budget:        map[string]any{"usage": map[string]any{"model_calls": 2}},
		NodeOutputs:   map[string]any{},
		NodeTrace:     []map[string]any{},
		ModelCalls:    []map[string]any{},
		ToolCalls:     []map[string]any{},
		Interrupts:    []map[string]any{},
		Recovery:      map[string]any{},
	}, nil
}

func TestOperatorAgentSyntheticDryRunSupportsDraftAndImmutableVersion(t *testing.T) {
	now := time.Now().UTC()
	supportSecret, viewerSecret := "JBSWY3DPEHPK3PXP", "KRSXG5DSNFXGOIDB"
	support, err := opsauth.BootstrapAccount("sandbox-support", "Sandbox Support", "support", "sandbox-support-token", supportSecret, true, now)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := opsauth.BootstrapAccount("sandbox-viewer", "Sandbox Viewer", "viewer", "sandbox-viewer-token", viewerSecret, true, now)
	if err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test", Environment: "test",
		AuthTokenSecret: "agent-sandbox-test-secret-with-enough-entropy", OperatorMFARequired: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperatorAuthStore(opsauth.NewMemoryStore(support, viewer))
	if err = server.SetControlPlaneStore(controlplane.NewMemoryStore()); err != nil {
		t.Fatal(err)
	}
	sandbox := &recordingAgentSandbox{}
	server.SetAgentSandbox(sandbox)
	supportOTP, _ := opsauth.GenerateTOTP(supportSecret, time.Now().UTC())
	viewerOTP, _ := opsauth.GenerateTOTP(viewerSecret, time.Now().UTC())
	definition := sandboxAgentDefinition()

	draftBody := map[string]any{
		"definition": definition,
		"scenario": map[string]any{
			"module": "work",
			"routes": map[string]any{"route": map[string]any{"condition": "respond"}},
		},
	}
	forbidden := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agents/work-assistant/dry-run", "sandbox-viewer-token", "", viewerOTP, draftBody)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("viewer dry run=%d %s", forbidden.Code, forbidden.Body.String())
	}
	draft := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agents/work-assistant/dry-run", "sandbox-support-token", "", supportOTP, draftBody)
	if draft.Code != http.StatusOK {
		t.Fatalf("draft dry run=%d %s", draft.Code, draft.Body.String())
	}
	if len(sandbox.requests) != 1 || sandbox.requests[0].Identity.VersionID != "draft" ||
		sandbox.requests[0].Identity.Key != "work-assistant" || len(sandbox.requests[0].Identity.Fingerprint) != 64 {
		t.Fatalf("draft sandbox request=%#v", sandbox.requests)
	}

	payload, _ := json.Marshal(definition)
	version, err := server.configuration.Create(context.Background(), controlplane.CreateInput{
		Kind: controlplane.KindAgentDefinition, Key: "work-assistant", Payload: payload,
		Actor: "sandbox-author", Reason: "persist a version for synthetic dry-run testing",
	})
	if err != nil {
		t.Fatal(err)
	}
	versionRun := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/agent-versions/"+version.ID+"/dry-runs", "sandbox-support-token", "", supportOTP, map[string]any{
		"scenario": map[string]any{"module": "work"},
	})
	if versionRun.Code != http.StatusOK {
		t.Fatalf("version dry run=%d %s", versionRun.Code, versionRun.Body.String())
	}
	if len(sandbox.requests) != 2 || sandbox.requests[1].Identity.VersionID != version.ID ||
		sandbox.requests[1].Identity.Key != version.Key || sandbox.requests[1].Identity.Fingerprint != version.Fingerprint {
		t.Fatalf("version sandbox request=%#v", sandbox.requests)
	}
	if version, err = server.configuration.Validate(context.Background(), version.ID, "sandbox-author"); err != nil {
		t.Fatal(err)
	}
	evaluationBody := map[string]any{
		"schema_version": "agent-eval-suite-v1", "name": "studio-smoke", "version": "v1",
		"cases": []map[string]any{{
			"schema_version": "agent-eval-case-v1", "id": "synthetic-answer", "tags": []string{"synthetic"},
			"given":  map[string]any{"module": "work", "user_message": "合成评测", "tools": []any{}},
			"expect": map[string]any{"status": "completed", "outcome": "completed", "max_model_calls": 4, "max_steps": 8, "response_contains": []string{"合成测试"}},
			"tape":   map[string]any{},
		}},
	}
	evaluationPath := "/v1/ops/agent-versions/" + version.ID + "/evaluations"
	forbiddenEvaluation := performOperatorJSONWithOTP(t, server, http.MethodPost, evaluationPath, "sandbox-viewer-token", "", viewerOTP, evaluationBody)
	if forbiddenEvaluation.Code != http.StatusForbidden {
		t.Fatalf("viewer evaluation=%d %s", forbiddenEvaluation.Code, forbiddenEvaluation.Body.String())
	}
	evaluation := performOperatorJSONWithOTP(t, server, http.MethodPost, evaluationPath, "sandbox-support-token", "", supportOTP, evaluationBody)
	if evaluation.Code != http.StatusCreated {
		t.Fatalf("evaluation=%d %s", evaluation.Code, evaluation.Body.String())
	}
	var evaluationEnvelope struct {
		Run controlplane.AgentEvaluationRun `json:"evaluation_run"`
	}
	if err = json.Unmarshal(evaluation.Body.Bytes(), &evaluationEnvelope); err != nil || evaluationEnvelope.Run.ID == "" || evaluationEnvelope.Run.Decision != "pass" {
		t.Fatalf("evaluation response=%#v, %v", evaluationEnvelope, err)
	}
	listed := performOperatorJSONWithOTP(t, server, http.MethodGet, evaluationPath, "sandbox-viewer-token", "", viewerOTP, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), evaluationEnvelope.Run.ID) {
		t.Fatalf("evaluation list=%d %s", listed.Code, listed.Body.String())
	}
	loaded := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/evaluation-runs/"+evaluationEnvelope.Run.ID, "sandbox-viewer-token", "", viewerOTP, nil)
	if loaded.Code != http.StatusOK || !strings.Contains(loaded.Body.String(), `"suite_fingerprint"`) {
		t.Fatalf("evaluation get=%d %s", loaded.Code, loaded.Body.String())
	}
}

func sandboxAgentDefinition() map[string]any {
	return map[string]any{
		"display_name": "Work Assistant", "description": "Synthetic sandbox graph",
		"modules": []string{"work"}, "model_profile": "production-default", "entry_node": "route",
		"nodes": []map[string]any{
			{"key": "route", "type": "router", "model_role": "router", "prompt_template": "Route safely."},
			{"key": "answer", "type": "model", "model_role": "responder", "prompt_template": "Answer safely."},
			{"key": "done", "type": "end"},
		},
		"edges": []map[string]any{
			{"from": "route", "to": "answer", "condition": "respond"},
			{"from": "route", "to": "done", "condition": "stop"},
			{"from": "answer", "to": "done"},
		},
		"budget": map[string]any{"max_steps": 8, "max_model_calls": 4, "max_tool_calls": 0, "max_total_tokens": 4096, "timeout_ms": 60000},
	}
}
