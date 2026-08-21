package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetricParsing(t *testing.T) {
	document := `# HELP sample ignored
sample_total{outcome="one"} 2
sample_total{outcome="two"} 3
sample_other 9
`
	if value := metricSum(document, "sample_total"); value != 5 {
		t.Fatalf("metricSum() = %v", value)
	}
	if value := metricValue(document, `sample_total{outcome="two"}`); value != 3 {
		t.Fatalf("metricValue() = %v", value)
	}
	if values := splitNonEmpty(" kafka:29092, ,127.0.0.1:9092 "); len(values) != 2 || values[0] != "kafka:29092" {
		t.Fatalf("splitNonEmpty() = %#v", values)
	}
}

func TestHistogramDeltaMaximumUsesNewBucketSamples(t *testing.T) {
	before := strings.Join([]string{
		`metric_bucket{le="0.1"} 2`,
		`metric_bucket{le="0.25"} 3`,
		`metric_bucket{le="0.5"} 3`,
		`metric_bucket{le="1"} 3`,
		`metric_bucket{le="2"} 3`,
		`metric_bucket{le="5"} 3`,
		`metric_bucket{le="+Inf"} 3`,
	}, "\n")
	after := strings.Join([]string{
		`metric_bucket{le="0.1"} 3`,
		`metric_bucket{le="0.25"} 5`,
		`metric_bucket{le="0.5"} 5`,
		`metric_bucket{le="1"} 5`,
		`metric_bucket{le="2"} 5`,
		`metric_bucket{le="5"} 5`,
		`metric_bucket{le="+Inf"} 5`,
	}, "\n")
	if value := histogramDeltaMaximum(before, after, "metric_bucket"); value != 0.25 {
		t.Fatalf("histogramDeltaMaximum() = %v", value)
	}
}

func TestEvaluateReportRejectsPythonModelLatencyAndCountFailures(t *testing.T) {
	baseline := canaryBaseline{
		SchemaVersion: canaryBaselineVersion, MinimumCycles: 5, MaximumCycles: 100,
		TerminalEventsPerCycle: 3, MaximumWakeLatencySeconds: 1,
		MaximumDurationSeconds: 60, RequiredCompletionOutcome: "not_claimed", RequiredTerminalOutcome: "no_match",
	}
	report := evaluateReport(baseline, strings.Repeat("a", 64), canarySummary{
		Cycles: 5, DispatchHints: 5, NotClaimed: 4, TerminalWakes: 14,
		TerminalNoMatches: 13, TerminalErrors: 1,
		WakeLatencyMaxSeconds: 2, PythonExecutions: 1, ModelCalls: 1, DurationMS: 61000,
	})
	if report.Decision != "fail" || len(report.Violations) != 8 {
		t.Fatalf("report = %#v", report)
	}
	codes := make(map[string]bool)
	for _, violation := range report.Violations {
		codes[violation.Code] = true
	}
	for _, code := range []string{"not_claimed_count", "terminal_wake_count", "terminal_no_match_count", "terminal_error_detected", "wake_latency_high", "python_execution_detected", "model_call_detected", "duration_high"} {
		if !codes[code] {
			t.Fatalf("missing violation %q: %#v", code, report.Violations)
		}
	}
}

func TestLoadBaselineAndWriteReports(t *testing.T) {
	baseline, digest, err := loadBaseline(filepath.Join("..", "..", "evals", "agent", "baselines", "observability-canary.v1.json"))
	if err != nil || baseline.SchemaVersion != canaryBaselineVersion || len(digest) != 64 {
		t.Fatalf("loadBaseline() = %#v %q %v", baseline, digest, err)
	}
	directory := t.TempDir()
	jsonPath := filepath.Join(directory, "report.json")
	xmlPath := filepath.Join(directory, "report.xml")
	report := evaluateReport(baseline, digest, canarySummary{Cycles: 5, DispatchHints: 5, NotClaimed: 5, TerminalWakes: 15, TerminalNoMatches: 15, WakeLatencyMaxSeconds: 0.25})
	if err = writeReports(jsonPath, xmlPath, report); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{jsonPath, xmlPath} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("report %s mode = %v, %v", path, info.Mode().Perm(), statErr)
		}
	}
	if math.IsInf(report.Summary.WakeLatencyMaxSeconds, 0) || report.Decision != "pass" {
		t.Fatalf("report = %#v", report)
	}
}

func TestWriteReportsRecordsFailingJUnit(t *testing.T) {
	baseline := canaryBaseline{
		SchemaVersion: canaryBaselineVersion, MinimumCycles: 5, MaximumCycles: 100,
		TerminalEventsPerCycle: 3, MaximumWakeLatencySeconds: 1, MaximumDurationSeconds: 60,
		RequiredCompletionOutcome: "not_claimed", RequiredTerminalOutcome: "no_match",
	}
	report := evaluateReport(baseline, strings.Repeat("b", 64), canarySummary{Cycles: 5, ModelCalls: 1})
	directory := t.TempDir()
	jsonPath := filepath.Join(directory, "failed.json")
	xmlPath := filepath.Join(directory, "failed.xml")
	if err := writeReports(jsonPath, xmlPath, report); err != nil {
		t.Fatal(err)
	}
	jsonBody, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	xmlBody, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(jsonBody), `"decision": "fail"`) ||
		!strings.Contains(string(jsonBody), `"model_call_detected"`) ||
		!strings.Contains(string(xmlBody), `failures="1"`) ||
		!strings.Contains(string(xmlBody), `canary gate failed`) {
		t.Fatalf("failure artifacts are incomplete:\n%s\n%s", jsonBody, xmlBody)
	}
}
