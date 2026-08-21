package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
)

var agentToolWakeLatencyBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5}

type agentWorkerMetrics struct {
	mu                  sync.RWMutex
	dispatchHints       map[agent.RunDispatchHintOutcome]uint64
	dispatchCompletions map[string]uint64
	dispatchReplays     uint64
	pythonExecutions    map[string]uint64
	pythonModelCalls    uint64
	canaryRuns          map[string]struct{}
	canaryDispatchHints map[agent.RunDispatchHintOutcome]uint64
	canaryCompletions   map[string]uint64
	canaryPythonRuns    uint64
	canaryModelCalls    uint64
	canaryToolWakes     map[string]uint64
	canaryWakeLatency   []uint64
	canaryWakeSum       float64
	toolWakeEvents      map[string]map[string]uint64
	toolAwakenedRuns    map[string]uint64
	toolWakeLatency     []uint64
	toolWakeLatencySum  float64
}

func newAgentWorkerMetrics() *agentWorkerMetrics {
	return &agentWorkerMetrics{
		dispatchHints:       make(map[agent.RunDispatchHintOutcome]uint64),
		dispatchCompletions: make(map[string]uint64),
		pythonExecutions:    make(map[string]uint64),
		canaryRuns:          make(map[string]struct{}),
		canaryDispatchHints: make(map[agent.RunDispatchHintOutcome]uint64),
		canaryCompletions:   make(map[string]uint64),
		canaryToolWakes:     make(map[string]uint64),
		canaryWakeLatency:   make([]uint64, len(agentToolWakeLatencyBuckets)+1),
		toolWakeEvents:      make(map[string]map[string]uint64),
		toolAwakenedRuns:    make(map[string]uint64),
		toolWakeLatency:     make([]uint64, len(agentToolWakeLatencyBuckets)+1),
	}
}

func (m *agentWorkerMetrics) trackCanaryRun(runID string, active bool) {
	if m == nil || strings.TrimSpace(runID) == "" {
		return
	}
	m.mu.Lock()
	runID = strings.TrimSpace(runID)
	if active {
		m.canaryRuns[runID] = struct{}{}
	} else {
		delete(m.canaryRuns, runID)
	}
	m.mu.Unlock()
}

func (m *agentWorkerMetrics) observePythonRuntime(observation agent.PythonRuntimeObservation) {
	if m == nil {
		return
	}
	outcome := "succeeded"
	if !observation.Success {
		outcome = "failed"
	}
	m.mu.Lock()
	m.pythonExecutions[outcome]++
	if observation.ModelCalls > 0 {
		m.pythonModelCalls += uint64(observation.ModelCalls)
	}
	if _, canary := m.canaryRuns[observation.RunID]; canary {
		m.canaryPythonRuns++
		if observation.ModelCalls > 0 {
			m.canaryModelCalls += uint64(observation.ModelCalls)
		}
	}
	m.mu.Unlock()
}

func (m *agentWorkerMetrics) observeDispatchHint(observation agent.RunDispatchHintObservation) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.dispatchHints[observation.Outcome]++
	if _, canary := m.canaryRuns[observation.RunID]; canary {
		m.canaryDispatchHints[observation.Outcome]++
	}
	m.mu.Unlock()
}

func (m *agentWorkerMetrics) observeDispatchCompletion(observation agent.RunDispatchObservation) {
	if m == nil {
		return
	}
	outcome := "processed"
	if observation.Err != nil {
		outcome = "error"
	} else if !observation.Processed {
		outcome = "not_claimed"
	}
	m.mu.Lock()
	m.dispatchCompletions[outcome]++
	if _, canary := m.canaryRuns[observation.RunID]; canary {
		m.canaryCompletions[outcome]++
		delete(m.canaryRuns, observation.RunID)
	}
	if observation.Replay {
		m.dispatchReplays++
	}
	m.mu.Unlock()
}

func (m *agentWorkerMetrics) observeToolWake(observation toolWakeObservation) {
	if m == nil {
		return
	}
	eventType := strings.TrimSpace(observation.EventType)
	if eventType == "" {
		eventType = "unknown"
	}
	outcome := strings.TrimSpace(observation.Outcome)
	if outcome == "" {
		outcome = "unknown"
	}
	latency := observation.WakeLatency.Seconds()
	if latency < 0 {
		latency = 0
	}
	m.mu.Lock()
	if m.toolWakeEvents[eventType] == nil {
		m.toolWakeEvents[eventType] = make(map[string]uint64)
	}
	m.toolWakeEvents[eventType][outcome]++
	m.toolAwakenedRuns[eventType] += uint64(observation.AwakenedRuns)
	bucket := len(agentToolWakeLatencyBuckets)
	for index, upperBound := range agentToolWakeLatencyBuckets {
		if latency <= upperBound {
			bucket = index
			break
		}
	}
	m.toolWakeLatency[bucket]++
	m.toolWakeLatencySum += latency
	if observation.Canary {
		m.canaryToolWakes[outcome]++
		m.canaryWakeLatency[bucket]++
		m.canaryWakeSum += latency
	}
	m.mu.Unlock()
}

func (m *agentWorkerMetrics) prometheus(stats agent.RunDispatcherStats) string {
	if m == nil {
		m = newAgentWorkerMetrics()
	}
	m.mu.RLock()
	dispatchHints := make(map[agent.RunDispatchHintOutcome]uint64, len(m.dispatchHints))
	for outcome, count := range m.dispatchHints {
		dispatchHints[outcome] = count
	}
	dispatchCompletions := make(map[string]uint64, len(m.dispatchCompletions))
	for outcome, count := range m.dispatchCompletions {
		dispatchCompletions[outcome] = count
	}
	dispatchReplays := m.dispatchReplays
	pythonExecutions := make(map[string]uint64, len(m.pythonExecutions))
	for outcome, count := range m.pythonExecutions {
		pythonExecutions[outcome] = count
	}
	pythonModelCalls := m.pythonModelCalls
	canaryDispatchHints := make(map[agent.RunDispatchHintOutcome]uint64, len(m.canaryDispatchHints))
	for outcome, count := range m.canaryDispatchHints {
		canaryDispatchHints[outcome] = count
	}
	canaryCompletions := make(map[string]uint64, len(m.canaryCompletions))
	for outcome, count := range m.canaryCompletions {
		canaryCompletions[outcome] = count
	}
	canaryPythonRuns := m.canaryPythonRuns
	canaryModelCalls := m.canaryModelCalls
	canaryToolWakes := make(map[string]uint64, len(m.canaryToolWakes))
	for outcome, count := range m.canaryToolWakes {
		canaryToolWakes[outcome] = count
	}
	canaryWakeLatency := append([]uint64(nil), m.canaryWakeLatency...)
	canaryWakeSum := m.canaryWakeSum
	toolWakeEvents := make(map[string]map[string]uint64, len(m.toolWakeEvents))
	for eventType, outcomes := range m.toolWakeEvents {
		toolWakeEvents[eventType] = make(map[string]uint64, len(outcomes))
		for outcome, count := range outcomes {
			toolWakeEvents[eventType][outcome] = count
		}
	}
	toolAwakenedRuns := make(map[string]uint64, len(m.toolAwakenedRuns))
	for eventType, count := range m.toolAwakenedRuns {
		toolAwakenedRuns[eventType] = count
	}
	toolWakeLatency := append([]uint64(nil), m.toolWakeLatency...)
	toolWakeLatencySum := m.toolWakeLatencySum
	m.mu.RUnlock()

	var output strings.Builder
	output.WriteString("# HELP ai_companion_agent_worker_up Whether the Agent Worker process is serving metrics.\n")
	output.WriteString("# TYPE ai_companion_agent_worker_up gauge\n")
	output.WriteString("ai_companion_agent_worker_up 1\n")
	output.WriteString("# HELP ai_companion_agent_dispatch_hints_total Durable run hints by in-memory deduplication outcome.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_hints_total counter\n")
	for _, outcome := range []agent.RunDispatchHintOutcome{
		agent.RunDispatchHintEnqueued,
		agent.RunDispatchHintCoalesced,
		agent.RunDispatchHintReplayRequested,
	} {
		fmt.Fprintf(&output, "ai_companion_agent_dispatch_hints_total{outcome=%q} %d\n", outcome, dispatchHints[outcome])
	}
	output.WriteString("# HELP ai_companion_agent_dispatch_completions_total Run dispatch completions by claim outcome.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_completions_total counter\n")
	for _, outcome := range []string{"processed", "not_claimed", "error"} {
		fmt.Fprintf(&output, "ai_companion_agent_dispatch_completions_total{outcome=%q} %d\n", outcome, dispatchCompletions[outcome])
	}
	output.WriteString("# HELP ai_companion_agent_dispatch_replays_total Executed trailing replays retained for in-flight run updates.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_replays_total counter\n")
	fmt.Fprintf(&output, "ai_companion_agent_dispatch_replays_total %d\n", dispatchReplays)
	output.WriteString("# HELP ai_companion_agent_python_executions_total Python runtime executions by outcome.\n")
	output.WriteString("# TYPE ai_companion_agent_python_executions_total counter\n")
	for _, outcome := range []string{"succeeded", "failed"} {
		fmt.Fprintf(&output, "ai_companion_agent_python_executions_total{outcome=%q} %d\n", outcome, pythonExecutions[outcome])
	}
	output.WriteString("# HELP ai_companion_agent_python_model_calls_total Model calls reported by completed Python runtime executions.\n")
	output.WriteString("# TYPE ai_companion_agent_python_model_calls_total counter\n")
	fmt.Fprintf(&output, "ai_companion_agent_python_model_calls_total %d\n", pythonModelCalls)
	output.WriteString("# HELP ai_companion_agent_canary_dispatch_hints_total Synthetic canary run hints by in-memory deduplication outcome.\n")
	output.WriteString("# TYPE ai_companion_agent_canary_dispatch_hints_total counter\n")
	for _, outcome := range []agent.RunDispatchHintOutcome{
		agent.RunDispatchHintEnqueued,
		agent.RunDispatchHintCoalesced,
		agent.RunDispatchHintReplayRequested,
	} {
		fmt.Fprintf(&output, "ai_companion_agent_canary_dispatch_hints_total{outcome=%q} %d\n", outcome, canaryDispatchHints[outcome])
	}
	output.WriteString("# HELP ai_companion_agent_canary_dispatch_completions_total Synthetic canary run hints completed by outcome.\n")
	output.WriteString("# TYPE ai_companion_agent_canary_dispatch_completions_total counter\n")
	for _, outcome := range []string{"processed", "not_claimed", "error"} {
		fmt.Fprintf(&output, "ai_companion_agent_canary_dispatch_completions_total{outcome=%q} %d\n", outcome, canaryCompletions[outcome])
	}
	output.WriteString("# HELP ai_companion_agent_canary_python_executions_total Python executions attributable to synthetic canary run IDs.\n")
	output.WriteString("# TYPE ai_companion_agent_canary_python_executions_total counter\n")
	fmt.Fprintf(&output, "ai_companion_agent_canary_python_executions_total %d\n", canaryPythonRuns)
	output.WriteString("# HELP ai_companion_agent_canary_model_calls_total Model calls attributable to synthetic canary run IDs.\n")
	output.WriteString("# TYPE ai_companion_agent_canary_model_calls_total counter\n")
	fmt.Fprintf(&output, "ai_companion_agent_canary_model_calls_total %d\n", canaryModelCalls)
	output.WriteString("# HELP ai_companion_agent_canary_tool_wake_events_total Synthetic terminal Skill events handled by the Agent Worker.\n")
	output.WriteString("# TYPE ai_companion_agent_canary_tool_wake_events_total counter\n")
	for _, outcome := range []string{"awakened", "no_match", "error", "unknown"} {
		fmt.Fprintf(&output, "ai_companion_agent_canary_tool_wake_events_total{outcome=%q} %d\n", outcome, canaryToolWakes[outcome])
	}
	output.WriteString("# HELP ai_companion_agent_canary_tool_wake_latency_seconds Synthetic terminal Skill event wake latency.\n")
	output.WriteString("# TYPE ai_companion_agent_canary_tool_wake_latency_seconds histogram\n")
	var canaryCumulative uint64
	for index, upperBound := range agentToolWakeLatencyBuckets {
		canaryCumulative += canaryWakeLatency[index]
		fmt.Fprintf(&output, "ai_companion_agent_canary_tool_wake_latency_seconds_bucket{le=%q} %d\n", fmt.Sprintf("%g", upperBound), canaryCumulative)
	}
	canaryCumulative += canaryWakeLatency[len(canaryWakeLatency)-1]
	fmt.Fprintf(&output, "ai_companion_agent_canary_tool_wake_latency_seconds_bucket{le=%q} %d\n", "+Inf", canaryCumulative)
	fmt.Fprintf(&output, "ai_companion_agent_canary_tool_wake_latency_seconds_sum %.6f\n", canaryWakeSum)
	fmt.Fprintf(&output, "ai_companion_agent_canary_tool_wake_latency_seconds_count %d\n", canaryCumulative)
	output.WriteString("# HELP ai_companion_agent_dispatch_queue_depth Run hints currently waiting in the bounded dispatch queue.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_queue_depth gauge\n")
	fmt.Fprintf(&output, "ai_companion_agent_dispatch_queue_depth %d\n", stats.Queued)
	output.WriteString("# HELP ai_companion_agent_dispatch_pending Runs represented in the deduplicating dispatcher.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_pending gauge\n")
	fmt.Fprintf(&output, "ai_companion_agent_dispatch_pending %d\n", stats.Pending)
	output.WriteString("# HELP ai_companion_agent_dispatch_in_flight Runs currently executing in the dispatcher.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_in_flight gauge\n")
	fmt.Fprintf(&output, "ai_companion_agent_dispatch_in_flight %d\n", stats.InFlight)
	output.WriteString("# HELP ai_companion_agent_dispatch_replays_pending In-flight runs retaining one trailing replay.\n")
	output.WriteString("# TYPE ai_companion_agent_dispatch_replays_pending gauge\n")
	fmt.Fprintf(&output, "ai_companion_agent_dispatch_replays_pending %d\n", stats.ReplaysPending)
	output.WriteString("# HELP ai_companion_agent_tool_wake_events_total Terminal Skill events handled by type and wake outcome.\n")
	output.WriteString("# TYPE ai_companion_agent_tool_wake_events_total counter\n")
	eventTypes := sortedKeys(toolWakeEvents)
	for _, eventType := range eventTypes {
		outcomes := sortedKeys(toolWakeEvents[eventType])
		for _, outcome := range outcomes {
			fmt.Fprintf(&output, "ai_companion_agent_tool_wake_events_total{event_type=%q,outcome=%q} %d\n", eventType, outcome, toolWakeEvents[eventType][outcome])
		}
	}
	output.WriteString("# HELP ai_companion_agent_tool_wake_awakened_runs_total Waiting Agent runs made runnable by terminal Skill events.\n")
	output.WriteString("# TYPE ai_companion_agent_tool_wake_awakened_runs_total counter\n")
	for _, eventType := range sortedKeys(toolAwakenedRuns) {
		fmt.Fprintf(&output, "ai_companion_agent_tool_wake_awakened_runs_total{event_type=%q} %d\n", eventType, toolAwakenedRuns[eventType])
	}
	output.WriteString("# HELP ai_companion_agent_tool_wake_latency_seconds Time from terminal Skill event occurrence through durable wake and dispatch enqueue.\n")
	output.WriteString("# TYPE ai_companion_agent_tool_wake_latency_seconds histogram\n")
	var cumulative uint64
	for index, upperBound := range agentToolWakeLatencyBuckets {
		cumulative += toolWakeLatency[index]
		fmt.Fprintf(&output, "ai_companion_agent_tool_wake_latency_seconds_bucket{le=%q} %d\n", fmt.Sprintf("%g", upperBound), cumulative)
	}
	cumulative += toolWakeLatency[len(toolWakeLatency)-1]
	fmt.Fprintf(&output, "ai_companion_agent_tool_wake_latency_seconds_bucket{le=%q} %d\n", "+Inf", cumulative)
	fmt.Fprintf(&output, "ai_companion_agent_tool_wake_latency_seconds_sum %.6f\n", toolWakeLatencySum)
	fmt.Fprintf(&output, "ai_companion_agent_tool_wake_latency_seconds_count %d\n", cumulative)
	return output.String()
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type agentMetricsServer struct {
	server *http.Server
	ready  atomic.Bool
}

func newAgentMetricsServer(address string, metrics *agentWorkerMetrics, stats func() agent.RunDispatcherStats) *agentMetricsServer {
	if stats == nil {
		stats = func() agent.RunDispatcherStats { return agent.RunDispatcherStats{} }
	}
	mux := http.NewServeMux()
	result := &agentMetricsServer{}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if !result.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(metrics.prometheus(stats())))
	})
	result.server = &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return result
}

func (s *agentMetricsServer) markReady() { s.ready.Store(true) }

func (s *agentMetricsServer) run(ctx context.Context, shutdownTimeout time.Duration) error {
	shutdownComplete := make(chan struct{})
	go func() {
		defer close(shutdownComplete)
		<-ctx.Done()
		s.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}()
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownComplete
		return nil
	}
	return err
}
