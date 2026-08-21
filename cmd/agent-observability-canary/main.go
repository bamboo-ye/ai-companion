package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

const (
	canaryBaselineVersion = "agent-observability-canary-baseline-v1"
	canaryReportVersion   = "agent-observability-canary-report-v1"
)

type canaryBaseline struct {
	SchemaVersion               string  `json:"schema_version"`
	MinimumCycles               int     `json:"minimum_cycles"`
	MaximumCycles               int     `json:"maximum_cycles"`
	TerminalEventsPerCycle      int     `json:"terminal_events_per_cycle"`
	MaximumWakeLatencySeconds   float64 `json:"maximum_wake_latency_seconds"`
	MaximumDurationSeconds      float64 `json:"maximum_duration_seconds"`
	MaximumPythonExecutionDelta int     `json:"maximum_python_execution_delta"`
	MaximumModelCallDelta       int     `json:"maximum_model_call_delta"`
	RequiredCompletionOutcome   string  `json:"required_completion_outcome"`
	RequiredTerminalOutcome     string  `json:"required_terminal_outcome"`
}

type canaryViolation struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Expected string `json:"expected"`
	Actual   any    `json:"actual"`
}

type canarySummary struct {
	Cycles                int     `json:"cycles"`
	DispatchHints         int     `json:"dispatch_hints"`
	NotClaimed            int     `json:"not_claimed"`
	TerminalWakes         int     `json:"terminal_wakes"`
	TerminalNoMatches     int     `json:"terminal_no_matches"`
	TerminalErrors        int     `json:"terminal_errors"`
	WakeLatencyMaxSeconds float64 `json:"wake_latency_max_seconds"`
	PythonExecutions      int     `json:"python_executions"`
	ModelCalls            int     `json:"model_calls"`
	DurationMS            int     `json:"duration_ms"`
}

type canaryReport struct {
	SchemaVersion string `json:"schema_version"`
	GeneratedAt   string `json:"generated_at"`
	Decision      string `json:"decision"`
	Source        struct {
		BaselineVersion string `json:"baseline_version"`
		BaselineSHA256  string `json:"baseline_sha256"`
	} `json:"source"`
	Summary    canarySummary     `json:"summary"`
	Violations []canaryViolation `json:"violations"`
}

func main() {
	os.Exit(run())
}

func run() int {
	brokersValue := flag.String("brokers", "127.0.0.1:9092", "comma-separated Kafka brokers")
	metricsURL := flag.String("metrics-url", "http://127.0.0.1:9467/metrics", "Agent Worker metrics URL")
	cycles := flag.Int("cycles", 5, "number of finite mixed-traffic cycles")
	interval := flag.Duration("interval", time.Second, "delay between cycles")
	timeout := flag.Duration("timeout", 30*time.Second, "maximum wait for observed counters")
	baselinePath := flag.String("baseline", "evals/agent/baselines/observability-canary.v1.json", "versioned canary baseline")
	jsonReport := flag.String("json-report", "", "optional JSON report path")
	junitReport := flag.String("junit-report", "", "optional JUnit report path")
	flag.Parse()
	baseline, baselineDigest, err := loadBaseline(*baselinePath)
	if err != nil {
		log.Fatal(err)
	}
	if *cycles < baseline.MinimumCycles || *cycles > baseline.MaximumCycles {
		log.Fatalf("cycles must be between %d and %d", baseline.MinimumCycles, baseline.MaximumCycles)
	}
	if *interval < 0 || *timeout <= 0 {
		log.Fatal("interval must be non-negative and timeout must be positive")
	}
	brokers := splitNonEmpty(*brokersValue)
	if len(brokers) == 0 {
		log.Fatal("at least one Kafka broker is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+time.Duration(*cycles)*(*interval)+15*time.Second)
	defer cancel()
	publisher, err := eventbus.NewKafkaPublisher(brokers, "ai-companion-agent-observability-canary")
	if err != nil {
		log.Fatal(err)
	}
	defer publisher.Close()
	if err = publisher.Ping(ctx); err != nil {
		log.Fatalf("ping Kafka: %v", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	before, err := fetchMetrics(ctx, client, *metricsURL)
	if err != nil {
		log.Fatal(err)
	}
	beforeHints := metricSum(before, "ai_companion_agent_canary_dispatch_hints_total")
	beforeCompletions := metricSum(before, "ai_companion_agent_canary_dispatch_completions_total")
	beforeWakes := metricSum(before, "ai_companion_agent_canary_tool_wake_events_total")
	beforeNoMatches := metricValue(before, `ai_companion_agent_canary_tool_wake_events_total{outcome="no_match"}`)
	beforeTerminalErrors := metricValue(before, `ai_companion_agent_canary_tool_wake_events_total{outcome="error"}`)
	beforeNotClaimed := metricValue(before, `ai_companion_agent_canary_dispatch_completions_total{outcome="not_claimed"}`)
	beforePythonExecutions := metricValue(before, "ai_companion_agent_canary_python_executions_total")
	beforeModelCalls := metricValue(before, "ai_companion_agent_canary_model_calls_total")
	startedAt := time.Now()

	runIDs := make([]string, 0, *cycles)
	for cycle := 0; cycle < *cycles; cycle++ {
		runID := mustID()
		userID := mustID()
		runIDs = append(runIDs, runID)
		publish(ctx, publisher, "agent.run.requested.v1", "agent_run", runID, map[string]any{
			"run_id": runID, "canary": true,
		})
		for _, topic := range []string{
			"skill.run.succeeded.v1",
			"skill.run.failed.v1",
			"skill.run.cancelled.v1",
		} {
			taskID := mustID()
			publish(ctx, publisher, topic, "skill_run", taskID, map[string]any{
				"run_id": taskID, "user_id": userID, "canary": true,
			})
		}
		if cycle+1 < *cycles && *interval > 0 {
			timer := time.NewTimer(*interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				log.Fatal(ctx.Err())
			case <-timer.C:
			}
		}
	}

	deadline := time.Now().Add(*timeout)
	lastAfter := before
	var lastFetchErr error
	for {
		after, fetchErr := fetchMetrics(ctx, client, *metricsURL)
		if fetchErr == nil {
			lastAfter = after
			lastFetchErr = nil
			hintDelta := metricSum(after, "ai_companion_agent_canary_dispatch_hints_total") - beforeHints
			completionDelta := metricSum(after, "ai_companion_agent_canary_dispatch_completions_total") - beforeCompletions
			wakeDelta := metricSum(after, "ai_companion_agent_canary_tool_wake_events_total") - beforeWakes
			requiredWakes := float64(*cycles * baseline.TerminalEventsPerCycle)
			if hintDelta >= float64(*cycles) && completionDelta >= float64(*cycles) && wakeDelta >= requiredWakes {
				summary := summarizeCanaryMetrics(before, after, *cycles, startedAt, beforeHints, beforeWakes, beforeNoMatches, beforeTerminalErrors, beforeNotClaimed, beforePythonExecutions, beforeModelCalls)
				report := evaluateReport(baseline, baselineDigest, summary)
				if err = writeReports(*jsonReport, *junitReport, report); err != nil {
					log.Fatal(err)
				}
				fmt.Printf("agent_observability_canary=%s cycles=%d dispatch_hints=%d terminal_wakes=%d not_claimed=%d wake_max_seconds=%.3f python_executions=%d model_calls=%d canary_run_ids=%s\n",
					report.Decision, summary.Cycles, summary.DispatchHints, summary.TerminalWakes, summary.NotClaimed,
					summary.WakeLatencyMaxSeconds, summary.PythonExecutions, summary.ModelCalls, strings.Join(runIDs, ","))
				if report.Decision != "pass" {
					return 1
				}
				return 0
			}
		} else {
			lastFetchErr = fetchErr
		}
		if time.Now().After(deadline) {
			summary := summarizeCanaryMetrics(before, lastAfter, *cycles, startedAt, beforeHints, beforeWakes, beforeNoMatches, beforeTerminalErrors, beforeNotClaimed, beforePythonExecutions, beforeModelCalls)
			report := evaluateReport(baseline, baselineDigest, summary)
			actual := any("required metric deltas were not observed")
			if lastFetchErr != nil {
				actual = lastFetchErr.Error()
			}
			report.Violations = append(report.Violations, canaryViolation{
				Code: "metrics_timeout", Path: "/summary/collection",
				Expected: "dispatch, completion and terminal deltas before timeout", Actual: actual,
			})
			report.Decision = "fail"
			if err = writeReports(*jsonReport, *junitReport, report); err != nil {
				log.Fatal(err)
			}
			fmt.Printf("agent_observability_canary=fail cycles=%d dispatch_hints=%d terminal_wakes=%d not_claimed=%d wake_max_seconds=%.3f python_executions=%d model_calls=%d canary_run_ids=%s\n",
				summary.Cycles, summary.DispatchHints, summary.TerminalWakes, summary.NotClaimed,
				summary.WakeLatencyMaxSeconds, summary.PythonExecutions, summary.ModelCalls, strings.Join(runIDs, ","))
			return 1
		}
		select {
		case <-ctx.Done():
			log.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func loadBaseline(path string) (canaryBaseline, string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return canaryBaseline{}, "", fmt.Errorf("read canary baseline: %w", err)
	}
	var baseline canaryBaseline
	if err = json.Unmarshal(raw, &baseline); err != nil {
		return canaryBaseline{}, "", fmt.Errorf("decode canary baseline: %w", err)
	}
	if baseline.SchemaVersion != canaryBaselineVersion || baseline.MinimumCycles < 1 ||
		baseline.MaximumCycles < baseline.MinimumCycles || baseline.TerminalEventsPerCycle != 3 ||
		baseline.MaximumWakeLatencySeconds <= 0 || baseline.MaximumDurationSeconds <= 0 ||
		baseline.MaximumPythonExecutionDelta < 0 || baseline.MaximumModelCallDelta < 0 ||
		baseline.RequiredCompletionOutcome != "not_claimed" || baseline.RequiredTerminalOutcome != "no_match" {
		return canaryBaseline{}, "", fmt.Errorf("canary baseline contract is invalid")
	}
	digest := sha256.Sum256(raw)
	return baseline, fmt.Sprintf("%x", digest), nil
}

func evaluateReport(baseline canaryBaseline, baselineDigest string, summary canarySummary) canaryReport {
	report := canaryReport{SchemaVersion: canaryReportVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Decision: "pass", Summary: summary, Violations: []canaryViolation{}}
	report.Source.BaselineVersion = baseline.SchemaVersion
	report.Source.BaselineSHA256 = baselineDigest
	checks := []struct {
		code, path, expected string
		violated             bool
		actual               any
	}{
		{"dispatch_hint_count", "/summary/dispatch_hints", fmt.Sprintf(">= %d", summary.Cycles), summary.DispatchHints < summary.Cycles, summary.DispatchHints},
		{"not_claimed_count", "/summary/not_claimed", fmt.Sprintf("= %d", summary.Cycles), summary.NotClaimed != summary.Cycles, summary.NotClaimed},
		{"terminal_wake_count", "/summary/terminal_wakes", fmt.Sprintf("= %d", summary.Cycles*baseline.TerminalEventsPerCycle), summary.TerminalWakes != summary.Cycles*baseline.TerminalEventsPerCycle, summary.TerminalWakes},
		{"terminal_no_match_count", "/summary/terminal_no_matches", fmt.Sprintf("= %d", summary.Cycles*baseline.TerminalEventsPerCycle), summary.TerminalNoMatches != summary.Cycles*baseline.TerminalEventsPerCycle, summary.TerminalNoMatches},
		{"terminal_error_detected", "/summary/terminal_errors", "= 0", summary.TerminalErrors != 0, summary.TerminalErrors},
		{"wake_latency_high", "/summary/wake_latency_max_seconds", fmt.Sprintf("<= %.3f", baseline.MaximumWakeLatencySeconds), summary.WakeLatencyMaxSeconds > baseline.MaximumWakeLatencySeconds, summary.WakeLatencyMaxSeconds},
		{"python_execution_detected", "/summary/python_executions", fmt.Sprintf("<= %d", baseline.MaximumPythonExecutionDelta), summary.PythonExecutions > baseline.MaximumPythonExecutionDelta, summary.PythonExecutions},
		{"model_call_detected", "/summary/model_calls", fmt.Sprintf("<= %d", baseline.MaximumModelCallDelta), summary.ModelCalls > baseline.MaximumModelCallDelta, summary.ModelCalls},
		{"duration_high", "/summary/duration_ms", fmt.Sprintf("<= %.0f", baseline.MaximumDurationSeconds*1000), float64(summary.DurationMS) > baseline.MaximumDurationSeconds*1000, summary.DurationMS},
	}
	for _, check := range checks {
		if check.violated {
			report.Violations = append(report.Violations, canaryViolation{Code: check.code, Path: check.path, Expected: check.expected, Actual: check.actual})
		}
	}
	if len(report.Violations) > 0 {
		report.Decision = "fail"
	}
	return report
}

func summarizeCanaryMetrics(
	before, after string,
	cycles int,
	startedAt time.Time,
	beforeHints, beforeWakes, beforeNoMatches, beforeTerminalErrors, beforeNotClaimed, beforePythonExecutions, beforeModelCalls float64,
) canarySummary {
	return canarySummary{
		Cycles:                cycles,
		DispatchHints:         roundedInt(metricSum(after, "ai_companion_agent_canary_dispatch_hints_total") - beforeHints),
		NotClaimed:            roundedInt(metricValue(after, `ai_companion_agent_canary_dispatch_completions_total{outcome="not_claimed"}`) - beforeNotClaimed),
		TerminalWakes:         roundedInt(metricSum(after, "ai_companion_agent_canary_tool_wake_events_total") - beforeWakes),
		TerminalNoMatches:     roundedInt(metricValue(after, `ai_companion_agent_canary_tool_wake_events_total{outcome="no_match"}`) - beforeNoMatches),
		TerminalErrors:        roundedInt(metricValue(after, `ai_companion_agent_canary_tool_wake_events_total{outcome="error"}`) - beforeTerminalErrors),
		WakeLatencyMaxSeconds: histogramDeltaMaximum(before, after, "ai_companion_agent_canary_tool_wake_latency_seconds_bucket"),
		PythonExecutions:      roundedInt(metricValue(after, "ai_companion_agent_canary_python_executions_total") - beforePythonExecutions),
		ModelCalls:            roundedInt(metricValue(after, "ai_companion_agent_canary_model_calls_total") - beforeModelCalls),
		DurationMS:            int(time.Since(startedAt).Milliseconds()),
	}
}

func histogramDeltaMaximum(before, after, metricName string) float64 {
	beforeBuckets := histogramBuckets(before, metricName)
	afterBuckets := histogramBuckets(after, metricName)
	maximum := 0.0
	previousCumulative := 0.0
	for _, upperBound := range agentWakeBucketsAscending() {
		cumulative := afterBuckets[upperBound] - beforeBuckets[upperBound]
		if cumulative-previousCumulative > 0 {
			maximum = upperBound
		}
		previousCumulative = cumulative
	}
	if overflow := afterBuckets[math.Inf(1)] - beforeBuckets[math.Inf(1)] - previousCumulative; overflow > 0 {
		return 6
	}
	return maximum
}

func agentWakeBucketsAscending() []float64 { return []float64{0.1, 0.25, 0.5, 1, 2, 5} }

func histogramBuckets(document, metricName string) map[float64]float64 {
	result := make(map[float64]float64)
	scanner := bufio.NewScanner(strings.NewReader(document))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || !strings.HasPrefix(fields[0], metricName+"{") {
			continue
		}
		start := strings.Index(fields[0], `le="`)
		if start < 0 {
			continue
		}
		valueStart := start + len(`le="`)
		valueEnd := strings.Index(fields[0][valueStart:], `"`)
		if valueEnd < 0 {
			continue
		}
		label := fields[0][valueStart : valueStart+valueEnd]
		upperBound := math.Inf(1)
		if label != "+Inf" {
			upperBound, _ = strconv.ParseFloat(label, 64)
		}
		count, parseErr := strconv.ParseFloat(fields[1], 64)
		if parseErr == nil {
			result[upperBound] = count
		}
	}
	return result
}

func roundedInt(value float64) int { return int(math.Round(value)) }

func writeReports(jsonPath, junitPath string, report canaryReport) error {
	if jsonPath != "" {
		if err := writePrivateFile(jsonPath, func(writer io.Writer) error {
			encoder := json.NewEncoder(writer)
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		}); err != nil {
			return err
		}
	}
	if junitPath != "" {
		if err := writePrivateFile(junitPath, func(writer io.Writer) error {
			type failure struct {
				Message string `xml:"message,attr"`
				Text    string `xml:",chardata"`
			}
			type testCase struct {
				Name    string   `xml:"name,attr"`
				Failure *failure `xml:"failure,omitempty"`
			}
			type suite struct {
				XMLName  xml.Name `xml:"testsuite"`
				Name     string   `xml:"name,attr"`
				Tests    int      `xml:"tests,attr"`
				Failures int      `xml:"failures,attr"`
				Case     testCase `xml:"testcase"`
			}
			value := suite{Name: "agent-observability-canary", Tests: 1, Case: testCase{Name: "online-mixed-traffic"}}
			if len(report.Violations) > 0 {
				encoded, _ := json.Marshal(report.Violations)
				value.Failures = 1
				value.Case.Failure = &failure{Message: "canary gate failed", Text: string(encoded)}
			}
			_, err := writer.Write([]byte(xml.Header))
			if err != nil {
				return err
			}
			return xml.NewEncoder(writer).Encode(value)
		}); err != nil {
			return err
		}
	}
	return nil
}

func writePrivateFile(path string, write func(io.Writer) error) error {
	cleanPath := filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(cleanPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err = file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if err = write(file); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func publish(ctx context.Context, publisher *eventbus.KafkaPublisher, topic, aggregateType, aggregateID string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		log.Fatal(err)
	}
	eventID := mustID()
	if _, err = publisher.Publish(ctx, eventbus.Event{
		ID: eventID, AggregateType: aggregateType, AggregateID: aggregateID,
		Type: topic, Version: 1, Payload: encoded, OccurredAt: time.Now().UTC(),
	}); err != nil {
		log.Fatalf("publish %s: %v", topic, err)
	}
}

func mustID() string {
	value, err := id.New()
	if err != nil {
		log.Fatal(err)
	}
	return value
}

func fetchMetrics(ctx context.Context, client *http.Client, url string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch Agent metrics: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch Agent metrics: status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", fmt.Errorf("read Agent metrics: %w", err)
	}
	return string(body), nil
}

func metricSum(document, metricName string) float64 {
	var total float64
	scanner := bufio.NewScanner(strings.NewReader(document))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || (fields[0] != metricName && !strings.HasPrefix(fields[0], metricName+"{")) {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err == nil {
			total += value
		}
	}
	return total
}

func metricValue(document, series string) float64 {
	scanner := bufio.NewScanner(strings.NewReader(document))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != series {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err == nil {
			return value
		}
	}
	return 0
}

func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
