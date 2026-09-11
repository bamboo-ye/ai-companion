package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/performance"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

type performanceHTTPStore struct{ withRecommendation bool }

type performanceReviewHTTPStore struct {
	performanceHTTPStore
	decision performance.BudgetPolicyDecision
	outcomes []performance.BudgetForecastOutcome
}

func TestPerformanceBudgetReviewExport(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "performance-report-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetPerformanceStore(performanceHTTPStore{withRecommendation: true})
	markdown := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/budgets/report?range=90d&format=markdown", "ops-token", "viewer", nil)
	if markdown.Code != http.StatusOK || !strings.Contains(markdown.Header().Get("Content-Type"), "text/markdown") || !strings.Contains(markdown.Body.String(), "# 成本预算周期复盘") || !strings.Contains(markdown.Body.String(), "工作每日预算") {
		t.Fatalf("markdown report=%d %s", markdown.Code, markdown.Body.String())
	}
	jsonReport := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/budgets/report?range=30d&format=json", "ops-token", "viewer", nil)
	if jsonReport.Code != http.StatusOK || !strings.Contains(jsonReport.Body.String(), `"range":"30d"`) || !strings.Contains(jsonReport.Body.String(), `"recommendations":1`) {
		t.Fatalf("json report=%d %s", jsonReport.Code, jsonReport.Body.String())
	}
}

func newPerformanceReviewHTTPStore(now time.Time) *performanceReviewHTTPStore {
	budgetID, decisionID := "00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000099"
	appliedAt := now.Add(-40 * 24 * time.Hour)
	store := &performanceReviewHTTPStore{performanceHTTPStore: performanceHTTPStore{withRecommendation: true}, decision: performance.BudgetPolicyDecision{
		ID: decisionID, BudgetID: budgetID, RecommendationKey: strings.Repeat("e", 64), Action: "increase_sample_gate", Decision: "accepted", Confidence: "high",
		OutcomeCount: 5, HitRate: .8, AverageLeadTimeMinutes: 120, CurrentLookbackDays: 7, CurrentMinSamples: 20, ProposedLookbackDays: 7, ProposedMinSamples: 25,
		BudgetRevision: 1, AppliedAt: &appliedAt, AppliedBy: "admin-a", AppliedBudgetRevision: 2, EffectObservationDays: 30, EffectMinSamples: 5,
	}}
	for index := 0; index < 5; index++ {
		outcome := "cleared"
		if index == 0 {
			outcome = "hit"
		}
		store.outcomes = append(store.outcomes, performance.BudgetForecastOutcome{IncidentID: "00000000-0000-4000-8000-00000000008" + string(rune('0'+index)), BudgetID: budgetID, BudgetName: "工作每日预算", Module: "work", Period: "daily", Outcome: outcome, OpenedAt: appliedAt.Add(time.Duration(index+1) * 24 * time.Hour), DurationMinutes: 60, BudgetRevision: 2})
	}
	return store
}

func (s *performanceReviewHTTPStore) ListBudgetPolicyDecisions(context.Context, string) ([]performance.BudgetPolicyDecision, error) {
	return []performance.BudgetPolicyDecision{s.decision}, nil
}
func (s *performanceReviewHTTPStore) ListBudgets(context.Context) ([]performance.Budget, error) {
	return []performance.Budget{{
		ID: s.decision.BudgetID, Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8,
		Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: s.decision.ProposedLookbackDays, ForecastMinSamples: s.decision.ProposedMinSamples,
		EffectObservationDays: 30, EffectMinSamples: 5, Revision: s.decision.AppliedBudgetRevision,
	}}, nil
}
func (s *performanceReviewHTTPStore) GetBudget(ctx context.Context, budgetID string) (performance.Budget, error) {
	items, _ := s.ListBudgets(ctx)
	if len(items) == 1 && items[0].ID == budgetID {
		return items[0], nil
	}
	return performance.Budget{}, performance.ErrNotFound
}
func (s *performanceReviewHTTPStore) ListBudgetForecastOutcomes(context.Context, performance.ForecastHistoryFilter) ([]performance.BudgetForecastOutcome, error) {
	return append([]performance.BudgetForecastOutcome(nil), s.outcomes...), nil
}
func (s *performanceReviewHTTPStore) AcknowledgeBudgetPolicyEffect(_ context.Context, _ performance.BudgetPolicyEffect, review performance.BudgetPolicyEffectReview) (performance.BudgetPolicyEffectReview, error) {
	reviewCopy := review
	s.decision.EffectReview = &reviewCopy
	return review, nil
}
func (s *performanceReviewHTTPStore) CloseBudgetPolicyEffect(_ context.Context, effect performance.BudgetPolicyEffect, actor, reason string, now time.Time) (performance.BudgetPolicyEffectReview, error) {
	if effect.Review == nil || effect.Review.Status != "acknowledged" {
		return performance.BudgetPolicyEffectReview{}, performance.ErrEffectReviewConflict
	}
	review := *effect.Review
	review.Status, review.ClosedBy, review.ClosedReason, review.ClosedAt = "closed", actor, reason, &now
	s.decision.EffectReview = &review
	return review, nil
}

func (performanceHTTPStore) ListVersionMetrics(_ context.Context, filter performance.Filter) ([]performance.VersionMetrics, error) {
	return []performance.VersionMetrics{{
		Dimension: filter.Dimension, GroupID: "version-1", Key: "work-assistant", Label: "work-assistant · v2",
		VersionID: "version-1", Version: 2, Runs: 20, SampleSize: 20, Completed: 19,
		SuccessRate: .95, ModelCalls: 22, CostMicros: 42000, AverageCostMicros: 2100,
	}}, nil
}

func (performanceHTTPStore) ListModelMetrics(context.Context, performance.Filter) ([]performance.ModelMetrics, error) {
	return []performance.ModelMetrics{
		{Provider: "openrouter", Model: "model-a", Calls: 22, Succeeded: 21, Failed: 1, ErrorRate: 1.0 / 22.0, CostMicros: 42000, AverageCostMicros: 2000, P95LatencyMS: 1000},
		{Provider: "openrouter", Model: "model-b", Calls: 30, Succeeded: 30, CostMicros: 30000, AverageCostMicros: 1000, P95LatencyMS: 1100},
	}, nil
}

func (performanceHTTPStore) MeasureWindow(_ context.Context, filter performance.WindowFilter) (performance.WindowMetrics, error) {
	return performance.WindowMetrics{From: filter.From, To: filter.To, SampleSize: 3}, nil
}
func (performanceHTTPStore) ListTrend(_ context.Context, filter performance.TrendFilter) ([]performance.TrendPoint, error) {
	return []performance.TrendPoint{{From: filter.From, To: filter.To, Runs: 3, CostMicros: 12000}}, nil
}
func (store performanceHTTPStore) ListBudgets(context.Context) ([]performance.Budget, error) {
	if !store.withRecommendation {
		return nil, nil
	}
	return []performance.Budget{{ID: "00000000-0000-4000-8000-000000000001", Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 20, EffectObservationDays: 30, EffectMinSamples: 5, Revision: 1}}, nil
}
func (performanceHTTPStore) GetBudget(_ context.Context, budgetID string) (performance.Budget, error) {
	return performance.Budget{ID: budgetID, Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 20, EffectObservationDays: 30, EffectMinSamples: 5, Revision: 1}, nil
}
func (store performanceHTTPStore) ListBudgetForecastOutcomes(context.Context, performance.ForecastHistoryFilter) ([]performance.BudgetForecastOutcome, error) {
	now := time.Now().UTC()
	if store.withRecommendation {
		items := make([]performance.BudgetForecastOutcome, 0, 5)
		for index := 0; index < 5; index++ {
			outcome := "cleared"
			if index == 0 {
				outcome = "hit"
			}
			items = append(items, performance.BudgetForecastOutcome{IncidentID: "00000000-0000-4000-8000-00000000001" + string(rune('1'+index)), BudgetID: "00000000-0000-4000-8000-000000000001", BudgetName: "工作每日预算", Module: "work", Period: "daily", Outcome: outcome, OpenedAt: now.Add(-time.Duration(index+1) * time.Hour), DurationMinutes: 60})
		}
		return items, nil
	}
	return []performance.BudgetForecastOutcome{
		{IncidentID: "00000000-0000-4000-8000-000000000011", BudgetID: "00000000-0000-4000-8000-000000000001", BudgetName: "工作每日预算", Module: "work", Period: "daily", Outcome: "hit", OpenedAt: now.Add(-2 * time.Hour), DurationMinutes: 60, ProjectedUtilization: 1.2, ForecastSampleSize: 80},
		{IncidentID: "00000000-0000-4000-8000-000000000012", BudgetID: "00000000-0000-4000-8000-000000000001", BudgetName: "工作每日预算", Module: "work", Period: "daily", Outcome: "cleared", OpenedAt: now.Add(-time.Hour), DurationMinutes: 30, ProjectedUtilization: 1.1, ForecastSampleSize: 60},
	}, nil
}
func (performanceHTTPStore) ListBudgetPolicyDecisions(context.Context, string) ([]performance.BudgetPolicyDecision, error) {
	return nil, nil
}
func (performanceHTTPStore) SaveBudgetPolicyDecision(_ context.Context, item performance.BudgetPolicyDecision) (performance.BudgetPolicyDecision, error) {
	return item, nil
}
func (performanceHTTPStore) AcknowledgeBudgetPolicyEffect(_ context.Context, _ performance.BudgetPolicyEffect, review performance.BudgetPolicyEffectReview) (performance.BudgetPolicyEffectReview, error) {
	return review, nil
}
func (performanceHTTPStore) CloseBudgetPolicyEffect(_ context.Context, effect performance.BudgetPolicyEffect, actor, reason string, now time.Time) (performance.BudgetPolicyEffectReview, error) {
	if effect.Review == nil {
		return performance.BudgetPolicyEffectReview{}, performance.ErrEffectReviewConflict
	}
	review := *effect.Review
	review.Status, review.ClosedBy, review.ClosedReason, review.ClosedAt = "closed", actor, reason, &now
	return review, nil
}
func (performanceHTTPStore) CreateBudget(context.Context, performance.Budget, string) error {
	return nil
}
func (performanceHTTPStore) UpdateBudget(_ context.Context, item performance.Budget, _ int, _ string) (performance.Budget, error) {
	return item, nil
}
func (performanceHTTPStore) ReconcileBudgetIncident(context.Context, performance.BudgetStatus, time.Time) (string, error) {
	return "steady", nil
}

func TestOperatorPerformanceEndpoints(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "performance-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetPerformanceStore(performanceHTTPStore{})

	unauthorized := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/versions", "", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	versions := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/versions?dimension=agent_version&range=7d&module=work", "ops-token", "viewer", nil)
	if versions.Code != http.StatusOK || !strings.Contains(versions.Body.String(), `"success_rate":0.95`) || !strings.Contains(versions.Body.String(), `"key":"work-assistant"`) {
		t.Fatalf("versions=%d %s", versions.Code, versions.Body.String())
	}
	models := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/models?range=7d", "ops-token", "viewer", nil)
	if models.Code != http.StatusOK || !strings.Contains(models.Body.String(), `"model":"model-a"`) {
		t.Fatalf("models=%d %s", models.Code, models.Body.String())
	}
	trend := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/trend?range=24h", "ops-token", "viewer", nil)
	if trend.Code != http.StatusOK || !strings.Contains(trend.Body.String(), `"bucket":"hour"`) || !strings.Contains(trend.Body.String(), `"cost_micros":12000`) {
		t.Fatalf("trend=%d %s", trend.Code, trend.Body.String())
	}
	forecast := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/forecast?range=7d", "ops-token", "viewer", nil)
	if forecast.Code != http.StatusOK || !strings.Contains(forecast.Body.String(), `"budget_forecasts":[]`) || !strings.Contains(forecast.Body.String(), `"requires_validation_before_adoption"`) {
		t.Fatalf("forecast=%d %s", forecast.Code, forecast.Body.String())
	}
	budgets := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/budgets", "ops-token", "viewer", nil)
	if budgets.Code != http.StatusOK || !strings.Contains(budgets.Body.String(), `"budgets":[]`) {
		t.Fatalf("budgets=%d %s", budgets.Code, budgets.Body.String())
	}
	history := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/budgets/forecast-history?range=90d&limit=10", "ops-token", "viewer", nil)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), `"predictions":2`) || !strings.Contains(history.Body.String(), `"hit_rate":0.5`) || !strings.Contains(history.Body.String(), `"outcome":"cleared"`) || !strings.Contains(history.Body.String(), `"recommendations":[]`) {
		t.Fatalf("history=%d %s", history.Code, history.Body.String())
	}
	created := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/performance/budgets", "ops-token", "admin", map[string]any{"name": "工作每日预算", "module": "work", "period": "daily", "cost_limit_micros": 1000000, "warning_ratio": .8, "enabled": true, "forecast_alerts_enabled": true, "forecast_lookback_days": 7, "forecast_min_samples": 20, "effect_observation_days": 45, "effect_min_samples": 8, "reason": "测试预算配置"})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"revision":1`) || !strings.Contains(created.Body.String(), `"forecast_lookback_days":7`) || !strings.Contains(created.Body.String(), `"effect_observation_days":45`) || !strings.Contains(created.Body.String(), `"effect_min_samples":8`) {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	updated := performOperatorJSON(t, server, http.MethodPatch, "/v1/ops/performance/budgets/00000000-0000-4000-8000-000000000001", "ops-token", "admin", map[string]any{"name": "工作每日预算", "module": "work", "period": "daily", "cost_limit_micros": 1000000, "warning_ratio": .8, "enabled": true, "forecast_alerts_enabled": false, "forecast_lookback_days": 14, "forecast_min_samples": 50, "effect_observation_days": 60, "effect_min_samples": 10, "revision": 1, "reason": "调整预测策略"})
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"forecast_alerts_enabled":false`) || !strings.Contains(updated.Body.String(), `"forecast_min_samples":50`) || !strings.Contains(updated.Body.String(), `"effect_observation_days":60`) {
		t.Fatalf("updated=%d %s", updated.Code, updated.Body.String())
	}
	preview := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/performance/budgets/00000000-0000-4000-8000-000000000001/preview", "ops-token", "admin", map[string]any{"name": "工作每日预算", "module": "work", "period": "daily", "cost_limit_micros": 1000000, "warning_ratio": .8, "enabled": true, "forecast_alerts_enabled": false, "forecast_lookback_days": 14, "forecast_min_samples": 50, "effect_observation_days": 60, "effect_min_samples": 10})
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"forecast_risk":"not_evaluated"`) || !strings.Contains(preview.Body.String(), `"note":"仅使用已持久化的实际调用数据进行试算`) {
		t.Fatalf("preview=%d %s", preview.Code, preview.Body.String())
	}
	invalidPreview := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/performance/budgets/00000000-0000-4000-8000-000000000001/preview", "ops-token", "admin", map[string]any{"name": "工作每日预算"})
	if invalidPreview.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid preview=%d %s", invalidPreview.Code, invalidPreview.Body.String())
	}
	evaluated := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/performance/budgets/evaluate", "ops-token", "support", nil)
	if evaluated.Code != http.StatusOK || !strings.Contains(evaluated.Body.String(), `"budgets_evaluated":0`) || !strings.Contains(evaluated.Body.String(), `"projected_exceeded":0`) {
		t.Fatalf("evaluated=%d %s", evaluated.Code, evaluated.Body.String())
	}
	anomalies := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/anomalies?window=1h&baseline=24h", "ops-token", "viewer", nil)
	if anomalies.Code != http.StatusOK || !strings.Contains(anomalies.Body.String(), `"status":"insufficient_data"`) {
		t.Fatalf("anomalies=%d %s", anomalies.Code, anomalies.Body.String())
	}
	invalid := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/versions?dimension=prompt", "ops-token", "viewer", nil)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
	invalidRange := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/performance/versions?range=2h", "ops-token", "viewer", nil)
	if invalidRange.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid range=%d %s", invalidRange.Code, invalidRange.Body.String())
	}

	decisionServer := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "performance-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	decisionServer.SetPerformanceStore(performanceHTTPStore{withRecommendation: true})
	decisionHistory := performOperatorJSON(t, decisionServer, http.MethodGet, "/v1/ops/performance/budgets/forecast-history?range=90d", "ops-token", "viewer", nil)
	var report performance.BudgetForecastHistory
	if err := json.Unmarshal(decisionHistory.Body.Bytes(), &report); err != nil || len(report.Recommendations) != 1 || !report.Recommendations[0].RequiresImpactPreview {
		t.Fatalf("decision history=%d %s err=%v", decisionHistory.Code, decisionHistory.Body.String(), err)
	}
	recommendation := report.Recommendations[0]
	path := "/v1/ops/performance/budgets/" + recommendation.BudgetID + "/recommendations/" + recommendation.RecommendationKey + "/decision?range=90d"
	unauthorizedDecision := performOperatorJSON(t, decisionServer, http.MethodPost, path, "", "", map[string]any{"decision": "accepted", "reason": "准备采用"})
	if unauthorizedDecision.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized decision=%d %s", unauthorizedDecision.Code, unauthorizedDecision.Body.String())
	}
	acceptedDecision := performOperatorJSON(t, decisionServer, http.MethodPost, path, "ops-token", "admin", map[string]any{"decision": "accepted", "reason": "预览通过，准备采用"})
	if acceptedDecision.Code != http.StatusOK || !strings.Contains(acceptedDecision.Body.String(), `"decision":"accepted"`) || !strings.Contains(acceptedDecision.Body.String(), `"decided_by":"admin"`) {
		t.Fatalf("accepted decision=%d %s", acceptedDecision.Code, acceptedDecision.Body.String())
	}
	staleDecision := performOperatorJSON(t, decisionServer, http.MethodPost, "/v1/ops/performance/budgets/"+recommendation.BudgetID+"/recommendations/"+strings.Repeat("a", 64)+"/decision?range=90d", "ops-token", "admin", map[string]any{"decision": "rejected", "reason": "建议已过期"})
	if staleDecision.Code != http.StatusConflict || !strings.Contains(staleDecision.Body.String(), "performance_budget_recommendation_stale") {
		t.Fatalf("stale decision=%d %s", staleDecision.Code, staleDecision.Body.String())
	}

	reviewStore := newPerformanceReviewHTTPStore(time.Now().UTC())
	reviewServer := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "performance-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	reviewServer.SetPerformanceStore(reviewStore)
	reviewPath := "/v1/ops/performance/budgets/" + reviewStore.decision.BudgetID + "/effects/" + reviewStore.decision.ID
	unauthorizedReview := performOperatorJSON(t, reviewServer, http.MethodPost, reviewPath+"/acknowledge", "", "", map[string]any{"disposition": "rollback_planned", "reason": "确认异常"})
	if unauthorizedReview.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized review=%d %s", unauthorizedReview.Code, unauthorizedReview.Body.String())
	}
	acknowledged := performOperatorJSON(t, reviewServer, http.MethodPost, reviewPath+"/acknowledge", "ops-token", "admin", map[string]any{"disposition": "rollback_planned", "reason": "命中率下降，准备回滚"})
	if acknowledged.Code != http.StatusOK || !strings.Contains(acknowledged.Body.String(), `"status":"acknowledged"`) || !strings.Contains(acknowledged.Body.String(), `"disposition":"rollback_planned"`) {
		t.Fatalf("acknowledged review=%d %s", acknowledged.Code, acknowledged.Body.String())
	}
	reviewHistory := performOperatorJSON(t, reviewServer, http.MethodGet, "/v1/ops/performance/budgets/forecast-history?range=365d", "ops-token", "viewer", nil)
	var reviewedReport performance.BudgetForecastHistory
	if err := json.Unmarshal(reviewHistory.Body.Bytes(), &reviewedReport); err != nil || len(reviewedReport.Effects) != 1 || reviewedReport.Effects[0].Review == nil || !reviewedReport.Effects[0].RollbackCanApply {
		t.Fatalf("review history=%d %s err=%v", reviewHistory.Code, reviewHistory.Body.String(), err)
	}
	closed := performOperatorJSON(t, reviewServer, http.MethodPost, reviewPath+"/close", "ops-token", "admin", map[string]any{"reason": "已转交人工变更流程"})
	if closed.Code != http.StatusOK || !strings.Contains(closed.Body.String(), `"status":"closed"`) || !strings.Contains(closed.Body.String(), `"closed_by":"admin"`) {
		t.Fatalf("closed review=%d %s", closed.Code, closed.Body.String())
	}
	reopened := performOperatorJSON(t, reviewServer, http.MethodPost, reviewPath+"/acknowledge", "ops-token", "admin", map[string]any{"disposition": "continue_observing", "reason": "尝试重新打开"})
	if reopened.Code != http.StatusConflict || !strings.Contains(reopened.Body.String(), "performance_budget_effect_review_conflict") {
		t.Fatalf("reopened review=%d %s", reopened.Code, reopened.Body.String())
	}
}
