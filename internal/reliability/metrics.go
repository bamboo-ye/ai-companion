package reliability

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type requestMetric struct {
	Count    uint64
	Duration time.Duration
}

type Metrics struct {
	mu       sync.RWMutex
	requests map[string]requestMetric
}

func NewMetrics() *Metrics { return &Metrics{requests: make(map[string]requestMetric)} }

func (m *Metrics) ObserveRequest(method, route string, status int, duration time.Duration) {
	key := fmt.Sprintf("%s\x00%s\x00%d", method, route, status)
	m.mu.Lock()
	metric := m.requests[key]
	metric.Count++
	metric.Duration += duration
	m.requests[key] = metric
	m.mu.Unlock()
}

func (m *Metrics) Prometheus(snapshot Snapshot) string {
	m.mu.RLock()
	keys := make([]string, 0, len(m.requests))
	for key := range m.requests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make(map[string]requestMetric, len(m.requests))
	for key, value := range m.requests {
		items[key] = value
	}
	m.mu.RUnlock()
	var output strings.Builder
	output.WriteString("# HELP ai_companion_http_requests_total HTTP requests by route and status.\n# TYPE ai_companion_http_requests_total counter\n")
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		metric := items[key]
		fmt.Fprintf(&output, "ai_companion_http_requests_total{method=%q,route=%q,status=%q} %d\n", parts[0], parts[1], parts[2], metric.Count)
	}
	output.WriteString("# HELP ai_companion_http_request_duration_seconds_total Cumulative HTTP request duration.\n# TYPE ai_companion_http_request_duration_seconds_total counter\n")
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		metric := items[key]
		fmt.Fprintf(&output, "ai_companion_http_request_duration_seconds_total{method=%q,route=%q,status=%q} %.6f\n", parts[0], parts[1], parts[2], metric.Duration.Seconds())
	}
	output.WriteString("# HELP ai_companion_degradation_level Current degradation level from 0 to 3.\n# TYPE ai_companion_degradation_level gauge\n")
	level := 0
	_, _ = fmt.Sscanf(snapshot.Level, "L%d", &level)
	fmt.Fprintf(&output, "ai_companion_degradation_level %d\n", level)
	output.WriteString("# HELP ai_companion_queue_lag Runnable durable jobs.\n# TYPE ai_companion_queue_lag gauge\n")
	fmt.Fprintf(&output, "ai_companion_queue_lag %d\n", snapshot.QueueLag)
	output.WriteString("# HELP ai_companion_oldest_job_age_seconds Age of the oldest runnable durable job.\n# TYPE ai_companion_oldest_job_age_seconds gauge\n")
	fmt.Fprintf(&output, "ai_companion_oldest_job_age_seconds %.3f\n", float64(snapshot.OldestJobAgeMS)/1000)
	output.WriteString("# HELP ai_companion_model_error_ratio Recent failed or timed-out generation ratio.\n# TYPE ai_companion_model_error_ratio gauge\n")
	fmt.Fprintf(&output, "ai_companion_model_error_ratio %.6f\n", snapshot.ModelErrorRate)
	output.WriteString("# HELP ai_companion_model_latency_p95_seconds Recent model latency p95.\n# TYPE ai_companion_model_latency_p95_seconds gauge\n")
	fmt.Fprintf(&output, "ai_companion_model_latency_p95_seconds %.3f\n", float64(snapshot.P95LatencyMS)/1000)
	output.WriteString("# HELP ai_companion_model_calls_recent Model calls persisted in the last five minutes.\n# TYPE ai_companion_model_calls_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_model_calls_recent %d\n", snapshot.ModelUsage.Calls)
	output.WriteString("# HELP ai_companion_model_prompt_tokens_recent Prompt tokens persisted in the last five minutes.\n# TYPE ai_companion_model_prompt_tokens_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_model_prompt_tokens_recent %d\n", snapshot.ModelUsage.PromptTokens)
	output.WriteString("# HELP ai_companion_model_completion_tokens_recent Completion tokens persisted in the last five minutes.\n# TYPE ai_companion_model_completion_tokens_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_model_completion_tokens_recent %d\n", snapshot.ModelUsage.CompletionTokens)
	output.WriteString("# HELP ai_companion_model_cost_micros_recent Model cost in micro-USD persisted in the last five minutes.\n# TYPE ai_companion_model_cost_micros_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_model_cost_micros_recent %d\n", snapshot.ModelUsage.CostMicros)
	output.WriteString("# HELP ai_companion_tool_repairs_recent Tool repair attempts persisted in the last five minutes.\n# TYPE ai_companion_tool_repairs_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_tool_repairs_recent{outcome=%q} %d\n", "attempted", snapshot.Repairs.Attempts)
	fmt.Fprintf(&output, "ai_companion_tool_repairs_recent{outcome=%q} %d\n", "succeeded", snapshot.Repairs.Succeeded)
	fmt.Fprintf(&output, "ai_companion_tool_repairs_recent{outcome=%q} %d\n", "blocked", snapshot.Repairs.Blocked)
	output.WriteString("# HELP ai_companion_tool_repair_model_calls_recent Repair planner model calls persisted in the last five minutes.\n# TYPE ai_companion_tool_repair_model_calls_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_tool_repair_model_calls_recent %d\n", snapshot.Repairs.ModelCalls)
	output.WriteString("# HELP ai_companion_tool_repair_cost_micros_recent Repair planner model cost in micro-USD persisted in the last five minutes.\n# TYPE ai_companion_tool_repair_cost_micros_recent gauge\n")
	fmt.Fprintf(&output, "ai_companion_tool_repair_cost_micros_recent %d\n", snapshot.Repairs.ModelCostMicros)
	output.WriteString("# HELP ai_companion_agent_runs Agent runs by active status or terminal status observed in the last five minutes.\n# TYPE ai_companion_agent_runs gauge\n")
	for _, item := range []struct {
		status string
		value  int
	}{
		{"accepted", snapshot.AgentRuns.Accepted},
		{"queued", snapshot.AgentRuns.Queued},
		{"running", snapshot.AgentRuns.Running},
		{"waiting_approval", snapshot.AgentRuns.WaitingApproval},
		{"cancel_requested", snapshot.AgentRuns.CancelRequested},
		{"completed", snapshot.AgentRuns.CompletedRecent},
		{"failed", snapshot.AgentRuns.FailedRecent},
		{"cancelled", snapshot.AgentRuns.CancelledRecent},
		{"timed_out", snapshot.AgentRuns.TimedOutRecent},
	} {
		fmt.Fprintf(&output, "ai_companion_agent_runs{status=%q} %d\n", item.status, item.value)
	}
	output.WriteString("# HELP ai_companion_agent_run_duration_p95_seconds Agent run end-to-end duration p95 over the last five minutes.\n# TYPE ai_companion_agent_run_duration_p95_seconds gauge\n")
	fmt.Fprintf(&output, "ai_companion_agent_run_duration_p95_seconds %.3f\n", float64(snapshot.AgentRuns.P95DurationMS)/1000)
	output.WriteString("# HELP ai_companion_agent_execution_retries_recent Agent execution retries scheduled or settled in the last five minutes.\n# TYPE ai_companion_agent_execution_retries_recent gauge\n")
	for _, item := range []struct {
		outcome string
		value   int
	}{
		{"scheduled", snapshot.AgentRetries.ScheduledRecent},
		{"recovered", snapshot.AgentRetries.RecoveredRecent},
		{"exhausted", snapshot.AgentRetries.ExhaustedRecent},
		{"deadline_exhausted", snapshot.AgentRetries.DeadlineExhaustedRecent},
	} {
		fmt.Fprintf(&output, "ai_companion_agent_execution_retries_recent{outcome=%q} %d\n", item.outcome, item.value)
	}
	output.WriteString("# HELP ai_companion_agent_execution_retry_recovery_ratio Share of recently settled retrying runs that recovered.\n# TYPE ai_companion_agent_execution_retry_recovery_ratio gauge\n")
	fmt.Fprintf(&output, "ai_companion_agent_execution_retry_recovery_ratio %.6f\n", snapshot.AgentRetries.RecoveryRatio)
	return output.String()
}
