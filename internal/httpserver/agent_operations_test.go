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

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

const operatorAgentRunID = "11111111-1111-4111-8111-111111111111"

type testAgentOperationsStore struct {
	run       agent.Run
	summaries []agent.RunSummary
	events    []agent.Event
	tools     []agent.ToolCallSummary
	filter    agent.RunOperationsFilter
}

func (s *testAgentOperationsStore) ListAgentRuns(_ context.Context, filter agent.RunOperationsFilter) ([]agent.RunSummary, error) {
	s.filter = filter
	return append([]agent.RunSummary(nil), s.summaries...), nil
}

func (s *testAgentOperationsStore) GetAgentRun(_ context.Context, runID string) (agent.Run, error) {
	if runID != s.run.ID {
		return agent.Run{}, agent.ErrNotFound
	}
	return s.run, nil
}

func (s *testAgentOperationsStore) ListAgentRunEvents(_ context.Context, runID string, _ int64, _ int) ([]agent.Event, error) {
	if runID != s.run.ID {
		return nil, agent.ErrNotFound
	}
	return append([]agent.Event(nil), s.events...), nil
}

func (s *testAgentOperationsStore) ListAgentToolCalls(_ context.Context, runID string) ([]agent.ToolCallSummary, error) {
	if runID != s.run.ID {
		return nil, agent.ErrNotFound
	}
	return append([]agent.ToolCallSummary(nil), s.tools...), nil
}

func TestOperatorAgentRunsRequireAuthAndAcceptFilters(t *testing.T) {
	now := time.Date(2026, 9, 5, 9, 30, 0, 0, time.UTC)
	store := operatorAgentOperationsFixture(now)
	server := newAgentOperationsServer(store)

	unauthorized := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/agent-runs", "", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	from := now.Add(-time.Hour).Format(time.RFC3339)
	response := performOperatorJSON(t, server, http.MethodGet,
		"/v1/ops/agent-runs?status=completed&module=work&from="+from+"&q="+operatorAgentRunID+"&limit=1",
		"ops-token", "sre-agent", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), operatorAgentRunID) || !strings.Contains(response.Body.String(), `"next_cursor":"`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if store.filter.Status != "completed" || store.filter.Module != "work" || store.filter.Query != operatorAgentRunID || store.filter.Limit != 1 || store.filter.CreatedFrom == nil {
		t.Fatalf("unexpected filter: %+v", store.filter)
	}

	invalid := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/agent-runs?status=unknown", "ops-token", "sre-agent", nil)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
}

func TestOperatorAgentRunDetailRedactsUserContent(t *testing.T) {
	now := time.Date(2026, 9, 5, 9, 30, 0, 0, time.UTC)
	store := operatorAgentOperationsFixture(now)
	server := newAgentOperationsServer(store)

	response := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/agent-runs/"+operatorAgentRunID, "ops-token", "sre-agent", nil)
	body := response.Body.String()
	if response.Code != http.StatusOK {
		t.Fatalf("response=%d %s", response.Code, body)
	}
	for _, expected := range []string{`"graph_node":"compose"`, `"node":"compose"`, `"type":"completed"`, `"tool_name":"work_query"`, `"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %s in %s", expected, body)
		}
	}
	for _, secret := range []string{"private user prompt", "private model response", "confirmation-secret", "private event output", "private-langfuse-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("sensitive value %q leaked in %s", secret, body)
		}
	}

	invalid := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/agent-runs/not-a-uuid", "ops-token", "sre-agent", nil)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
}

func operatorAgentOperationsFixture(now time.Time) *testAgentOperationsStore {
	completed := now.Add(2 * time.Second)
	output := json.RawMessage(`{
		"response":"private model response",
		"confirmation_token":"confirmation-secret",
		"graph":{"name":"test-agent","version":"1.0.0","tool_catalog_fingerprint":"abc"},
		"budget":{"limits":{"max_model_calls":4},"usage":{"model_calls":1}},
		"model":{"manifest":{"provider":"openrouter","roles":{"composer":{"models":["model-a"]}}},"calls":[{"graph_node":"compose","role":"composer","status":"succeeded","provider":"openrouter","returned_model":"model-a","prompt_tokens":10,"completion_tokens":4,"cost_micros":42}]},
		"observability":{"node_trace":[{"node":"compose","status":"succeeded","duration_ms":125,"details":{"route":"direct","user_message":"private user prompt"}}],"langfuse":{"enabled":true,"trace_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","initialized":true,"capture_content":false,"environment":"test","secret":"private-langfuse-secret"}}
	}`)
	run := agent.Run{ID: operatorAgentRunID, Output: output, CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed}
	return &testAgentOperationsStore{
		run: run,
		summaries: []agent.RunSummary{{
			ID: operatorAgentRunID, ThreadID: operatorAgentRunID,
			UserID: "22222222-2222-4222-8222-222222222222", ConversationID: "33333333-3333-4333-8333-333333333333",
			Module: "work", GraphName: "test-agent", GraphVersion: "1.0.0", Status: "completed",
			CurrentNode: "compose", Revision: 2, ModelCalls: 1, PromptTokens: 10,
			CompletionTokens: 4, CostMicros: 42, ToolCalls: 1, DurationMS: 2000,
			CreatedAt: now, UpdatedAt: completed, CompletedAt: &completed, DeadlineAt: now.Add(15 * time.Minute),
		}},
		events: []agent.Event{{
			ID: 1, RunID: operatorAgentRunID, Sequence: 1, Type: "completed", CreatedAt: completed,
			Payload: json.RawMessage(`{"revision":2,"output":{"response":"private event output"}}`),
		}},
		tools: []agent.ToolCallSummary{{
			ID: "44444444-4444-4444-8444-444444444444", RunID: operatorAgentRunID,
			ToolName: "work_query", RiskLevel: "none", Status: "succeeded",
			CreatedAt: now.Add(time.Second), UpdatedAt: completed, CompletedAt: &completed,
		}},
	}
}

func newAgentOperationsServer(store agent.OperationsStore) *Server {
	server := New(config.Config{
		HTTPAddr: ":0", ServiceName: "test", Environment: "test",
		AuthTokenSecret: "ops-agent-secret-with-enough-entropy", OperatorToken: "ops-token",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetAgentOperationsStore(store)
	return server
}
