package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

const (
	AgentRolloutStatusRunning  = "running"
	AgentRolloutStatusReady    = "ready"
	AgentRolloutStatusPaused   = "paused"
	AgentRolloutStatusPromoted = "promoted"
	AgentRolloutStatusAborted  = "aborted"

	AgentRolloutDecisionCollecting = "collecting"
	AgentRolloutDecisionPass       = "pass"
	AgentRolloutDecisionFail       = "fail"
)

type AgentRolloutPolicy struct {
	MinimumSampleSize             int     `json:"minimum_sample_size"`
	ObservationWindowSeconds      int     `json:"observation_window_seconds"`
	MaxErrorRate                  float64 `json:"max_error_rate"`
	MaxQualityFailureRate         float64 `json:"max_quality_failure_rate"`
	MaxP95LatencyMS               int64   `json:"max_p95_latency_ms"`
	MaxAverageCostMicros          int64   `json:"max_average_cost_micros"`
	MaxErrorRateRegression        float64 `json:"max_error_rate_regression"`
	MaxP95LatencyRegressionRatio  float64 `json:"max_p95_latency_regression_ratio"`
	MaxAverageCostRegressionRatio float64 `json:"max_average_cost_regression_ratio"`
}

type AgentRolloutMetrics struct {
	SampleSize         int     `json:"sample_size"`
	Completed          int     `json:"completed"`
	Failed             int     `json:"failed"`
	QualityFailures    int     `json:"quality_failures"`
	ErrorRate          float64 `json:"error_rate"`
	QualityFailureRate float64 `json:"quality_failure_rate"`
	P95LatencyMS       float64 `json:"p95_latency_ms"`
	AverageCostMicros  float64 `json:"average_cost_micros"`
}

type AgentRolloutMeasurements struct {
	Candidate AgentRolloutMetrics `json:"candidate"`
	Baseline  AgentRolloutMetrics `json:"baseline"`
}

type AgentRolloutSummary struct {
	WindowStartedAt time.Time           `json:"window_started_at"`
	WindowEndedAt   time.Time           `json:"window_ended_at"`
	Candidate       AgentRolloutMetrics `json:"candidate"`
	Baseline        AgentRolloutMetrics `json:"baseline"`
}

type AgentRolloutViolation struct {
	Metric   string  `json:"metric"`
	Expected float64 `json:"expected"`
	Actual   float64 `json:"actual"`
	Message  string  `json:"message"`
}

type AgentRollout struct {
	ID                  string                  `json:"id"`
	Environment         string                  `json:"environment"`
	AgentKey            string                  `json:"agent_key"`
	AgentVersionID      string                  `json:"agent_version_id"`
	AgentVersion        int                     `json:"agent_version"`
	AgentFingerprint    string                  `json:"agent_fingerprint"`
	BaselineVersionID   string                  `json:"baseline_version_id"`
	BaselineVersion     int                     `json:"baseline_version"`
	BaselineFingerprint string                  `json:"baseline_fingerprint"`
	TrafficPercent      int                     `json:"traffic_percent"`
	RoutingRevision     int                     `json:"routing_revision"`
	Policy              AgentRolloutPolicy      `json:"policy"`
	Status              string                  `json:"status"`
	Decision            string                  `json:"decision"`
	Summary             AgentRolloutSummary     `json:"summary"`
	Violations          []AgentRolloutViolation `json:"violations"`
	Revision            int                     `json:"revision"`
	CreatedBy           string                  `json:"created_by"`
	UpdatedBy           string                  `json:"updated_by"`
	CreatedAt           time.Time               `json:"created_at"`
	UpdatedAt           time.Time               `json:"updated_at"`
	EvaluatedAt         *time.Time              `json:"evaluated_at,omitempty"`
	CompletedAt         *time.Time              `json:"completed_at,omitempty"`
}

type StartAgentRolloutInput struct {
	TrafficPercent int                `json:"traffic_percent"`
	Policy         AgentRolloutPolicy `json:"policy"`
	Actor          string             `json:"-"`
}

func DefaultAgentRolloutPolicy() AgentRolloutPolicy {
	return AgentRolloutPolicy{
		MinimumSampleSize: 20, ObservationWindowSeconds: 3600,
		MaxErrorRate: .05, MaxQualityFailureRate: .03,
		MaxP95LatencyMS: 120000, MaxAverageCostMicros: 5_000_000,
		MaxErrorRateRegression: .02, MaxP95LatencyRegressionRatio: .25,
		MaxAverageCostRegressionRatio: .25,
	}
}

func (s *Service) StartAgentRollout(ctx context.Context, versionID string, input StartAgentRolloutInput) (AgentRollout, error) {
	input.Actor = strings.TrimSpace(input.Actor)
	if input.Actor == "" || input.TrafficPercent < 1 || input.TrafficPercent > 50 {
		return AgentRollout{}, ErrValidation
	}
	policy, err := normalizeAgentRolloutPolicy(input.Policy)
	if err != nil {
		return AgentRollout{}, err
	}
	item, err := s.Get(ctx, versionID)
	if err != nil {
		return AgentRollout{}, err
	}
	if item.Kind != KindAgentDefinition || item.Status != StatusSubmitted {
		return AgentRollout{}, ErrConflict
	}
	if input.Actor == item.CreatedBy || input.Actor == item.SubmittedBy {
		return AgentRollout{}, ErrApproval
	}
	if _, err = s.store.LatestPassingAgentEvaluationRun(ctx, item.ID, item.Fingerprint); err != nil {
		if errors.Is(err, ErrNotFound) {
			return AgentRollout{}, ErrEvaluation
		}
		return AgentRollout{}, err
	}
	if err = s.validateAgentActivation(ctx, item); err != nil {
		return AgentRollout{}, err
	}
	if _, err = s.store.ActiveAgentRollout(ctx, s.environment); err == nil {
		return AgentRollout{}, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return AgentRollout{}, err
	}
	deployments, err := s.Active(ctx, KindAgentDefinition)
	if err != nil {
		return AgentRollout{}, err
	}
	var baseline Deployment
	for _, deployment := range deployments {
		if deployment.Key == item.Key {
			baseline = deployment
			break
		}
	}
	if baseline.Version.ID == "" || baseline.Version.ID == item.ID {
		return AgentRollout{}, fmt.Errorf("%w: an active baseline Agent version is required", ErrRollout)
	}
	rolloutID, err := id.New()
	if err != nil {
		return AgentRollout{}, err
	}
	now := s.now().UTC()
	routingRevision := baseline.Revision + 1
	if baseline.Environment != s.environment {
		routingRevision = 1
	}
	return s.store.CreateAgentRollout(ctx, AgentRollout{
		ID: rolloutID, Environment: s.environment, AgentKey: item.Key,
		AgentVersionID: item.ID, AgentVersion: item.Version, AgentFingerprint: item.Fingerprint,
		BaselineVersionID: baseline.Version.ID, BaselineVersion: baseline.Version.Version,
		BaselineFingerprint: baseline.Version.Fingerprint, TrafficPercent: input.TrafficPercent,
		RoutingRevision: routingRevision, Policy: policy,
		Status: AgentRolloutStatusRunning, Decision: AgentRolloutDecisionCollecting,
		Summary:    AgentRolloutSummary{WindowStartedAt: now, WindowEndedAt: now},
		Violations: []AgentRolloutViolation{}, Revision: 1,
		CreatedBy: input.Actor, UpdatedBy: input.Actor, CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Service) RefreshAgentRollout(ctx context.Context, rolloutID, actor string) (AgentRollout, error) {
	actor = strings.TrimSpace(actor)
	if actor == "" || strings.TrimSpace(rolloutID) == "" {
		return AgentRollout{}, ErrValidation
	}
	rollout, err := s.store.GetAgentRollout(ctx, rolloutID)
	if err != nil {
		return AgentRollout{}, err
	}
	if rollout.Status != AgentRolloutStatusRunning && rollout.Status != AgentRolloutStatusReady {
		return AgentRollout{}, ErrConflict
	}
	now := s.now().UTC()
	since := rollout.CreatedAt
	windowStart := now.Add(-time.Duration(rollout.Policy.ObservationWindowSeconds) * time.Second)
	if windowStart.After(since) {
		since = windowStart
	}
	measurements, err := s.store.MeasureAgentRollout(ctx, rollout, since, now)
	if err != nil {
		return AgentRollout{}, err
	}
	summary := AgentRolloutSummary{WindowStartedAt: since, WindowEndedAt: now, Candidate: measurements.Candidate, Baseline: measurements.Baseline}
	status, decision := AgentRolloutStatusRunning, AgentRolloutDecisionCollecting
	violations := make([]AgentRolloutViolation, 0)
	if measurements.Candidate.SampleSize >= rollout.Policy.MinimumSampleSize {
		violations = evaluateAgentRolloutPolicy(rollout.Policy, measurements)
		if len(violations) > 0 {
			status, decision = AgentRolloutStatusPaused, AgentRolloutDecisionFail
		} else if measurements.Baseline.SampleSize >= rollout.Policy.MinimumSampleSize {
			status, decision = AgentRolloutStatusReady, AgentRolloutDecisionPass
		}
	}
	reason := "rollout metrics refreshed"
	if status == AgentRolloutStatusPaused {
		reason = "rollout automatically paused by online quality gate"
	}
	return s.store.UpdateAgentRollout(ctx, rollout.ID, rollout.Revision, status, decision, summary, violations, actor, reason, now)
}

func (s *Service) AbortAgentRollout(ctx context.Context, rolloutID, actor, reason string) (AgentRollout, error) {
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if actor == "" || reason == "" || len([]rune(reason)) > 512 {
		return AgentRollout{}, ErrValidation
	}
	rollout, err := s.store.GetAgentRollout(ctx, rolloutID)
	if err != nil {
		return AgentRollout{}, err
	}
	if rollout.Status != AgentRolloutStatusRunning && rollout.Status != AgentRolloutStatusReady {
		return AgentRollout{}, ErrConflict
	}
	violations := append([]AgentRolloutViolation(nil), rollout.Violations...)
	violations = append(violations, AgentRolloutViolation{Metric: "manual_abort", Expected: 0, Actual: 1, Message: reason})
	return s.store.UpdateAgentRollout(ctx, rollout.ID, rollout.Revision, AgentRolloutStatusAborted,
		AgentRolloutDecisionFail, rollout.Summary, violations, actor, reason, s.now().UTC())
}

func (s *Service) AgentRollout(ctx context.Context, rolloutID string) (AgentRollout, error) {
	if strings.TrimSpace(rolloutID) == "" {
		return AgentRollout{}, ErrValidation
	}
	return s.store.GetAgentRollout(ctx, strings.TrimSpace(rolloutID))
}

func (s *Service) AgentRollouts(ctx context.Context, agentKey string, limit int) ([]AgentRollout, error) {
	agentKey = normalizeKey(agentKey)
	if !validKey(agentKey) {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.store.ListAgentRollouts(ctx, agentKey, limit)
}

func normalizeAgentRolloutPolicy(policy AgentRolloutPolicy) (AgentRolloutPolicy, error) {
	if policy == (AgentRolloutPolicy{}) {
		return DefaultAgentRolloutPolicy(), nil
	}
	if policy.MinimumSampleSize < 1 || policy.MinimumSampleSize > 10000 ||
		policy.ObservationWindowSeconds < 60 || policy.ObservationWindowSeconds > 604800 ||
		policy.MaxErrorRate < 0 || policy.MaxErrorRate > 1 ||
		policy.MaxQualityFailureRate < 0 || policy.MaxQualityFailureRate > 1 ||
		policy.MaxP95LatencyMS < 1 || policy.MaxP95LatencyMS > 900000 ||
		policy.MaxAverageCostMicros < 1 || policy.MaxAverageCostMicros > 1_000_000_000 ||
		policy.MaxErrorRateRegression < 0 || policy.MaxErrorRateRegression > 1 ||
		policy.MaxP95LatencyRegressionRatio < 0 || policy.MaxP95LatencyRegressionRatio > 10 ||
		policy.MaxAverageCostRegressionRatio < 0 || policy.MaxAverageCostRegressionRatio > 10 {
		return AgentRolloutPolicy{}, ErrValidation
	}
	return policy, nil
}

func evaluateAgentRolloutPolicy(policy AgentRolloutPolicy, measurements AgentRolloutMeasurements) []AgentRolloutViolation {
	violations := make([]AgentRolloutViolation, 0)
	add := func(metric string, expected, actual float64, message string) {
		if math.IsNaN(actual) || math.IsInf(actual, 0) || actual > expected {
			violations = append(violations, AgentRolloutViolation{Metric: metric, Expected: expected, Actual: actual, Message: message})
		}
	}
	candidate, baseline := measurements.Candidate, measurements.Baseline
	add("error_rate", policy.MaxErrorRate, candidate.ErrorRate, "候选版本错误率超过上限")
	add("quality_failure_rate", policy.MaxQualityFailureRate, candidate.QualityFailureRate, "候选版本质量失败率超过上限")
	add("p95_latency_ms", float64(policy.MaxP95LatencyMS), candidate.P95LatencyMS, "候选版本 P95 延迟超过上限")
	add("average_cost_micros", float64(policy.MaxAverageCostMicros), candidate.AverageCostMicros, "候选版本平均成本超过上限")
	if baseline.SampleSize >= policy.MinimumSampleSize {
		add("error_rate_regression", policy.MaxErrorRateRegression, candidate.ErrorRate-baseline.ErrorRate, "候选版本错误率相对基线回退")
		if baseline.P95LatencyMS > 0 {
			add("p95_latency_regression_ratio", policy.MaxP95LatencyRegressionRatio, candidate.P95LatencyMS/baseline.P95LatencyMS-1, "候选版本延迟相对基线回退")
		}
		if baseline.AverageCostMicros > 0 {
			add("average_cost_regression_ratio", policy.MaxAverageCostRegressionRatio, candidate.AverageCostMicros/baseline.AverageCostMicros-1, "候选版本成本相对基线回退")
		}
	}
	return violations
}

func cloneAgentRollout(item AgentRollout) AgentRollout {
	payload, err := json.Marshal(item)
	if err != nil {
		return item
	}
	var clone AgentRollout
	if json.Unmarshal(payload, &clone) != nil {
		return item
	}
	return clone
}
