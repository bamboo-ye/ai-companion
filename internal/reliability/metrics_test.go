package reliability

import (
	"strings"
	"testing"
	"time"
)

func TestMetricsUsesBoundedRouteLabelsAndReliabilityGauges(t *testing.T) {
	metrics := NewMetrics()
	metrics.ObserveRequest("GET", "/v1/skill-runs/{run_id}", 200, 250*time.Millisecond)
	metrics.ObserveRequest("GET", "/v1/skill-runs/{run_id}", 200, 750*time.Millisecond)
	text := metrics.Prometheus(Snapshot{
		Level: "L2", QueueLag: 42, OldestJobAgeMS: 12500,
		ModelErrorRate: 0.25, P95LatencyMS: 3200,
		AgentRuns: AgentRunMetrics{
			Running: 3, TimedOutRecent: 2, P95DurationMS: 4500,
		},
		AgentRetries: AgentRetryMetrics{
			ScheduledRecent: 4, RecoveredRecent: 2, ExhaustedRecent: 1,
			DeadlineExhaustedRecent: 1, RecoveryRatio: 0.5,
		},
		ModelUsage: ModelUsageMetrics{
			Calls: 6, PromptTokens: 1000, CompletionTokens: 200, CostMicros: 45,
		},
		Repairs: RepairMetrics{Attempts: 3, Succeeded: 2, Blocked: 1, ModelCalls: 1, ModelCostMicros: 17},
	})
	for _, expected := range []string{
		`ai_companion_http_requests_total{method="GET",route="/v1/skill-runs/{run_id}",status="200"} 2`,
		`ai_companion_http_request_duration_seconds_total{method="GET",route="/v1/skill-runs/{run_id}",status="200"} 1.000000`,
		"ai_companion_degradation_level 2", "ai_companion_queue_lag 42", "ai_companion_oldest_job_age_seconds 12.500",
		"ai_companion_model_calls_recent 6",
		"ai_companion_model_prompt_tokens_recent 1000",
		"ai_companion_model_completion_tokens_recent 200",
		"ai_companion_model_cost_micros_recent 45",
		`ai_companion_tool_repairs_recent{outcome="attempted"} 3`,
		`ai_companion_tool_repairs_recent{outcome="succeeded"} 2`,
		`ai_companion_tool_repairs_recent{outcome="blocked"} 1`,
		"ai_companion_tool_repair_model_calls_recent 1",
		"ai_companion_tool_repair_cost_micros_recent 17",
		`ai_companion_agent_runs{status="running"} 3`,
		`ai_companion_agent_runs{status="timed_out"} 2`,
		"ai_companion_agent_run_duration_p95_seconds 4.500",
		`ai_companion_agent_execution_retries_recent{outcome="scheduled"} 4`,
		`ai_companion_agent_execution_retries_recent{outcome="recovered"} 2`,
		`ai_companion_agent_execution_retries_recent{outcome="exhausted"} 1`,
		`ai_companion_agent_execution_retries_recent{outcome="deadline_exhausted"} 1`,
		"ai_companion_agent_execution_retry_recovery_ratio 0.500000",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, text)
		}
	}
}
