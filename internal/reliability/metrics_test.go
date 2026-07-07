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
	text := metrics.Prometheus(Snapshot{Level: "L2", QueueLag: 42, OldestJobAgeMS: 12500, ModelErrorRate: 0.25, P95LatencyMS: 3200})
	for _, expected := range []string{
		`ai_companion_http_requests_total{method="GET",route="/v1/skill-runs/{run_id}",status="200"} 2`,
		`ai_companion_http_request_duration_seconds_total{method="GET",route="/v1/skill-runs/{run_id}",status="200"} 1.000000`,
		"ai_companion_degradation_level 2", "ai_companion_queue_lag 42", "ai_companion_oldest_job_age_seconds 12.500",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, text)
		}
	}
}
