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
	return output.String()
}
