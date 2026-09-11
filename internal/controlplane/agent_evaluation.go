package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

const (
	AgentEvaluationSuiteSchema = "agent-eval-suite-v1"
	AgentEvaluationCaseSchema  = "agent-eval-case-v1"

	EvaluationStatusRunning   = "running"
	EvaluationStatusCompleted = "completed"
	EvaluationDecisionPass    = "pass"
	EvaluationDecisionFail    = "fail"

	maxAgentEvaluationCases       = 8
	maxAgentEvaluationParallelism = 4
)

type AgentEvaluationSuite struct {
	SchemaVersion string                `json:"schema_version"`
	Name          string                `json:"name"`
	Version       string                `json:"version"`
	BaselineRunID string                `json:"baseline_run_id,omitempty"`
	Cases         []AgentEvaluationCase `json:"cases"`
}

type AgentEvaluationCase struct {
	SchemaVersion string                `json:"schema_version"`
	ID            string                `json:"id"`
	Tags          []string              `json:"tags"`
	Given         AgentEvaluationGiven  `json:"given"`
	Expect        AgentEvaluationExpect `json:"expect"`
	Tape          map[string]any        `json:"tape"`
}

type AgentEvaluationGiven struct {
	Module      string           `json:"module"`
	UserMessage string           `json:"user_message"`
	Context     map[string]any   `json:"context,omitempty"`
	Tools       []map[string]any `json:"tools"`
}

type AgentEvaluationExpect struct {
	Status              string          `json:"status,omitempty"`
	Outcome             json.RawMessage `json:"outcome,omitempty"`
	ExecutionMode       string          `json:"execution_mode,omitempty"`
	ToolSequence        []string        `json:"tool_sequence,omitempty"`
	RequiredNodes       []string        `json:"required_nodes,omitempty"`
	ForbiddenNodes      []string        `json:"forbidden_nodes,omitempty"`
	MaxModelCalls       *int            `json:"max_model_calls,omitempty"`
	MaxSteps            *int            `json:"max_steps,omitempty"`
	MaxRetries          *int            `json:"max_retries,omitempty"`
	QualityPass         *bool           `json:"quality_pass,omitempty"`
	ResponseContains    []string        `json:"response_contains,omitempty"`
	ResponseNotContains []string        `json:"response_not_contains,omitempty"`
}

type AgentEvaluationAssertion struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Expected any    `json:"expected,omitempty"`
	Actual   any    `json:"actual,omitempty"`
}

type AgentEvaluationCaseResult struct {
	CaseID       string                     `json:"case_id"`
	Passed       bool                       `json:"passed"`
	Assertions   []AgentEvaluationAssertion `json:"assertions"`
	Status       string                     `json:"status,omitempty"`
	Outcome      string                     `json:"outcome,omitempty"`
	Response     string                     `json:"response,omitempty"`
	ModelCalls   int                        `json:"model_calls"`
	ToolCalls    int                        `json:"tool_calls"`
	TotalTokens  int                        `json:"total_tokens"`
	FailureCode  string                     `json:"failure_code,omitempty"`
	Failure      string                     `json:"failure,omitempty"`
	SandboxRunID string                     `json:"sandbox_run_id,omitempty"`
}

type AgentEvaluationComparison struct {
	BaselineRunID    string  `json:"baseline_run_id"`
	Available        bool    `json:"available"`
	PassRateDelta    float64 `json:"pass_rate_delta"`
	PassedCasesDelta int     `json:"passed_cases_delta"`
	ModelCallsDelta  int     `json:"model_calls_delta"`
	TotalTokensDelta int     `json:"total_tokens_delta"`
}

type AgentEvaluationSummary struct {
	TotalCases  int                        `json:"total_cases"`
	PassedCases int                        `json:"passed_cases"`
	FailedCases int                        `json:"failed_cases"`
	PassRate    float64                    `json:"pass_rate"`
	ModelCalls  int                        `json:"model_calls"`
	ToolCalls   int                        `json:"tool_calls"`
	TotalTokens int                        `json:"total_tokens"`
	Comparison  *AgentEvaluationComparison `json:"comparison,omitempty"`
}

type AgentEvaluationRun struct {
	ID               string                      `json:"id"`
	AgentVersionID   string                      `json:"agent_version_id"`
	AgentKey         string                      `json:"agent_key"`
	AgentVersion     int                         `json:"agent_version"`
	AgentFingerprint string                      `json:"agent_fingerprint"`
	Suite            AgentEvaluationSuite        `json:"suite"`
	SuiteFingerprint string                      `json:"suite_fingerprint"`
	Status           string                      `json:"status"`
	Decision         string                      `json:"decision,omitempty"`
	Summary          AgentEvaluationSummary      `json:"summary"`
	Results          []AgentEvaluationCaseResult `json:"results"`
	CreatedBy        string                      `json:"created_by"`
	StartedAt        time.Time                   `json:"started_at"`
	CompletedAt      *time.Time                  `json:"completed_at,omitempty"`
}

func (s *Service) EvaluateAgentVersion(
	ctx context.Context,
	versionID string,
	suite AgentEvaluationSuite,
	actor string,
	sandbox AgentSandbox,
) (AgentEvaluationRun, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || sandbox == nil {
		return AgentEvaluationRun{}, ErrValidation
	}
	version, err := s.Get(ctx, versionID)
	if err != nil {
		return AgentEvaluationRun{}, err
	}
	if version.Kind != KindAgentDefinition {
		return AgentEvaluationRun{}, ErrNotFound
	}
	if version.Status == StatusDraft {
		return AgentEvaluationRun{}, ErrConflict
	}
	if err = validateAgentEvaluationSuite(version, &suite); err != nil {
		return AgentEvaluationRun{}, err
	}
	suiteFingerprint, err := agentEvaluationSuiteFingerprint(suite)
	if err != nil {
		return AgentEvaluationRun{}, err
	}
	var baselineSummary *AgentEvaluationSummary
	if suite.BaselineRunID != "" {
		baseline, baselineErr := s.store.GetAgentEvaluationRun(ctx, suite.BaselineRunID)
		if baselineErr != nil {
			return AgentEvaluationRun{}, baselineErr
		}
		if baseline.Status != EvaluationStatusCompleted || baseline.Decision != EvaluationDecisionPass ||
			baseline.Suite.Name != suite.Name || baseline.Suite.Version != suite.Version ||
			baseline.SuiteFingerprint != suiteFingerprint {
			return AgentEvaluationRun{}, validationError("baseline evaluation must be a passing run of the same suite version")
		}
		baselineSummary = &baseline.Summary
	}
	runID, err := id.New()
	if err != nil {
		return AgentEvaluationRun{}, err
	}
	now := s.now().UTC()
	run := AgentEvaluationRun{
		ID: runID, AgentVersionID: version.ID, AgentKey: version.Key,
		AgentVersion: version.Version, AgentFingerprint: version.Fingerprint,
		Suite: suite, SuiteFingerprint: suiteFingerprint,
		Status: EvaluationStatusRunning, Results: []AgentEvaluationCaseResult{},
		CreatedBy: actor, StartedAt: now,
	}
	run, err = s.store.CreateAgentEvaluationRun(ctx, run)
	if err != nil {
		return AgentEvaluationRun{}, err
	}

	var definition AgentDefinitionPayload
	if err = json.Unmarshal(version.Payload, &definition); err != nil {
		return AgentEvaluationRun{}, validationError("decode Agent definition for evaluation: %v", err)
	}
	results := evaluateAgentCases(ctx, version, definition, suite.Cases, sandbox)
	summary := summarizeAgentEvaluation(results)
	if baselineSummary != nil {
		summary.Comparison = compareAgentEvaluations(run.Suite.BaselineRunID, summary, *baselineSummary)
	}
	decision := EvaluationDecisionPass
	if summary.FailedCases > 0 || summary.TotalCases == 0 {
		decision = EvaluationDecisionFail
	}
	persistenceContext, cancelPersistence := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelPersistence()
	return s.store.CompleteAgentEvaluationRun(
		persistenceContext, run.ID, EvaluationStatusRunning, decision,
		summary, results, s.now().UTC(),
	)
}

func (s *Service) EvaluationRun(ctx context.Context, runID string) (AgentEvaluationRun, error) {
	if strings.TrimSpace(runID) == "" {
		return AgentEvaluationRun{}, ErrValidation
	}
	return s.store.GetAgentEvaluationRun(ctx, strings.TrimSpace(runID))
}

func (s *Service) EvaluationRuns(ctx context.Context, versionID string, limit int) ([]AgentEvaluationRun, error) {
	if strings.TrimSpace(versionID) == "" {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.store.ListAgentEvaluationRuns(ctx, strings.TrimSpace(versionID), limit)
}

func validateAgentEvaluationSuite(version Version, suite *AgentEvaluationSuite) error {
	suite.SchemaVersion = strings.TrimSpace(suite.SchemaVersion)
	suite.Name, suite.Version = strings.TrimSpace(suite.Name), strings.TrimSpace(suite.Version)
	suite.BaselineRunID = strings.TrimSpace(suite.BaselineRunID)
	if suite.SchemaVersion != AgentEvaluationSuiteSchema || !validKey(suite.Name) ||
		suite.Version == "" || len(suite.Version) > 64 || len(suite.Cases) == 0 || len(suite.Cases) > maxAgentEvaluationCases {
		return validationError("evaluation suite metadata or case count is invalid")
	}
	var definition AgentDefinitionPayload
	if err := json.Unmarshal(version.Payload, &definition); err != nil {
		return validationError("decode Agent definition for evaluation: %v", err)
	}
	modules := make(map[string]bool, len(definition.Modules))
	for _, module := range definition.Modules {
		modules[module] = true
	}
	seen := map[string]bool{}
	coveredModules := map[string]bool{}
	happyPathModules := map[string]bool{}
	for index := range suite.Cases {
		item := &suite.Cases[index]
		item.SchemaVersion, item.ID = strings.TrimSpace(item.SchemaVersion), strings.TrimSpace(item.ID)
		item.Given.Module, item.Given.UserMessage = strings.TrimSpace(item.Given.Module), strings.TrimSpace(item.Given.UserMessage)
		if item.SchemaVersion != AgentEvaluationCaseSchema || item.ID == "" || len(item.ID) > 128 || seen[item.ID] ||
			!modules[item.Given.Module] || item.Given.UserMessage == "" || len(item.Given.UserMessage) > 12_000 ||
			len(item.Tags) > 32 || len(item.Given.Tools) > 64 {
			return validationError("evaluation case %d is invalid", index+1)
		}
		seen[item.ID] = true
		coveredModules[item.Given.Module] = true
		if err := validateAgentEvaluationExpect(item.Expect); err != nil {
			return validationError("evaluation case %s: %v", item.ID, err)
		}
		if item.Expect.Status == "" || len(item.Expect.Outcome) == 0 || item.Expect.MaxModelCalls == nil || item.Expect.MaxSteps == nil {
			return validationError("evaluation case %s must gate status, outcome, model calls, and steps", item.ID)
		}
		if *item.Expect.MaxModelCalls > definition.Budget.MaxModelCalls || *item.Expect.MaxSteps > definition.Budget.MaxSteps || *item.Expect.MaxSteps < 1 {
			return validationError("evaluation case %s exceeds the Agent version budget", item.ID)
		}
		expected, _ := expectedOutcomes(item.Expect.Outcome)
		if item.Expect.Status == "completed" && containsString(expected, "completed") {
			happyPathModules[item.Given.Module] = true
		}
		if _, err := agentEvaluationScenario(*item); err != nil {
			return validationError("evaluation case %s: %v", item.ID, err)
		}
	}
	for module := range modules {
		if !coveredModules[module] {
			return validationError("evaluation suite does not cover Agent module %s", module)
		}
		if !happyPathModules[module] {
			return validationError("evaluation suite does not contain a completed happy path for Agent module %s", module)
		}
	}
	return nil
}

func validateAgentEvaluationExpect(expect AgentEvaluationExpect) error {
	assertions := 0
	if expect.Status != "" {
		assertions++
	}
	if len(expect.Outcome) > 0 {
		if _, err := expectedOutcomes(expect.Outcome); err != nil {
			return err
		}
		assertions++
	}
	if expect.ExecutionMode != "" {
		if expect.ExecutionMode != "direct" && expect.ExecutionMode != "single_action" && expect.ExecutionMode != "agentic" {
			return errors.New("execution_mode is invalid")
		}
		assertions++
	}
	for _, value := range []any{expect.ToolSequence, expect.RequiredNodes, expect.ForbiddenNodes, expect.MaxModelCalls, expect.MaxSteps, expect.MaxRetries, expect.QualityPass, expect.ResponseContains, expect.ResponseNotContains} {
		switch typed := value.(type) {
		case []string:
			if typed != nil {
				assertions++
			}
		case *int:
			if typed != nil {
				if *typed < 0 {
					return errors.New("numeric expectation is negative")
				}
				assertions++
			}
		case *bool:
			if typed != nil {
				assertions++
			}
		}
	}
	if assertions == 0 {
		return errors.New("at least one expectation is required")
	}
	return nil
}

func agentEvaluationScenario(item AgentEvaluationCase) (map[string]any, error) {
	scenario := map[string]any{"module": item.Given.Module, "user_message": item.Given.UserMessage}
	if item.Given.Context == nil {
		return scenario, nil
	}
	raw, exists := item.Given.Context["synthetic"]
	if !exists {
		return scenario, nil
	}
	synthetic, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("given.context.synthetic must be an object")
	}
	for _, field := range []string{"routes", "models", "tools"} {
		if value, found := synthetic[field]; found {
			scenario[field] = value
		}
	}
	return scenario, nil
}

func evaluateAgentCases(
	ctx context.Context,
	version Version,
	definition AgentDefinitionPayload,
	cases []AgentEvaluationCase,
	sandbox AgentSandbox,
) []AgentEvaluationCaseResult {
	results := make([]AgentEvaluationCaseResult, len(cases))
	semaphore := make(chan struct{}, maxAgentEvaluationParallelism)
	var group sync.WaitGroup
	for index := range cases {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index] = failedEvaluationCase(cases[index].ID, "evaluation_cancelled", "evaluation request was cancelled")
				return
			}
			scenario, err := agentEvaluationScenario(cases[index])
			if err != nil {
				results[index] = failedEvaluationCase(cases[index].ID, "invalid_fixture", err.Error())
				return
			}
			report, err := sandbox.Run(ctx, AgentSandboxRequest{
				Definition: definition,
				Identity:   AgentSandboxIdentity{Key: version.Key, VersionID: version.ID, Fingerprint: version.Fingerprint},
				Scenario:   scenario,
			})
			if err != nil {
				code := "sandbox_failed"
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					code = "evaluation_cancelled"
				}
				results[index] = failedEvaluationCase(cases[index].ID, code, publicEvaluationFailure(err))
				return
			}
			results[index] = assertAgentEvaluationCase(cases[index], report)
		}()
	}
	group.Wait()
	return results
}

func assertAgentEvaluationCase(item AgentEvaluationCase, report AgentSandboxReport) AgentEvaluationCaseResult {
	result := AgentEvaluationCaseResult{
		CaseID: item.ID, Passed: true, Status: report.Status, Outcome: report.Outcome,
		Response: report.Response, ModelCalls: len(report.ModelCalls), ToolCalls: evaluationToolCalls(report),
		TotalTokens: evaluationTokens(report), SandboxRunID: report.RunID,
		Assertions: []AgentEvaluationAssertion{},
	}
	add := func(name string, expected, actual any, passed bool) {
		result.Assertions = append(result.Assertions, AgentEvaluationAssertion{Name: name, Expected: expected, Actual: actual, Passed: passed})
		result.Passed = result.Passed && passed
	}
	if item.Expect.Status != "" {
		add("status", item.Expect.Status, report.Status, report.Status == item.Expect.Status)
	}
	if len(item.Expect.Outcome) > 0 {
		expected, _ := expectedOutcomes(item.Expect.Outcome)
		add("outcome", expected, report.Outcome, containsString(expected, report.Outcome))
	}
	if item.Expect.ExecutionMode != "" {
		actual := evaluationExecutionMode(report)
		add("execution_mode", item.Expect.ExecutionMode, actual, actual == item.Expect.ExecutionMode)
	}
	if item.Expect.ToolSequence != nil {
		actual := evaluationToolSequence(report)
		add("tool_sequence", item.Expect.ToolSequence, actual, equalStrings(item.Expect.ToolSequence, actual))
	}
	visited := evaluationVisitedNodes(report)
	if item.Expect.RequiredNodes != nil {
		missing := missingStrings(item.Expect.RequiredNodes, visited)
		add("required_nodes", item.Expect.RequiredNodes, sortedKeys(visited), len(missing) == 0)
	}
	if item.Expect.ForbiddenNodes != nil {
		found := presentStrings(item.Expect.ForbiddenNodes, visited)
		add("forbidden_nodes", item.Expect.ForbiddenNodes, found, len(found) == 0)
	}
	if item.Expect.MaxModelCalls != nil {
		add("max_model_calls", *item.Expect.MaxModelCalls, result.ModelCalls, result.ModelCalls <= *item.Expect.MaxModelCalls)
	}
	if item.Expect.MaxSteps != nil {
		add("max_steps", *item.Expect.MaxSteps, len(report.NodeTrace), len(report.NodeTrace) <= *item.Expect.MaxSteps)
	}
	if item.Expect.MaxRetries != nil {
		actual := evaluationRetries(report)
		add("max_retries", *item.Expect.MaxRetries, actual, actual <= *item.Expect.MaxRetries)
	}
	if item.Expect.QualityPass != nil {
		actual := report.Outcome != "response_quality_failed"
		add("quality_pass", *item.Expect.QualityPass, actual, actual == *item.Expect.QualityPass)
	}
	for _, fragment := range item.Expect.ResponseContains {
		add("response_contains", fragment, report.Response, strings.Contains(report.Response, fragment))
	}
	for _, fragment := range item.Expect.ResponseNotContains {
		add("response_not_contains", fragment, report.Response, !strings.Contains(report.Response, fragment))
	}
	safe := !mapBool(report.Safety, "side_effects") && !mapBool(report.Safety, "production_data_access") &&
		mapInt(report.Safety, "external_model_calls") == 0 && mapInt(report.Safety, "external_tool_calls") == 0
	add("synthetic_isolation", true, safe, safe)
	return result
}

func summarizeAgentEvaluation(results []AgentEvaluationCaseResult) AgentEvaluationSummary {
	summary := AgentEvaluationSummary{TotalCases: len(results)}
	for _, result := range results {
		if result.Passed {
			summary.PassedCases++
		} else {
			summary.FailedCases++
		}
		summary.ModelCalls += result.ModelCalls
		summary.ToolCalls += result.ToolCalls
		summary.TotalTokens += result.TotalTokens
	}
	if summary.TotalCases > 0 {
		summary.PassRate = float64(summary.PassedCases) / float64(summary.TotalCases)
	}
	return summary
}

func compareAgentEvaluations(runID string, current, baseline AgentEvaluationSummary) *AgentEvaluationComparison {
	result := &AgentEvaluationComparison{
		BaselineRunID: runID, Available: true,
		PassRateDelta:    current.PassRate - baseline.PassRate,
		PassedCasesDelta: current.PassedCases - baseline.PassedCases,
		ModelCallsDelta:  current.ModelCalls - baseline.ModelCalls,
		TotalTokensDelta: current.TotalTokens - baseline.TotalTokens,
	}
	return result
}

func failedEvaluationCase(caseID, code, message string) AgentEvaluationCaseResult {
	return AgentEvaluationCaseResult{CaseID: caseID, Passed: false, Assertions: []AgentEvaluationAssertion{}, FailureCode: code, Failure: message}
}

func publicEvaluationFailure(err error) string {
	if errors.Is(err, ErrValidation) {
		return "synthetic fixture or Agent definition is invalid"
	}
	if errors.Is(err, ErrSandboxUnavailable) {
		return "synthetic runtime is unavailable"
	}
	return "evaluation case could not be executed"
}

func expectedOutcomes(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && strings.TrimSpace(single) != "" {
		return []string{single}, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || len(values) == 0 {
		return nil, errors.New("outcome expectation must be a string or non-empty string list")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("outcome expectation contains an empty value")
		}
	}
	return values, nil
}

func evaluationFingerprint(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func agentEvaluationSuiteFingerprint(suite AgentEvaluationSuite) (string, error) {
	suite.BaselineRunID = ""
	payload, err := json.Marshal(suite)
	if err != nil {
		return "", err
	}
	return evaluationFingerprint(payload), nil
}

func evaluationToolCalls(report AgentSandboxReport) int {
	count := 0
	for _, call := range report.ToolCalls {
		if fmt.Sprint(call["stage"]) == "prepare" {
			count++
		}
	}
	return count
}

func evaluationToolSequence(report AgentSandboxReport) []string {
	result := make([]string, 0, len(report.ToolSelections))
	for _, item := range report.ToolSelections {
		if value := strings.TrimSpace(fmt.Sprint(item["tool_name"])); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func evaluationTokens(report AgentSandboxReport) int {
	usage, _ := report.Budget["usage"].(map[string]any)
	return mapInt(usage, "prompt_tokens") + mapInt(usage, "completion_tokens")
}

func evaluationRetries(report AgentSandboxReport) int {
	return mapInt(report.Recovery, "retries")
}

func evaluationExecutionMode(report AgentSandboxReport) string {
	count := len(evaluationToolSequence(report))
	if count == 0 {
		return "direct"
	}
	if count == 1 {
		return "single_action"
	}
	return "agentic"
}

func evaluationVisitedNodes(report AgentSandboxReport) map[string]bool {
	result := map[string]bool{}
	for _, item := range report.NodeTrace {
		value := normalizeEvaluationNode(fmt.Sprint(item["node"]))
		if value != "" {
			result[value] = true
		}
	}
	return result
}

func normalizeEvaluationNode(value string) string {
	value = strings.TrimPrefix(value, "studio__")
	value = strings.TrimPrefix(value, "__studio_")
	if index := strings.IndexByte(value, '.'); index > 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

func mapInt(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		result, _ := value.Int64()
		return int(result)
	default:
		return 0
	}
}

func mapBool(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func missingStrings(values []string, present map[string]bool) []string {
	result := []string{}
	for _, value := range values {
		if !present[value] {
			result = append(result, value)
		}
	}
	return result
}

func presentStrings(values []string, present map[string]bool) []string {
	result := []string{}
	for _, value := range values {
		if present[value] {
			result = append(result, value)
		}
	}
	return result
}

func sortedKeys(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
