package postgresstore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/incident"
	"github.com/windcry1/ai-companion/internal/performance"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func TestPerformanceBudgetForecastPolicyIncidentWorkflow(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	existing, err := store.ListBudgets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type scope struct{ module, period string }
	candidates := []scope{{"", "daily"}, {"", "monthly"}, {"companion", "daily"}, {"companion", "monthly"}, {"life", "daily"}, {"life", "monthly"}, {"work", "daily"}, {"work", "monthly"}}
	used := map[scope]bool{}
	for _, item := range existing {
		used[scope{item.Module, item.Period}] = true
	}
	var selected scope
	found := false
	for _, candidate := range candidates {
		if !used[candidate] {
			selected, found = candidate, true
			break
		}
	}
	if !found {
		t.Skip("all budget scopes are occupied")
	}

	now := time.Now().UTC().Truncate(time.Second)
	performanceService := performance.NewService(store)
	forecastAlerts := true
	budget, err := performanceService.CreateBudget(ctx, performance.BudgetInput{
		Name: "预算事故集成测试", Module: selected.module, Period: selected.period,
		CostLimitMicros: 100_000, WarningRatio: .8, Enabled: true, ForecastAlertsEnabled: &forecastAlerts,
		ForecastLookbackDays: 14, ForecastMinSamples: 40, EffectObservationDays: 45, EffectMinSamples: 8,
	}, "integration-test", "验证预算事故事务", now)
	if err != nil {
		t.Fatal(err)
	}
	persistedBudget, err := store.GetBudget(ctx, budget.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persistedBudget.ForecastAlertsEnabled || persistedBudget.ForecastLookbackDays != 14 || persistedBudget.ForecastMinSamples != 40 || persistedBudget.EffectObservationDays != 45 || persistedBudget.EffectMinSamples != 8 {
		t.Fatalf("persisted forecast policy=%#v", persistedBudget)
	}
	incidentID := ""
	decisionID := ""
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.outbox_events WHERE aggregate_id IN (SELECT delivery_id::text FROM ops.incident_notifications WHERE incident_id IN (SELECT id FROM ops.incidents WHERE source_type='performance_budget' AND source_id=$1))`, budget.ID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE resource_type='incident' AND resource_id IN (SELECT id FROM ops.incidents WHERE source_type='performance_budget' AND source_id=$1)`, budget.ID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.incidents WHERE source_type='performance_budget' AND source_id=$1`, budget.ID)
		if decisionID != "" {
			_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE resource_type='performance_budget_recommendation' AND resource_id=$1`, decisionID)
		}
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.performance_budget_recommendation_decisions WHERE budget_id=$1`, budget.ID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM eventing.audit_logs WHERE resource_type='performance_budget' AND resource_id=$1`, budget.ID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM ops.performance_budgets WHERE id=$1`, budget.ID)
	})

	decisionID, err = id.New()
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.SaveBudgetPolicyDecision(ctx, performance.BudgetPolicyDecision{
		ID: decisionID, BudgetID: budget.ID, RecommendationKey: strings.Repeat("b", 64), Action: "increase_sample_gate", Decision: "accepted", Confidence: "low",
		OutcomeCount: 5, HitRate: .2, AverageLeadTimeMinutes: 60, CurrentLookbackDays: 14, CurrentMinSamples: 40,
		ProposedLookbackDays: 14, ProposedMinSamples: 50, RecommendationReason: "集成测试建议", Reason: "确认记录反馈但不自动调参", DecidedBy: "integration-test", DecidedAt: now,
		BudgetRevision: budget.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := store.ListBudgetPolicyDecisions(ctx, budget.ID)
	if err != nil || len(decisions) != 1 || decisions[0].ID != decision.ID || decisions[0].Decision != "accepted" {
		t.Fatalf("persisted decisions=%#v err=%v", decisions, err)
	}
	var auditAction, auditBudgetID string
	if err = store.db.QueryRowContext(ctx, `SELECT action,metadata->>'budget_id' FROM eventing.audit_logs WHERE resource_type='performance_budget_recommendation' AND resource_id=$1 ORDER BY occurred_at DESC LIMIT 1`, decision.ID).Scan(&auditAction, &auditBudgetID); err != nil {
		t.Fatal(err)
	}
	if auditAction != "performance_budget.recommendation.accepted" || auditBudgetID != budget.ID {
		t.Fatalf("unexpected recommendation audit action=%q budget=%q", auditAction, auditBudgetID)
	}

	windowFrom := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if budget.Period == "monthly" {
		windowFrom = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	periodEnd := windowFrom.Add(24 * time.Hour)
	if budget.Period == "monthly" {
		periodEnd = windowFrom.AddDate(0, 1, 0)
	}
	limitAt := now.Add(3 * time.Hour)
	budgetStatus := performance.BudgetStatus{
		Budget: budget, WindowFrom: windowFrom, WindowTo: now, UsedCostMicros: 20_000, RemainingMicros: 80_000, Utilization: .2, Status: "normal", AlertStatus: "projected_exceeded",
		Forecast: &performance.BudgetForecast{BudgetID: budget.ID, Risk: "projected_exceeded", Confidence: "high", SampleSize: 120, ObservedHours: 168, ProjectedCostMicros: 120_000, ProjectedUtilization: 1.2, RequiredSavingsMicros: 20_000, PeriodEndsAt: periodEnd, ProjectedLimitExceededAt: &limitAt},
	}
	transition, err := store.ReconcileBudgetIncident(ctx, budgetStatus, now)
	if err != nil || transition != "opened" {
		t.Fatalf("open transition=%q err=%v", transition, err)
	}
	items, err := store.ListIncidents(ctx, incident.IncidentFilter{Source: "performance_budget", Limit: 20})
	if err != nil || len(items) == 0 || items[0].SourceID != budget.ID || items[0].Severity != "warning" || items[0].EventPrefix != "performance.budget.projected_exceeded" || items[0].ObservedValue != 120 {
		t.Fatalf("opened incidents=%#v err=%v", items, err)
	}
	incidentID = items[0].ID
	notifications, err := store.ListIncidentNotifications(ctx, incidentID)
	if err != nil || len(notifications) != 0 {
		t.Fatalf("opened notifications=%#v err=%v", notifications, err)
	}

	budgetStatus.UsedCostMicros, budgetStatus.RemainingMicros, budgetStatus.Utilization, budgetStatus.Status, budgetStatus.AlertStatus = 120_000, 0, 1.2, "exceeded", "exceeded"
	transition, err = store.ReconcileBudgetIncident(ctx, budgetStatus, now.Add(time.Minute))
	if err != nil || transition != "escalated" {
		t.Fatalf("escalate transition=%q err=%v", transition, err)
	}
	escalated, err := store.GetIncident(ctx, incidentID)
	if err != nil || escalated.Severity != "critical" || escalated.EventPrefix != "performance.budget.exceeded" {
		t.Fatalf("escalated incident=%#v err=%v", escalated, err)
	}

	budgetStatus.UsedCostMicros, budgetStatus.RemainingMicros, budgetStatus.Utilization, budgetStatus.Status, budgetStatus.AlertStatus = 20_000, 80_000, .2, "normal", "normal"
	transition, err = store.ReconcileBudgetIncident(ctx, budgetStatus, now.Add(2*time.Minute))
	if err != nil || transition != "resolved" {
		t.Fatalf("resolve transition=%q err=%v", transition, err)
	}
	resolved, err := store.GetIncident(ctx, incidentID)
	if err != nil || resolved.Status != "resolved" || resolved.ResolvedBy != "system" {
		t.Fatalf("resolved incident=%#v err=%v", resolved, err)
	}
	history, err := performanceService.ForecastHistory(ctx, performance.ForecastHistoryFilter{BudgetID: budget.ID, From: now.Add(-time.Hour), To: now.Add(time.Hour), Limit: 10}, now.Add(2*time.Minute))
	if err != nil || history.Predictions != 1 || history.Hits != 1 || history.Cleared != 0 || len(history.Items) != 1 || len(history.Recommendations) != 1 || history.Recommendations[0].Action != "collect_more_data" {
		t.Fatalf("forecast history=%#v err=%v", history, err)
	}
	outcome := history.Items[0]
	if outcome.IncidentID != incidentID || outcome.Outcome != "hit" || outcome.DurationMinutes != 1 || outcome.ProjectedCostMicros != 120_000 || outcome.ForecastSampleSize != 120 || outcome.PredictedLimitExceededAt == nil || outcome.BudgetRevision != budget.Revision {
		t.Fatalf("forecast outcome=%#v", outcome)
	}
	notifications, err = store.ListIncidentNotifications(ctx, incidentID)
	if err != nil || len(notifications) != 0 {
		t.Fatalf("resolved notifications=%#v err=%v", notifications, err)
	}
	appliedBudget, err := performanceService.UpdateBudget(ctx, budget.ID, budget.Revision, performance.BudgetInput{
		Name: budget.Name, Module: budget.Module, Period: budget.Period, CostLimitMicros: budget.CostLimitMicros,
		WarningRatio: budget.WarningRatio, Enabled: budget.Enabled, ForecastAlertsEnabled: &forecastAlerts,
		ForecastLookbackDays: 14, ForecastMinSamples: 50, EffectObservationDays: 21, EffectMinSamples: 6,
	}, "integration-test", "应用已采纳的调优建议", now.Add(3*time.Minute))
	if err != nil || appliedBudget.Revision != budget.Revision+1 {
		t.Fatalf("applied budget=%#v err=%v", appliedBudget, err)
	}
	decisions, err = store.ListBudgetPolicyDecisions(ctx, budget.ID)
	if err != nil || len(decisions) != 1 || decisions[0].AppliedAt == nil || decisions[0].AppliedBy != "integration-test" || decisions[0].AppliedBudgetRevision != appliedBudget.Revision || decisions[0].EffectObservationDays != 21 || decisions[0].EffectMinSamples != 6 {
		t.Fatalf("applied decisions=%#v err=%v", decisions, err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT action,metadata->>'budget_revision' FROM eventing.audit_logs WHERE resource_type='performance_budget_recommendation' AND resource_id=$1 AND action='performance_budget.recommendation.applied' ORDER BY occurred_at DESC LIMIT 1`, decision.ID).Scan(&auditAction, &auditBudgetID); err != nil {
		t.Fatal(err)
	}
	if auditAction != "performance_budget.recommendation.applied" || auditBudgetID != "2" {
		t.Fatalf("unexpected application audit action=%q revision=%q", auditAction, auditBudgetID)
	}
	reviewEffect := performance.BudgetPolicyEffect{
		DecisionID: decision.ID, BudgetID: budget.ID, Status: "regressed", AppliedBudgetRevision: appliedBudget.Revision,
		ObservationEndsAt: now.Add(24 * time.Hour), MinimumDecidedSamples: 6,
		Before: performance.BudgetPolicyEffectMetrics{Decided: 5, HitRate: .8}, After: performance.BudgetPolicyEffectMetrics{Decided: 6, HitRate: .2},
		RollbackLookbackDays: 14, RollbackMinSamples: 40,
	}
	review, err := store.AcknowledgeBudgetPolicyEffect(ctx, reviewEffect, performance.BudgetPolicyEffectReview{
		Status: "acknowledged", Disposition: "rollback_planned", Reason: "集成测试确认效果异常", ReviewedBy: "integration-test", ReviewedAt: now.Add(4 * time.Minute),
	})
	if err != nil || review.Status != "acknowledged" || review.Disposition != "rollback_planned" {
		t.Fatalf("effect review=%#v err=%v", review, err)
	}
	decisions, err = store.ListBudgetPolicyDecisions(ctx, budget.ID)
	if err != nil || len(decisions) != 1 || decisions[0].EffectReview == nil || decisions[0].EffectReview.Status != "acknowledged" {
		t.Fatalf("reviewed decisions=%#v err=%v", decisions, err)
	}
	rolledBackBudget, err := performanceService.UpdateBudget(ctx, budget.ID, appliedBudget.Revision, performance.BudgetInput{
		Name: budget.Name, Module: budget.Module, Period: budget.Period, CostLimitMicros: budget.CostLimitMicros,
		WarningRatio: budget.WarningRatio, Enabled: budget.Enabled, ForecastAlertsEnabled: &forecastAlerts,
		ForecastLookbackDays: 14, ForecastMinSamples: 40, EffectObservationDays: 21, EffectMinSamples: 6,
	}, "integration-test", "执行已确认的人工回滚", now.Add(5*time.Minute))
	if err != nil || rolledBackBudget.Revision != appliedBudget.Revision+1 {
		t.Fatalf("rolled back budget=%#v err=%v", rolledBackBudget, err)
	}
	decisions, err = store.ListBudgetPolicyDecisions(ctx, budget.ID)
	if err != nil || len(decisions) != 1 || decisions[0].EffectReview == nil || decisions[0].EffectReview.RollbackAppliedAt == nil ||
		decisions[0].EffectReview.RollbackAppliedBy != "integration-test" || decisions[0].EffectReview.RollbackBudgetRevision != rolledBackBudget.Revision {
		t.Fatalf("rollback-linked decisions=%#v err=%v", decisions, err)
	}
	rollbackHistory, err := performanceService.ForecastHistory(ctx, performance.ForecastHistoryFilter{BudgetID: budget.ID, From: now.Add(-time.Hour), To: now.Add(7 * time.Minute), Limit: 10}, now.Add(6*time.Minute))
	if err != nil || len(rollbackHistory.Effects) != 1 || rollbackHistory.Effects[0].RollbackVerification == nil ||
		rollbackHistory.Effects[0].RollbackVerification.Status != "collecting" || rollbackHistory.Effects[0].RollbackVerification.BudgetRevision != rolledBackBudget.Revision {
		t.Fatalf("rollback history=%#v err=%v", rollbackHistory.Effects, err)
	}
	closedReview, err := performanceService.CloseBudgetPolicyEffect(ctx, budget.ID, decision.ID, performance.BudgetPolicyEffectCloseInput{Reason: "集成测试关闭效果建议"}, "integration-test", now.Add(6*time.Minute))
	if err != nil || closedReview.Status != "closed" || closedReview.ClosedAt == nil {
		t.Fatalf("closed effect review=%#v err=%v", closedReview, err)
	}
	var reviewAuditCount int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM eventing.audit_logs WHERE resource_type='performance_budget_recommendation' AND resource_id=$1 AND action IN ('performance_budget.effect.acknowledged','performance_budget.effect.rollback.applied','performance_budget.effect.closed')`, decision.ID).Scan(&reviewAuditCount); err != nil || reviewAuditCount != 3 {
		t.Fatalf("effect review audit count=%d err=%v", reviewAuditCount, err)
	}
	var verificationStatus, verificationRevision string
	if err = store.db.QueryRowContext(ctx, `SELECT metadata->>'rollback_verification_status',metadata->>'rollback_budget_revision' FROM eventing.audit_logs WHERE resource_type='performance_budget_recommendation' AND resource_id=$1 AND action='performance_budget.effect.closed' ORDER BY occurred_at DESC LIMIT 1`, decision.ID).Scan(&verificationStatus, &verificationRevision); err != nil {
		t.Fatal(err)
	}
	if verificationStatus != "collecting" || verificationRevision != "3" {
		t.Fatalf("rollback verification audit status=%q revision=%q", verificationStatus, verificationRevision)
	}
}
