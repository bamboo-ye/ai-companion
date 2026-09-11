package performance

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

type windowStore struct {
	windows        []WindowMetrics
	models         []ModelMetrics
	budgets        []Budget
	reconciled     []BudgetStatus
	outcomes       []BudgetForecastOutcome
	outcomeFilters []ForecastHistoryFilter
	decisions      []BudgetPolicyDecision
	transition     string
}

func (s *windowStore) ListVersionMetrics(context.Context, Filter) ([]VersionMetrics, error) {
	return nil, nil
}
func (s *windowStore) ListModelMetrics(context.Context, Filter) ([]ModelMetrics, error) {
	return s.models, nil
}
func (s *windowStore) MeasureWindow(_ context.Context, filter WindowFilter) (WindowMetrics, error) {
	item := s.windows[0]
	s.windows = s.windows[1:]
	item.From, item.To = filter.From, filter.To
	return item, nil
}
func (s *windowStore) ListTrend(context.Context, TrendFilter) ([]TrendPoint, error) { return nil, nil }
func (s *windowStore) ListBudgets(context.Context) ([]Budget, error)                { return s.budgets, nil }
func (s *windowStore) GetBudget(_ context.Context, budgetID string) (Budget, error) {
	for _, item := range s.budgets {
		if item.ID == budgetID {
			return item, nil
		}
	}
	return Budget{}, ErrNotFound
}
func (s *windowStore) ListBudgetForecastOutcomes(_ context.Context, filter ForecastHistoryFilter) ([]BudgetForecastOutcome, error) {
	s.outcomeFilters = append(s.outcomeFilters, filter)
	return append([]BudgetForecastOutcome(nil), s.outcomes...), nil
}
func (s *windowStore) ListBudgetPolicyDecisions(context.Context, string) ([]BudgetPolicyDecision, error) {
	return append([]BudgetPolicyDecision(nil), s.decisions...), nil
}
func (s *windowStore) SaveBudgetPolicyDecision(_ context.Context, item BudgetPolicyDecision) (BudgetPolicyDecision, error) {
	for index := range s.decisions {
		if s.decisions[index].BudgetID == item.BudgetID && s.decisions[index].RecommendationKey == item.RecommendationKey {
			item.ID = s.decisions[index].ID
			s.decisions[index] = item
			return item, nil
		}
	}
	s.decisions = append(s.decisions, item)
	return item, nil
}
func (s *windowStore) AcknowledgeBudgetPolicyEffect(_ context.Context, effect BudgetPolicyEffect, review BudgetPolicyEffectReview) (BudgetPolicyEffectReview, error) {
	for index := range s.decisions {
		decision := &s.decisions[index]
		if decision.ID != effect.DecisionID || decision.BudgetID != effect.BudgetID {
			continue
		}
		if decision.AppliedAt == nil || (decision.EffectReview != nil && decision.EffectReview.Status == "closed") {
			return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
		}
		if decision.EffectReview != nil && decision.EffectReview.RollbackAppliedAt != nil && review.Disposition != "rollback_planned" {
			return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
		}
		if decision.EffectReview != nil {
			review.RollbackAppliedAt = decision.EffectReview.RollbackAppliedAt
			review.RollbackAppliedBy = decision.EffectReview.RollbackAppliedBy
			review.RollbackBudgetRevision = decision.EffectReview.RollbackBudgetRevision
		}
		reviewCopy := review
		decision.EffectReview = &reviewCopy
		return review, nil
	}
	return BudgetPolicyEffectReview{}, ErrNotFound
}
func (s *windowStore) CloseBudgetPolicyEffect(_ context.Context, effect BudgetPolicyEffect, actor, reason string, now time.Time) (BudgetPolicyEffectReview, error) {
	for index := range s.decisions {
		decision := &s.decisions[index]
		if decision.ID != effect.DecisionID || decision.BudgetID != effect.BudgetID {
			continue
		}
		if decision.EffectReview == nil || decision.EffectReview.Status != "acknowledged" {
			return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
		}
		review := *decision.EffectReview
		review.Status, review.ClosedReason, review.ClosedBy, review.ClosedAt = "closed", reason, actor, &now
		decision.EffectReview = &review
		return review, nil
	}
	return BudgetPolicyEffectReview{}, ErrNotFound
}
func (s *windowStore) CreateBudget(context.Context, Budget, string) error { return nil }
func (s *windowStore) UpdateBudget(_ context.Context, item Budget, expectedRevision int, _ string) (Budget, error) {
	for index := range s.budgets {
		if s.budgets[index].ID != item.ID {
			continue
		}
		if s.budgets[index].Revision != expectedRevision {
			return Budget{}, ErrConflict
		}
		item.CreatedAt, item.CreatedBy = s.budgets[index].CreatedAt, s.budgets[index].CreatedBy
		s.budgets[index] = item
		if item.Enabled && item.ForecastAlertsEnabled {
			for decisionIndex := range s.decisions {
				decision := &s.decisions[decisionIndex]
				if decision.BudgetID == item.ID && decision.Decision == "accepted" && decision.AppliedAt == nil && decision.BudgetRevision == expectedRevision && decision.ProposedLookbackDays == item.ForecastLookbackDays && decision.ProposedMinSamples == item.ForecastMinSamples {
					appliedAt := item.UpdatedAt
					decision.AppliedAt, decision.AppliedBy, decision.AppliedBudgetRevision = &appliedAt, item.UpdatedBy, item.Revision
					decision.EffectObservationDays, decision.EffectMinSamples = item.EffectObservationDays, item.EffectMinSamples
					break
				}
			}
			for decisionIndex := range s.decisions {
				decision := &s.decisions[decisionIndex]
				if decision.BudgetID == item.ID && decision.AppliedBudgetRevision == expectedRevision &&
					decision.CurrentLookbackDays == item.ForecastLookbackDays && decision.CurrentMinSamples == item.ForecastMinSamples &&
					decision.EffectReview != nil && decision.EffectReview.Status == "acknowledged" &&
					decision.EffectReview.Disposition == "rollback_planned" && decision.EffectReview.RollbackAppliedAt == nil {
					appliedAt := item.UpdatedAt
					decision.EffectReview.RollbackAppliedAt = &appliedAt
					decision.EffectReview.RollbackAppliedBy = item.UpdatedBy
					decision.EffectReview.RollbackBudgetRevision = item.Revision
					break
				}
			}
		}
		return item, nil
	}
	return Budget{}, ErrNotFound
}
func (s *windowStore) ReconcileBudgetIncident(_ context.Context, item BudgetStatus, _ time.Time) (string, error) {
	s.reconciled = append(s.reconciled, item)
	if s.transition != "" {
		return s.transition, nil
	}
	return "steady", nil
}

func TestAnalyzeDetectsCriticalCostAndWarningErrorRate(t *testing.T) {
	store := &windowStore{windows: []WindowMetrics{
		{SampleSize: 10, ErrorRate: .25, AverageCostMicros: 4000, P95DurationMS: 1200},
		{SampleSize: 50, ErrorRate: .10, AverageCostMicros: 1000, P95DurationMS: 1000},
	}}
	result, err := NewService(store).Analyze(t.Context(), time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC), time.Hour, 24*time.Hour, "work")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "critical" || len(result.Anomalies) != 2 {
		t.Fatalf("unexpected analysis: %#v", result)
	}
	if result.Anomalies[0].Metric != "error_rate" || result.Anomalies[1].Metric != "average_cost_micros" {
		t.Fatalf("unexpected anomaly order: %#v", result.Anomalies)
	}
}

func TestAnalyzeMarksSmallWindowsAsInsufficient(t *testing.T) {
	store := &windowStore{windows: []WindowMetrics{{SampleSize: 4}, {SampleSize: 100}}}
	result, err := NewService(store).Analyze(t.Context(), time.Now(), time.Hour, 24*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "insufficient_data" || result.Note == "" || len(result.Anomalies) != 0 {
		t.Fatalf("unexpected analysis: %#v", result)
	}
}

func TestVersionsRejectsUnsupportedDimension(t *testing.T) {
	_, err := NewService(NewMemoryStore()).Versions(t.Context(), Filter{Dimension: "prompt", From: time.Now().Add(-time.Hour), To: time.Now(), Limit: 10})
	if err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestBudgetLifecycleAndStatus(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	created, err := service.CreateBudget(t.Context(), BudgetInput{Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .75, Enabled: true}, "admin-a", "建立成本红线", now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.WarningRatio != .75 {
		t.Fatalf("unexpected created budget: %#v", created)
	}
	if !created.ForecastAlertsEnabled || created.ForecastLookbackDays != 7 || created.ForecastMinSamples != 20 || created.EffectObservationDays != 30 || created.EffectMinSamples != 5 {
		t.Fatalf("unexpected default forecast policy: %#v", created)
	}
	forecastEnabled := false
	updated, err := service.UpdateBudget(t.Context(), created.ID, created.Revision, BudgetInput{Name: created.Name, Module: created.Module, Period: created.Period, CostLimitMicros: 120_000, WarningRatio: .8, Enabled: false, ForecastAlertsEnabled: &forecastEnabled, ForecastLookbackDays: 14, ForecastMinSamples: 50, EffectObservationDays: 45, EffectMinSamples: 12}, "admin-b", "临时停用预算", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Enabled || updated.ForecastAlertsEnabled || updated.ForecastLookbackDays != 14 || updated.ForecastMinSamples != 50 || updated.EffectObservationDays != 45 || updated.EffectMinSamples != 12 {
		t.Fatalf("unexpected updated budget: %#v", updated)
	}
	statuses, err := service.BudgetStatuses(t.Context(), now.Add(2*time.Minute))
	if err != nil || len(statuses) != 1 || statuses[0].Status != "disabled" {
		t.Fatalf("unexpected statuses: %#v err=%v", statuses, err)
	}
}

func TestCreateBudgetRejectsMissingReasonAndDuplicateScope(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	input := BudgetInput{Name: "全局月度预算", Period: "monthly", CostLimitMicros: 1_000_000, Enabled: true}
	if _, err := service.CreateBudget(t.Context(), input, "admin", "", time.Now()); err != ErrInvalid {
		t.Fatalf("missing reason err=%v", err)
	}
	created, err := service.CreateBudget(t.Context(), input, "admin", "首次配置", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if created.ForecastLookbackDays != 30 || created.ForecastMinSamples != 20 || !created.ForecastAlertsEnabled || created.EffectObservationDays != 30 || created.EffectMinSamples != 5 {
		t.Fatalf("unexpected monthly forecast defaults: %#v", created)
	}
	if _, err := service.CreateBudget(t.Context(), input, "admin", "重复配置", time.Now()); err != ErrConflict {
		t.Fatalf("duplicate err=%v", err)
	}
}

func TestCreateBudgetRejectsInvalidForecastPolicy(t *testing.T) {
	_, err := NewService(NewMemoryStore()).CreateBudget(t.Context(), BudgetInput{
		Name: "无效预测预算", Period: "daily", CostLimitMicros: 1_000_000, Enabled: true,
		ForecastLookbackDays: 91, ForecastMinSamples: 20,
	}, "admin", "验证预测策略", time.Now())
	if err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestCreateBudgetRejectsInvalidEffectPolicy(t *testing.T) {
	_, err := NewService(NewMemoryStore()).CreateBudget(t.Context(), BudgetInput{
		Name: "无效效果策略", Period: "daily", CostLimitMicros: 1_000_000, Enabled: true,
		ForecastLookbackDays: 7, ForecastMinSamples: 20, EffectObservationDays: 181, EffectMinSamples: 5,
	}, "admin", "验证效果策略", time.Now())
	if err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestEvaluateBudgetsOpensEscalatesAndResolves(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	created, err := service.CreateBudget(t.Context(), BudgetInput{Name: "全局每日预算", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .8, Enabled: true}, "admin", "建立预算告警", now)
	if err != nil {
		t.Fatal(err)
	}
	store.SetFixtures(nil, nil, WindowMetrics{CostMicros: 85_000})
	report, err := service.EvaluateBudgets(t.Context(), now)
	if err != nil || report.Opened != 1 || report.Warning != 1 {
		t.Fatalf("warning report=%+v err=%v", report, err)
	}
	store.SetFixtures(nil, nil, WindowMetrics{CostMicros: 110_000})
	report, err = service.EvaluateBudgets(t.Context(), now.Add(time.Minute))
	if err != nil || report.Escalated != 1 || report.Exceeded != 1 {
		t.Fatalf("exceeded report=%+v err=%v", report, err)
	}
	_, err = service.UpdateBudget(t.Context(), created.ID, created.Revision, BudgetInput{Name: created.Name, Period: created.Period, CostLimitMicros: created.CostLimitMicros, WarningRatio: created.WarningRatio, Enabled: false}, "admin", "暂停预算告警", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	report, err = service.EvaluateBudgets(t.Context(), now.Add(3*time.Minute))
	if err != nil || report.Resolved != 1 {
		t.Fatalf("resolved report=%+v err=%v", report, err)
	}
}

func TestForecastProjectsBudgetLimitAndRecommendsGuardedModel(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := &windowStore{
		budgets: []Budget{{ID: "budget-1", Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true, Revision: 1}},
		windows: []WindowMetrics{
			{CostMicros: 600_000, SampleSize: 20},
			{CostMicros: 8_400_000, SampleSize: 200},
		},
		models: []ModelMetrics{
			{Provider: "provider-a", Model: "premium", Calls: 120, Failed: 1, ErrorRate: .01, AverageCostMicros: 1000, P95LatencyMS: 1000},
			{Provider: "provider-b", Model: "efficient", Calls: 100, Failed: 1, ErrorRate: .01, AverageCostMicros: 500, P95LatencyMS: 1100},
			{Provider: "provider-c", Model: "unreliable", Calls: 100, Failed: 20, ErrorRate: .20, AverageCostMicros: 100, P95LatencyMS: 700},
		},
	}
	result, err := NewService(store).Forecast(t.Context(), ForecastFilter{Module: "work", From: now.Add(-7 * 24 * time.Hour), To: now})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || len(result.BudgetForecasts) != 1 {
		t.Fatalf("unexpected forecast: %#v", result)
	}
	budget := result.BudgetForecasts[0]
	if budget.Risk != "projected_exceeded" || budget.ProjectedCostMicros != 1_200_000 || budget.RequiredSavingsMicros != 200_000 || budget.ProjectedLimitExceededAt == nil {
		t.Fatalf("unexpected budget forecast: %#v", budget)
	}
	if len(result.ModelRecommendations) == 0 {
		t.Fatalf("missing model recommendation: %#v", result)
	}
	recommendation := result.ModelRecommendations[0]
	if recommendation.SourceModel != "premium" || recommendation.TargetModel != "efficient" || recommendation.EstimatedSavingsRatio != .5 || recommendation.Confidence != "high" {
		t.Fatalf("unexpected model recommendation: %#v", recommendation)
	}
}

func TestForecastRejectsInvalidHistoryWindow(t *testing.T) {
	_, err := NewService(NewMemoryStore()).Forecast(t.Context(), ForecastFilter{From: time.Now(), To: time.Now().Add(-time.Hour)})
	if err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}

func TestEvaluateBudgetsOpensQualifiedForecastRisk(t *testing.T) {
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	store := &windowStore{
		budgets: []Budget{{ID: "budget-forecast", Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 20, Revision: 1}},
		windows: []WindowMetrics{
			{CostMicros: 100_000, SampleSize: 10},
			{CostMicros: 14_000_000, SampleSize: 200},
		},
		transition: "opened",
	}
	report, err := NewService(store).EvaluateBudgets(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectedExceeded != 1 || report.Opened != 1 || len(store.reconciled) != 1 {
		t.Fatalf("unexpected evaluation: %#v reconciled=%#v", report, store.reconciled)
	}
	item := store.reconciled[0]
	if item.AlertStatus != "projected_exceeded" || item.Forecast == nil || item.Forecast.ProjectedLimitExceededAt == nil {
		t.Fatalf("unexpected proactive signal: %#v", item)
	}
}

func TestEvaluateBudgetsSuppressesLowSampleForecastRisk(t *testing.T) {
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	store := &windowStore{
		budgets: []Budget{{ID: "budget-low-sample", Name: "生活每日预算", Module: "life", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 200, Revision: 1}},
		windows: []WindowMetrics{
			{CostMicros: 100_000, SampleSize: 2},
			{CostMicros: 14_000_000, SampleSize: 120},
		},
	}
	report, err := NewService(store).EvaluateBudgets(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if report.ProjectedExceeded != 0 || len(store.reconciled) != 1 || store.reconciled[0].AlertStatus != "normal" {
		t.Fatalf("low-sample forecast should not alert: %#v reconciled=%#v", report, store.reconciled)
	}
}

func TestPreviewBudgetImpactComparesPolicyWithoutReconciling(t *testing.T) {
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	store := &windowStore{
		budgets: []Budget{{ID: "budget-preview", Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 300, Revision: 1}},
		windows: []WindowMetrics{
			{CostMicros: 100_000, SampleSize: 10},
			{CostMicros: 14_000_000, SampleSize: 200},
			{CostMicros: 100_000, SampleSize: 10},
			{CostMicros: 14_000_000, SampleSize: 200},
		},
	}
	enabled := true
	preview, err := NewService(store).PreviewBudgetImpact(t.Context(), "budget-preview", BudgetInput{
		Name: "工作每日预算", Module: "work", Period: "daily", CostLimitMicros: 1_000_000, WarningRatio: .8, Enabled: true,
		ForecastAlertsEnabled: &enabled, ForecastLookbackDays: 7, ForecastMinSamples: 20,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Current.Signal != "insufficient_samples" || preview.Current.WillAlert {
		t.Fatalf("unexpected current impact: %#v", preview.Current)
	}
	if preview.Proposed.Signal != "projected_exceeded" || !preview.Proposed.WillAlert || preview.Change != "would_start_alerting" {
		t.Fatalf("unexpected proposed impact: %#v", preview)
	}
	if len(store.reconciled) != 0 {
		t.Fatalf("preview must not reconcile incidents: %#v", store.reconciled)
	}
}

func TestForecastHistoryAggregatesDecidedPredictions(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := &windowStore{budgets: []Budget{{ID: "budget-work", Name: "工作预算", Module: "work", Period: "daily", Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 20}}, outcomes: []BudgetForecastOutcome{
		{IncidentID: "hit-1", BudgetID: "budget-work", BudgetName: "工作预算", Module: "work", Period: "daily", Outcome: "hit", OpenedAt: now.Add(-4 * time.Hour), DurationMinutes: 120},
		{IncidentID: "clear-1", BudgetID: "budget-work", BudgetName: "工作预算", Module: "work", Period: "daily", Outcome: "cleared", OpenedAt: now.Add(-3 * time.Hour), DurationMinutes: 60},
		{IncidentID: "watch-1", BudgetID: "budget-life", BudgetName: "生活预算", Module: "life", Period: "monthly", Outcome: "observing", OpenedAt: now.Add(-time.Hour)},
	}}
	report, err := NewService(store).ForecastHistory(t.Context(), ForecastHistoryFilter{From: now.Add(-90 * 24 * time.Hour), To: now, Limit: 2}, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.Predictions != 3 || report.Hits != 1 || report.Cleared != 1 || report.Observing != 1 || report.Decided != 2 || report.HitRate != .5 || report.AverageLeadTimeMinutes != 120 {
		t.Fatalf("unexpected forecast history: %#v", report)
	}
	if len(report.Budgets) != 2 || report.Budgets[0].BudgetID != "budget-work" || len(report.Items) != 2 {
		t.Fatalf("unexpected forecast breakdown: %#v", report)
	}
	if len(report.Recommendations) != 1 || report.Recommendations[0].Action != "collect_more_data" || report.Recommendations[0].RequiresImpactPreview {
		t.Fatalf("unexpected recommendation: %#v", report.Recommendations)
	}
}

func TestForecastHistoryRecommendsBoundedSampleGateChanges(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	outcomes := make([]BudgetForecastOutcome, 0, 10)
	for index := 0; index < 5; index++ {
		outcome := "cleared"
		if index == 0 {
			outcome = "hit"
		}
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("noise-%d", index), BudgetID: "budget-noise", BudgetName: "噪声预算", Outcome: outcome, DurationMinutes: 60})
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("early-%d", index), BudgetID: "budget-early", BudgetName: "短提前量预算", Outcome: "hit", DurationMinutes: 30})
	}
	store := &windowStore{
		budgets: []Budget{
			{ID: "budget-noise", Name: "噪声预算", Period: "daily", Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 20},
			{ID: "budget-early", Name: "短提前量预算", Period: "daily", Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 100},
		},
		outcomes: outcomes,
	}
	report, err := NewService(store).ForecastHistory(t.Context(), ForecastHistoryFilter{From: now.Add(-90 * 24 * time.Hour), To: now, Limit: 100}, now)
	if err != nil {
		t.Fatal(err)
	}
	recommendations := map[string]BudgetPolicyRecommendation{}
	for _, item := range report.Recommendations {
		recommendations[item.BudgetID] = item
	}
	noise, early := recommendations["budget-noise"], recommendations["budget-early"]
	if noise.Action != "increase_sample_gate" || noise.ProposedMinSamples != 25 || !noise.RequiresImpactPreview {
		t.Fatalf("unexpected noise recommendation: %#v", noise)
	}
	if early.Action != "decrease_sample_gate" || early.ProposedMinSamples != 80 || !early.RequiresImpactPreview {
		t.Fatalf("unexpected early recommendation: %#v", early)
	}
	if len(noise.RecommendationKey) != 64 || len(early.RecommendationKey) != 64 || noise.RecommendationKey == early.RecommendationKey {
		t.Fatalf("unexpected recommendation keys: %#v %#v", noise, early)
	}
}

func TestBudgetPolicyRecommendationDecisionTracksExactSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	outcomes := make([]BudgetForecastOutcome, 0, 5)
	for index := 0; index < 5; index++ {
		outcome := "cleared"
		if index == 0 {
			outcome = "hit"
		}
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("decision-%d", index), BudgetID: "budget-decision", BudgetName: "决策预算", Outcome: outcome, DurationMinutes: 60})
	}
	store := &windowStore{budgets: []Budget{{ID: "budget-decision", Name: "决策预算", Period: "daily", Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 20, Revision: 3}}, outcomes: outcomes}
	service := NewService(store)
	filter := ForecastHistoryFilter{From: now.Add(-90 * 24 * time.Hour), To: now, Limit: 100}
	report, err := service.ForecastHistory(t.Context(), filter, now)
	if err != nil || len(report.Recommendations) != 1 {
		t.Fatalf("ForecastHistory() = %#v, %v", report, err)
	}
	recommendation := report.Recommendations[0]
	decision, err := service.DecideBudgetPolicyRecommendation(t.Context(), filter, recommendation.BudgetID, recommendation.RecommendationKey, BudgetPolicyDecisionInput{Decision: "accepted", Reason: "预览通过，准备人工调整"}, "admin-a", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Decision != "accepted" || decision.Action != recommendation.Action || decision.ProposedMinSamples != recommendation.ProposedMinSamples || decision.DecidedBy != "admin-a" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
	updated, err := service.ForecastHistory(t.Context(), filter, now.Add(2*time.Minute))
	if err != nil || updated.Recommendations[0].Feedback == nil || updated.Recommendations[0].Feedback.ID != decision.ID {
		t.Fatalf("feedback was not attached: %#v, %v", updated.Recommendations, err)
	}
	forecastEnabled := true
	appliedAt := now.Add(2 * time.Minute)
	appliedBudget, err := service.UpdateBudget(t.Context(), recommendation.BudgetID, 3, BudgetInput{Name: "决策预算", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: &forecastEnabled, ForecastLookbackDays: 7, ForecastMinSamples: recommendation.ProposedMinSamples}, "admin-a", "应用已采纳的样本门槛", appliedAt)
	if err != nil || appliedBudget.Revision != 4 {
		t.Fatalf("applied budget=%#v err=%v", appliedBudget, err)
	}
	for index := 0; index < 5; index++ {
		outcome := "hit"
		if index == 4 {
			outcome = "cleared"
		}
		store.outcomes = append(store.outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("after-%d", index), BudgetID: recommendation.BudgetID, BudgetName: "决策预算", Outcome: outcome, DurationMinutes: 120, OpenedAt: appliedAt.Add(time.Duration(index+1) * time.Minute), BudgetRevision: appliedBudget.Revision})
	}
	effectReport, err := service.ForecastHistory(t.Context(), ForecastHistoryFilter{From: now.Add(-90 * 24 * time.Hour), To: now.Add(24 * time.Hour), Limit: 100}, now.Add(24*time.Hour))
	if err != nil || len(effectReport.Effects) != 1 {
		t.Fatalf("effect report=%#v err=%v", effectReport.Effects, err)
	}
	effect := effectReport.Effects[0]
	if effect.Status != "improved" || effect.Before.HitRate != .2 || effect.After.HitRate != .8 || effect.After.AverageLeadTimeMinutes != 120 || effect.AppliedBudgetRevision != 4 || effect.ObservationDays != 30 || effect.MinimumDecidedSamples != 5 || effect.ObservationComplete {
		t.Fatalf("unexpected effect: %#v", effect)
	}
	_, err = service.DecideBudgetPolicyRecommendation(t.Context(), filter, recommendation.BudgetID, strings.Repeat("a", 64), BudgetPolicyDecisionInput{Decision: "rejected", Reason: "暂不调整"}, "admin-a", now.Add(3*time.Minute))
	if err != ErrRecommendationStale {
		t.Fatalf("stale recommendation error = %v", err)
	}
}

func TestBudgetPolicyEffectsUseConfiguredWindowAndRollbackOnlyAfterCompletion(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	appliedAt := now.Add(-40 * 24 * time.Hour)
	decision := BudgetPolicyDecision{
		ID: "decision-regressed", BudgetID: "budget-regressed", RecommendationKey: strings.Repeat("c", 64), Action: "increase_sample_gate",
		OutcomeCount: 5, HitRate: .8, AverageLeadTimeMinutes: 120, CurrentLookbackDays: 7, CurrentMinSamples: 20,
		ProposedLookbackDays: 7, ProposedMinSamples: 25, AppliedAt: &appliedAt, AppliedBy: "admin-a", AppliedBudgetRevision: 4,
		EffectObservationDays: 30, EffectMinSamples: 5,
	}
	outcomes := make([]BudgetForecastOutcome, 0, 6)
	for index := 0; index < 5; index++ {
		outcome := "cleared"
		if index == 0 {
			outcome = "hit"
		}
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("effect-%d", index), BudgetID: decision.BudgetID, Outcome: outcome, OpenedAt: appliedAt.Add(time.Duration(index+1) * 24 * time.Hour), DurationMinutes: 60, BudgetRevision: 4})
	}
	outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: "outside-window", BudgetID: decision.BudgetID, Outcome: "hit", OpenedAt: appliedAt.Add(31 * 24 * time.Hour), DurationMinutes: 180, BudgetRevision: 4})
	effects := budgetPolicyEffects([]BudgetPolicyDecision{decision}, outcomes, []Budget{{ID: decision.BudgetID, Name: "回退测试预算", Period: "daily"}}, ForecastHistoryFilter{From: now.Add(-90 * 24 * time.Hour), To: now}, now)
	if len(effects) != 1 {
		t.Fatalf("effects=%#v", effects)
	}
	effect := effects[0]
	if effect.Status != "regressed" || !effect.ObservationComplete || effect.After.Decided != 5 || !effect.RecommendRollback || effect.RollbackLookbackDays != 7 || effect.RollbackMinSamples != 20 || effect.RollbackReason == "" {
		t.Fatalf("unexpected regressed effect: %#v", effect)
	}

	decision.ID, decision.BudgetID, decision.AppliedBudgetRevision, decision.EffectMinSamples = "decision-insufficient", "budget-insufficient", 5, 10
	insufficient := budgetPolicyEffects([]BudgetPolicyDecision{decision}, []BudgetForecastOutcome{
		{IncidentID: "only-one", BudgetID: decision.BudgetID, Outcome: "hit", OpenedAt: appliedAt.Add(24 * time.Hour), DurationMinutes: 60, BudgetRevision: 5},
	}, []Budget{{ID: decision.BudgetID, Name: "样本不足预算", Period: "daily"}}, ForecastHistoryFilter{From: now.Add(-90 * 24 * time.Hour), To: now}, now)
	if len(insufficient) != 1 || insufficient[0].Status != "insufficient_data" || insufficient[0].RecommendRollback {
		t.Fatalf("unexpected insufficient effect: %#v", insufficient)
	}
}

func TestBudgetRollbackVerificationUsesExactRevisionAndObservationWindow(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	appliedAt := now.Add(-80 * 24 * time.Hour)
	rollbackAppliedAt := now.Add(-40 * 24 * time.Hour)
	decision := BudgetPolicyDecision{
		ID: "decision-rollback-verification", BudgetID: "budget-rollback-verification", RecommendationKey: strings.Repeat("f", 64),
		Action: "increase_sample_gate", Decision: "accepted", OutcomeCount: 5, HitRate: .8, AverageLeadTimeMinutes: 120,
		CurrentLookbackDays: 7, CurrentMinSamples: 20, ProposedLookbackDays: 7, ProposedMinSamples: 25,
		AppliedAt: &appliedAt, AppliedBy: "admin-a", AppliedBudgetRevision: 4, EffectObservationDays: 30, EffectMinSamples: 5,
		EffectReview: &BudgetPolicyEffectReview{
			Status: "closed", Disposition: "rollback_planned", Reason: "异常后回滚", ReviewedBy: "admin-b", ReviewedAt: now.Add(-41 * 24 * time.Hour),
			RollbackAppliedAt: &rollbackAppliedAt, RollbackAppliedBy: "admin-b", RollbackBudgetRevision: 5,
		},
	}
	outcomes := make([]BudgetForecastOutcome, 0, 13)
	for index := 0; index < 5; index++ {
		outcome := "cleared"
		if index == 0 {
			outcome = "hit"
		}
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("regressed-%d", index), BudgetID: decision.BudgetID, Outcome: outcome, OpenedAt: appliedAt.Add(time.Duration(index+1) * 24 * time.Hour), DurationMinutes: 60, BudgetRevision: 4})
	}
	for index := 0; index < 5; index++ {
		outcome := "hit"
		if index == 4 {
			outcome = "cleared"
		}
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("recovered-%d", index), BudgetID: decision.BudgetID, Outcome: outcome, OpenedAt: rollbackAppliedAt.Add(time.Duration(index+1) * 24 * time.Hour), DurationMinutes: 110, BudgetRevision: 5})
	}
	outcomes = append(outcomes,
		BudgetForecastOutcome{IncidentID: "wrong-revision", BudgetID: decision.BudgetID, Outcome: "cleared", OpenedAt: rollbackAppliedAt.Add(6 * 24 * time.Hour), BudgetRevision: 6},
		BudgetForecastOutcome{IncidentID: "outside-window", BudgetID: decision.BudgetID, Outcome: "cleared", OpenedAt: rollbackAppliedAt.Add(31 * 24 * time.Hour), BudgetRevision: 5},
	)
	store := &windowStore{outcomes: outcomes, decisions: []BudgetPolicyDecision{decision}, budgets: []Budget{{ID: decision.BudgetID, Name: "回滚复盘预算", Period: "daily", Revision: 5}}}
	report, err := NewService(store).ForecastHistory(t.Context(), ForecastHistoryFilter{BudgetID: decision.BudgetID, From: now.Add(-60 * 24 * time.Hour), To: now, Limit: 100}, now)
	if err != nil || len(report.Effects) != 1 || report.Effects[0].RollbackVerification == nil {
		t.Fatalf("effects=%#v err=%v", report.Effects, err)
	}
	if len(store.outcomeFilters) != 2 || !store.outcomeFilters[1].From.Equal(appliedAt) {
		t.Fatalf("effect history filters=%#v", store.outcomeFilters)
	}
	verification := report.Effects[0].RollbackVerification
	if verification.Status != "recovered" || !verification.ObservationComplete || verification.BudgetRevision != 5 ||
		verification.After.Predictions != 5 || verification.After.Decided != 5 || verification.After.Hits != 4 || verification.After.HitRate != .8 || verification.After.AverageLeadTimeMinutes != 110 {
		t.Fatalf("rollback verification=%#v", verification)
	}
}

func TestAssessBudgetPolicyRollbackWaitsForConfiguredSamples(t *testing.T) {
	target := BudgetPolicyEffectMetrics{Decided: 10, Hits: 8, HitRate: .8, AverageLeadTimeMinutes: 120}
	regressed := BudgetPolicyEffectMetrics{Decided: 10, Hits: 2, HitRate: .2, AverageLeadTimeMinutes: 60}
	after := BudgetPolicyEffectMetrics{Decided: 4, Hits: 3, HitRate: .75, AverageLeadTimeMinutes: 100}
	if status, _ := assessBudgetPolicyRollback(target, regressed, after, 5, false); status != "collecting" {
		t.Fatalf("status before observation completion=%q", status)
	}
	if status, _ := assessBudgetPolicyRollback(target, regressed, after, 5, true); status != "insufficient_data" {
		t.Fatalf("status after observation completion=%q", status)
	}
}

func TestBudgetPolicyEffectReviewLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	appliedAt := now.Add(-40 * 24 * time.Hour)
	decision := BudgetPolicyDecision{
		ID: "decision-review", BudgetID: "budget-review", RecommendationKey: strings.Repeat("d", 64), Action: "increase_sample_gate", Decision: "accepted",
		OutcomeCount: 5, HitRate: .8, AverageLeadTimeMinutes: 120, CurrentLookbackDays: 7, CurrentMinSamples: 20,
		ProposedLookbackDays: 7, ProposedMinSamples: 25, AppliedAt: &appliedAt, AppliedBy: "admin-a", AppliedBudgetRevision: 4,
		EffectObservationDays: 30, EffectMinSamples: 5,
	}
	outcomes := make([]BudgetForecastOutcome, 0, 5)
	for index := 0; index < 5; index++ {
		outcome := "cleared"
		if index == 0 {
			outcome = "hit"
		}
		outcomes = append(outcomes, BudgetForecastOutcome{IncidentID: fmt.Sprintf("review-%d", index), BudgetID: decision.BudgetID, BudgetName: "处置测试预算", Outcome: outcome, OpenedAt: appliedAt.Add(time.Duration(index+1) * 24 * time.Hour), DurationMinutes: 60, BudgetRevision: 4})
	}
	store := &windowStore{budgets: []Budget{{
		ID: decision.BudgetID, Name: "处置测试预算", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .8,
		Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 25,
		EffectObservationDays: 30, EffectMinSamples: 5, Revision: 4,
	}}, outcomes: outcomes, decisions: []BudgetPolicyDecision{decision}}
	service := NewService(store)
	review, err := service.AcknowledgeBudgetPolicyEffect(t.Context(), decision.BudgetID, decision.ID, BudgetPolicyEffectReviewInput{Disposition: "rollback_planned", Reason: "指标明显下降，准备人工回滚"}, "admin-b", now)
	if err != nil || review.Status != "acknowledged" || review.Disposition != "rollback_planned" || review.ReviewedBy != "admin-b" {
		t.Fatalf("review=%#v err=%v", review, err)
	}
	report, err := service.ForecastHistory(t.Context(), ForecastHistoryFilter{BudgetID: decision.BudgetID, From: now.Add(-90 * 24 * time.Hour), To: now, Limit: 100}, now)
	if err != nil || len(report.Effects) != 1 || report.Effects[0].Review == nil || report.Effects[0].Review.Status != "acknowledged" || !report.Effects[0].RollbackCanApply {
		t.Fatalf("reviewed effect=%#v err=%v", report.Effects, err)
	}
	rolledBack, err := service.UpdateBudget(t.Context(), decision.BudgetID, 4, BudgetInput{
		Name: "处置测试预算", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .8, Enabled: true,
		ForecastLookbackDays: 7, ForecastMinSamples: 20, EffectObservationDays: 30, EffectMinSamples: 5,
	}, "admin-b", "恢复推荐应用前的预测参数", now.Add(time.Minute))
	if err != nil || rolledBack.Revision != 5 {
		t.Fatalf("rolled back budget=%#v err=%v", rolledBack, err)
	}
	report, err = service.ForecastHistory(t.Context(), ForecastHistoryFilter{BudgetID: decision.BudgetID, From: now.Add(-90 * 24 * time.Hour), To: now.Add(2 * time.Minute), Limit: 100}, now.Add(2*time.Minute))
	if err != nil || len(report.Effects) != 1 || report.Effects[0].Review == nil || report.Effects[0].Review.RollbackAppliedAt == nil ||
		report.Effects[0].Review.RollbackAppliedBy != "admin-b" || report.Effects[0].Review.RollbackBudgetRevision != 5 || report.Effects[0].RollbackCanApply ||
		report.Effects[0].RollbackVerification == nil || report.Effects[0].RollbackVerification.Status != "collecting" || report.Effects[0].RollbackVerification.BudgetRevision != 5 {
		t.Fatalf("rollback application=%#v err=%v", report.Effects, err)
	}
	_, err = service.AcknowledgeBudgetPolicyEffect(t.Context(), decision.BudgetID, decision.ID, BudgetPolicyEffectReviewInput{Disposition: "continue_observing", Reason: "尝试改为继续观察"}, "admin-b", now.Add(2*time.Minute))
	if err != ErrEffectReviewConflict {
		t.Fatalf("change applied rollback disposition err=%v", err)
	}
	closed, err := service.CloseBudgetPolicyEffect(t.Context(), decision.BudgetID, decision.ID, BudgetPolicyEffectCloseInput{Reason: "已完成影响预览并转交变更流程"}, "admin-c", now.Add(3*time.Minute))
	if err != nil || closed.Status != "closed" || closed.ClosedBy != "admin-c" || closed.ClosedAt == nil {
		t.Fatalf("closed=%#v err=%v", closed, err)
	}
	_, err = service.CloseBudgetPolicyEffect(t.Context(), decision.BudgetID, decision.ID, BudgetPolicyEffectCloseInput{Reason: "重复关闭"}, "admin-c", now.Add(4*time.Minute))
	if err != ErrEffectReviewConflict {
		t.Fatalf("second close err=%v", err)
	}
}

func TestBudgetRollbackPlanDoesNotLinkAfterInterveningRevision(t *testing.T) {
	now := time.Date(2026, 9, 8, 15, 0, 0, 0, time.UTC)
	appliedAt := now.Add(-40 * 24 * time.Hour)
	reviewedAt := now.Add(-time.Hour)
	decision := BudgetPolicyDecision{
		ID: "decision-stale-rollback", BudgetID: "budget-stale-rollback", RecommendationKey: strings.Repeat("e", 64),
		Action: "increase_sample_gate", Decision: "accepted", CurrentLookbackDays: 7, CurrentMinSamples: 20,
		ProposedLookbackDays: 7, ProposedMinSamples: 25, AppliedAt: &appliedAt, AppliedBudgetRevision: 4,
		EffectReview: &BudgetPolicyEffectReview{Status: "acknowledged", Disposition: "rollback_planned", Reason: "等待回滚", ReviewedBy: "admin-a", ReviewedAt: reviewedAt},
	}
	store := &windowStore{budgets: []Budget{{
		ID: decision.BudgetID, Name: "过期回滚计划", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .8,
		Enabled: true, ForecastAlertsEnabled: true, ForecastLookbackDays: 7, ForecastMinSamples: 25,
		EffectObservationDays: 30, EffectMinSamples: 5, Revision: 5,
	}}, decisions: []BudgetPolicyDecision{decision}}
	service := NewService(store)
	updated, err := service.UpdateBudget(t.Context(), decision.BudgetID, 5, BudgetInput{
		Name: "过期回滚计划", Period: "daily", CostLimitMicros: 100_000, WarningRatio: .8, Enabled: true,
		ForecastLookbackDays: 7, ForecastMinSamples: 20, EffectObservationDays: 30, EffectMinSamples: 5,
	}, "admin-b", "尝试恢复旧计划参数", now)
	if err != nil || updated.Revision != 6 {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	if store.decisions[0].EffectReview == nil || store.decisions[0].EffectReview.RollbackAppliedAt != nil || store.decisions[0].EffectReview.RollbackBudgetRevision != 0 {
		t.Fatalf("stale rollback should remain unlinked: %#v", store.decisions[0].EffectReview)
	}
}

func TestForecastHistoryRejectsOversizedRange(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewService(NewMemoryStore()).ForecastHistory(t.Context(), ForecastHistoryFilter{From: now.Add(-366 * 24 * time.Hour), To: now}, now)
	if err != ErrInvalid {
		t.Fatalf("expected ErrInvalid, got %v", err)
	}
}
