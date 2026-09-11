package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
)

func TestAgentWorkerMetricsPrometheusContract(t *testing.T) {
	metrics := newAgentWorkerMetrics()
	metrics.trackCanaryRun("canary-run", true)
	metrics.observeDispatchHint(agent.RunDispatchHintObservation{Outcome: agent.RunDispatchHintEnqueued})
	metrics.observeDispatchHint(agent.RunDispatchHintObservation{RunID: "canary-run", Outcome: agent.RunDispatchHintReplayRequested})
	metrics.observeDispatchHint(agent.RunDispatchHintObservation{RunID: "canary-run", Outcome: agent.RunDispatchHintCoalesced})
	metrics.observeDispatchCompletion(agent.RunDispatchObservation{Processed: true})
	metrics.observeDispatchCompletion(agent.RunDispatchObservation{RunID: "canary-run", Replay: true})
	metrics.observePythonRuntime(agent.PythonRuntimeObservation{Success: true, ModelCalls: 2})
	metrics.observePythonRuntime(agent.PythonRuntimeObservation{Success: false, ModelCalls: 1})
	metrics.observeModelConfiguration(modelConfigurationObservation{Outcome: "error"})
	metrics.observeModelConfiguration(modelConfigurationObservation{
		Outcome: "applied", Revision: 3, VersionID: "version-3",
		ConfigVersion: "routing-v3", Fingerprint: "abc123",
	})
	metrics.trackCanaryRun("canary-exec", true)
	metrics.observePythonRuntime(agent.PythonRuntimeObservation{RunID: "canary-exec", Success: true, ModelCalls: 2})
	metrics.observeDispatchCompletion(agent.RunDispatchObservation{RunID: "canary-exec", Processed: true})
	metrics.observeToolWake(toolWakeObservation{
		EventType: "skill.run.succeeded.v1", Outcome: "awakened",
		WakeLatency: 175 * time.Millisecond, AwakenedRuns: 2,
	})
	metrics.observeToolWake(toolWakeObservation{
		EventType: "skill.run.failed.v1", Outcome: "no_match",
		WakeLatency: 1250 * time.Millisecond, Canary: true,
	})

	output := metrics.prometheus(agent.RunDispatcherStats{
		Queued: 2, Pending: 4, InFlight: 2, ReplaysPending: 1,
	})
	for _, expected := range []string{
		`ai_companion_agent_dispatch_hints_total{outcome="enqueued"} 1`,
		`ai_companion_agent_dispatch_hints_total{outcome="coalesced"} 1`,
		`ai_companion_agent_dispatch_hints_total{outcome="replay_requested"} 1`,
		`ai_companion_agent_dispatch_completions_total{outcome="processed"} 2`,
		`ai_companion_agent_dispatch_completions_total{outcome="not_claimed"} 1`,
		`ai_companion_agent_dispatch_replays_total 1`,
		`ai_companion_agent_python_executions_total{outcome="succeeded"} 2`,
		`ai_companion_agent_python_executions_total{outcome="failed"} 1`,
		`ai_companion_agent_python_model_calls_total 5`,
		`ai_companion_agent_model_config_reloads_total{outcome="applied"} 1`,
		`ai_companion_agent_model_config_reloads_total{outcome="error"} 1`,
		`ai_companion_agent_model_config_revision 3`,
		`ai_companion_agent_model_config_info{config_version="routing-v3",version_id="version-3",fingerprint="abc123"} 1`,
		`ai_companion_agent_canary_dispatch_hints_total{outcome="coalesced"} 1`,
		`ai_companion_agent_canary_dispatch_hints_total{outcome="replay_requested"} 1`,
		`ai_companion_agent_canary_dispatch_completions_total{outcome="not_claimed"} 1`,
		`ai_companion_agent_canary_dispatch_completions_total{outcome="processed"} 1`,
		`ai_companion_agent_canary_python_executions_total 1`,
		`ai_companion_agent_canary_model_calls_total 2`,
		`ai_companion_agent_canary_tool_wake_events_total{outcome="no_match"} 1`,
		`ai_companion_agent_canary_tool_wake_latency_seconds_bucket{le="2"} 1`,
		`ai_companion_agent_canary_tool_wake_latency_seconds_count 1`,
		`ai_companion_agent_dispatch_queue_depth 2`,
		`ai_companion_agent_dispatch_pending 4`,
		`ai_companion_agent_dispatch_in_flight 2`,
		`ai_companion_agent_dispatch_replays_pending 1`,
		`ai_companion_agent_tool_wake_events_total{event_type="skill.run.failed.v1",outcome="no_match"} 1`,
		`ai_companion_agent_tool_wake_events_total{event_type="skill.run.succeeded.v1",outcome="awakened"} 1`,
		`ai_companion_agent_tool_wake_awakened_runs_total{event_type="skill.run.succeeded.v1"} 2`,
		`ai_companion_agent_tool_wake_latency_seconds_bucket{le="0.25"} 1`,
		`ai_companion_agent_tool_wake_latency_seconds_bucket{le="2"} 2`,
		`ai_companion_agent_tool_wake_latency_seconds_bucket{le="+Inf"} 2`,
		`ai_companion_agent_tool_wake_latency_seconds_count 2`,
		`ai_companion_agent_tool_wake_latency_seconds_sum 1.425000`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, output)
		}
	}
}

func TestAgentMetricsServerHealthReadinessAndMetrics(t *testing.T) {
	metrics := newAgentWorkerMetrics()
	server := newAgentMetricsServer(":0", metrics, func() agent.RunDispatcherStats {
		return agent.RunDispatcherStats{Queued: 3}
	})
	for _, test := range []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{path: "/healthz", wantStatus: http.StatusOK, wantBody: `"status":"ok"`},
		{path: "/readyz", wantStatus: http.StatusServiceUnavailable, wantBody: `"status":"not_ready"`},
		{path: "/metrics", wantStatus: http.StatusOK, wantBody: "ai_companion_agent_dispatch_queue_depth 3"},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		server.server.Handler.ServeHTTP(response, request)
		if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), test.wantBody) {
			t.Fatalf("%s = %d %q", test.path, response.Code, response.Body.String())
		}
	}
	server.markReady()
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("ready response = %d %q", response.Code, response.Body.String())
	}
}
