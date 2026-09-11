package httpserver

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/performance"
)

type performanceBudgetRequest struct {
	Name                  string  `json:"name"`
	Module                string  `json:"module"`
	Period                string  `json:"period"`
	CostLimitMicros       int64   `json:"cost_limit_micros"`
	WarningRatio          float64 `json:"warning_ratio"`
	Enabled               bool    `json:"enabled"`
	ForecastAlertsEnabled *bool   `json:"forecast_alerts_enabled"`
	ForecastLookbackDays  int     `json:"forecast_lookback_days"`
	ForecastMinSamples    int     `json:"forecast_min_samples"`
	EffectObservationDays int     `json:"effect_observation_days"`
	EffectMinSamples      int     `json:"effect_min_samples"`
	Revision              int     `json:"revision"`
	Reason                string  `json:"reason"`
}

type performanceBudgetRecommendationDecisionRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type performanceBudgetEffectReviewRequest struct {
	Disposition string `json:"disposition"`
	Reason      string `json:"reason"`
}

type performanceBudgetEffectCloseRequest struct {
	Reason string `json:"reason"`
}

type performanceBudgetReviewReport struct {
	GeneratedAt time.Time                         `json:"generated_at"`
	From        time.Time                         `json:"from"`
	To          time.Time                         `json:"to"`
	Range       string                            `json:"range"`
	Summary     map[string]int                    `json:"summary"`
	Budgets     []performance.BudgetStatus        `json:"budgets"`
	History     performance.BudgetForecastHistory `json:"history"`
}

func (s *Server) listOperatorVersionPerformance(w http.ResponseWriter, r *http.Request) {
	filter, ok := parsePerformanceFilter(w, r)
	if !ok {
		return
	}
	items, err := s.performance.Versions(r.Context(), filter)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dimension": filter.Dimension, "module": filter.Module, "from": filter.From, "to": filter.To, "items": items,
	})
}

func (s *Server) listOperatorModelPerformance(w http.ResponseWriter, r *http.Request) {
	filter, ok := parsePerformanceFilter(w, r)
	if !ok {
		return
	}
	items, err := s.performance.Models(r.Context(), filter)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"module": filter.Module, "from": filter.From, "to": filter.To, "items": items,
	})
}

func (s *Server) getOperatorPerformanceAnomalies(w http.ResponseWriter, r *http.Request) {
	module := strings.TrimSpace(r.URL.Query().Get("module"))
	current, valid := namedDuration(r.URL.Query().Get("window"), "1h")
	if !valid || (current != time.Hour && current != 6*time.Hour && current != 24*time.Hour) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "window 仅支持 1h、6h 或 24h"})
		return
	}
	baseline, valid := namedDuration(r.URL.Query().Get("baseline"), "24h")
	if !valid || (baseline != 24*time.Hour && baseline != 7*24*time.Hour && baseline != 30*24*time.Hour) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "baseline 仅支持 24h、7d 或 30d"})
		return
	}
	result, err := s.performance.Analyze(r.Context(), time.Now().UTC(), current, baseline, module)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listOperatorPerformanceTrend(w http.ResponseWriter, r *http.Request) {
	filter, ok := parsePerformanceFilter(w, r)
	if !ok {
		return
	}
	bucket := "hour"
	if filter.To.Sub(filter.From) > 7*24*time.Hour {
		bucket = "day"
	}
	items, err := s.performance.Trend(r.Context(), performance.TrendFilter{Module: filter.Module, From: filter.From, To: filter.To, Bucket: bucket})
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"module": filter.Module, "from": filter.From, "to": filter.To, "bucket": bucket, "items": items})
}

func (s *Server) listOperatorPerformanceBudgets(w http.ResponseWriter, r *http.Request) {
	items, err := s.performance.BudgetStatuses(r.Context(), time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budgets": items})
}

func (s *Server) getOperatorPerformanceForecast(w http.ResponseWriter, r *http.Request) {
	filter, ok := parsePerformanceFilter(w, r)
	if !ok {
		return
	}
	result, err := s.performance.Forecast(r.Context(), performance.ForecastFilter{Module: filter.Module, From: filter.From, To: filter.To})
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getOperatorPerformanceBudgetForecastHistory(w http.ResponseWriter, r *http.Request) {
	rangeName := strings.TrimSpace(r.URL.Query().Get("range"))
	if rangeName == "" {
		rangeName = "90d"
	}
	duration, valid := budgetForecastHistoryDuration(rangeName)
	if !valid {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	budgetID := strings.TrimSpace(r.URL.Query().Get("budget_id"))
	if budgetID != "" && !validOperatorUUID(budgetID) {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	now := time.Now().UTC()
	report, err := s.performance.ForecastHistory(r.Context(), performance.ForecastHistoryFilter{
		BudgetID: budgetID, From: now.Add(-duration), To: now, Limit: queryLimitMax(r, 100, 500),
	}, now)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) exportOperatorPerformanceBudgetReview(w http.ResponseWriter, r *http.Request) {
	rangeName := strings.TrimSpace(r.URL.Query().Get("range"))
	if rangeName == "" {
		rangeName = "90d"
	}
	duration, valid := budgetForecastHistoryDuration(rangeName)
	if !valid {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	now := time.Now().UTC()
	history, err := s.performance.ForecastHistory(r.Context(), performance.ForecastHistoryFilter{
		From: now.Add(-duration), To: now, Limit: 500,
	}, now)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	budgets, err := s.performance.BudgetStatuses(r.Context(), now)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	summary := map[string]int{
		"budgets": len(budgets), "normal": 0, "warning": 0, "exceeded": 0, "disabled": 0,
		"predictions": history.Predictions, "hits": history.Hits, "cleared": history.Cleared,
		"observing": history.Observing, "recommendations": len(history.Recommendations), "effects": len(history.Effects),
	}
	for _, budget := range budgets {
		summary[budget.Status]++
	}
	report := performanceBudgetReviewReport{
		GeneratedAt: now, From: history.From, To: history.To, Range: rangeName,
		Summary: summary, Budgets: budgets, History: history,
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" || format == "json" {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="performance-budget-review-%s.json"`, rangeName))
		writeJSON(w, http.StatusOK, report)
		return
	}
	if format != "markdown" {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="performance-budget-review-%s.md"`, rangeName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(performanceBudgetReviewMarkdown(report)))
}

func performanceBudgetReviewMarkdown(report performanceBudgetReviewReport) string {
	var output strings.Builder
	fmt.Fprintf(&output, "# 成本预算周期复盘\n\n- 统计区间：%s — %s\n- 生成时间：%s\n- 预算数：%d；正常 %d；预警 %d；超限 %d；停用 %d\n- 提前预测：%d；命中 %d；提前解除 %d；观察中 %d\n- 策略建议：%d；效果观察：%d\n\n",
		report.From.Format(time.RFC3339), report.To.Format(time.RFC3339), report.GeneratedAt.Format(time.RFC3339),
		report.Summary["budgets"], report.Summary["normal"], report.Summary["warning"], report.Summary["exceeded"], report.Summary["disabled"],
		report.Summary["predictions"], report.Summary["hits"], report.Summary["cleared"], report.Summary["observing"], report.Summary["recommendations"], report.Summary["effects"])
	output.WriteString("## 当前预算\n\n| 预算 | 范围 | 周期 | 使用 / 上限 | 使用率 | 状态 | 修订 |\n| --- | --- | --- | ---: | ---: | --- | ---: |\n")
	for _, item := range report.Budgets {
		module := item.Module
		if module == "" {
			module = "全部模块"
		}
		fmt.Fprintf(&output, "| %s | %s | %s | $%.4f / $%.4f | %.1f%% | %s | r%d |\n", markdownCell(item.Name), markdownCell(module), item.Period, float64(item.UsedCostMicros)/1_000_000, float64(item.CostLimitMicros)/1_000_000, item.Utilization*100, item.Status, item.Revision)
	}
	if len(report.Budgets) == 0 {
		output.WriteString("| — | — | — | — | — | 尚未配置 | — |\n")
	}
	output.WriteString("\n## 策略建议与人工决策\n\n")
	for _, item := range report.History.Recommendations {
		decision := "未决策"
		if item.Feedback != nil {
			decision = item.Feedback.Decision + " · " + item.Feedback.DecidedBy
		}
		fmt.Fprintf(&output, "- **%s**：%s；命中率 %.1f%%（%d 条结论）；%d → %d 样本；%s。\n", markdownCell(item.BudgetName), item.Action, item.HitRate*100, item.Decided, item.CurrentMinSamples, item.ProposedMinSamples, decision)
	}
	if len(report.History.Recommendations) == 0 {
		output.WriteString("- 当前周期没有策略建议。\n")
	}
	output.WriteString("\n## 效果与回滚复盘\n\n")
	for _, item := range report.History.Effects {
		fmt.Fprintf(&output, "- **%s r%d**：%s；应用后 %d 条结论，命中率 %.1f%%。%s", markdownCell(item.BudgetName), item.AppliedBudgetRevision, item.Status, item.After.Decided, item.After.HitRate*100, markdownCell(item.Note))
		if item.RollbackVerification != nil {
			fmt.Fprintf(&output, " 回滚 r%d：%s，%d 条结论。", item.RollbackVerification.BudgetRevision, item.RollbackVerification.Status, item.RollbackVerification.After.Decided)
		}
		output.WriteString("\n")
	}
	if len(report.History.Effects) == 0 {
		output.WriteString("- 当前周期没有已应用策略的效果观察。\n")
	}
	output.WriteString("\n> 报告只包含聚合指标、配置修订和运维决策，不包含用户 Prompt、模型回复或凭据。\n")
	return output.String()
}

func (s *Server) decideOperatorPerformanceBudgetRecommendation(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	budgetID := strings.TrimSpace(r.PathValue("budget_id"))
	if !validOperatorUUID(budgetID) {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	rangeName := strings.TrimSpace(r.URL.Query().Get("range"))
	if rangeName == "" {
		rangeName = "90d"
	}
	duration, valid := budgetForecastHistoryDuration(rangeName)
	if !valid {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	var input performanceBudgetRecommendationDecisionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	now := time.Now().UTC()
	decision, err := s.performance.DecideBudgetPolicyRecommendation(r.Context(), performance.ForecastHistoryFilter{
		BudgetID: budgetID, From: now.Add(-duration), To: now, Limit: 500,
	}, budgetID, r.PathValue("recommendation_key"), performance.BudgetPolicyDecisionInput{Decision: input.Decision, Reason: input.Reason}, currentOperator(r).Actor, now)
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": decision})
}

func (s *Server) acknowledgeOperatorPerformanceBudgetEffect(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	budgetID, decisionID := strings.TrimSpace(r.PathValue("budget_id")), strings.TrimSpace(r.PathValue("decision_id"))
	if !validOperatorUUID(budgetID) || !validOperatorUUID(decisionID) {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	var input performanceBudgetEffectReviewRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	review, err := s.performance.AcknowledgeBudgetPolicyEffect(r.Context(), budgetID, decisionID, performance.BudgetPolicyEffectReviewInput{Disposition: input.Disposition, Reason: input.Reason}, currentOperator(r).Actor, time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": review})
}

func (s *Server) closeOperatorPerformanceBudgetEffect(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	budgetID, decisionID := strings.TrimSpace(r.PathValue("budget_id")), strings.TrimSpace(r.PathValue("decision_id"))
	if !validOperatorUUID(budgetID) || !validOperatorUUID(decisionID) {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	var input performanceBudgetEffectCloseRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	review, err := s.performance.CloseBudgetPolicyEffect(r.Context(), budgetID, decisionID, performance.BudgetPolicyEffectCloseInput{Reason: input.Reason}, currentOperator(r).Actor, time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"review": review})
}

func budgetForecastHistoryDuration(rangeName string) (time.Duration, bool) {
	switch strings.TrimSpace(rangeName) {
	case "30d":
		return 30 * 24 * time.Hour, true
	case "90d":
		return 90 * 24 * time.Hour, true
	case "365d":
		return 365 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func (s *Server) createOperatorPerformanceBudget(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	var input performanceBudgetRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.performance.CreateBudget(r.Context(), input.performanceInput(), currentOperator(r).Actor, input.Reason, time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"budget": item})
}

func (s *Server) updateOperatorPerformanceBudget(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	budgetID := strings.TrimSpace(r.PathValue("budget_id"))
	if !validOperatorUUID(budgetID) {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	var input performanceBudgetRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.performance.UpdateBudget(r.Context(), budgetID, input.Revision, input.performanceInput(), currentOperator(r).Actor, input.Reason, time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"budget": item})
}

func (s *Server) previewOperatorPerformanceBudget(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	budgetID := strings.TrimSpace(r.PathValue("budget_id"))
	if !validOperatorUUID(budgetID) {
		writePerformanceError(w, performance.ErrInvalid)
		return
	}
	var input performanceBudgetRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	preview, err := s.performance.PreviewBudgetImpact(r.Context(), budgetID, input.performanceInput(), time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": preview})
}

func (input performanceBudgetRequest) performanceInput() performance.BudgetInput {
	return performance.BudgetInput{
		Name: input.Name, Module: input.Module, Period: input.Period, CostLimitMicros: input.CostLimitMicros,
		WarningRatio: input.WarningRatio, Enabled: input.Enabled, ForecastAlertsEnabled: input.ForecastAlertsEnabled,
		ForecastLookbackDays: input.ForecastLookbackDays, ForecastMinSamples: input.ForecastMinSamples,
		EffectObservationDays: input.EffectObservationDays, EffectMinSamples: input.EffectMinSamples,
	}
}

func (s *Server) evaluateOperatorPerformanceBudgets(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	report, err := s.performance.EvaluateBudgets(r.Context(), time.Now().UTC())
	if err != nil {
		writePerformanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evaluation": report})
}

func parsePerformanceFilter(w http.ResponseWriter, r *http.Request) (performance.Filter, bool) {
	dimension := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if dimension == "" {
		dimension = performance.DimensionAgentVersion
	}
	rangeName := strings.TrimSpace(r.URL.Query().Get("range"))
	if rangeName == "" {
		rangeName = "7d"
	}
	period, valid := namedDuration(rangeName, "7d")
	if !valid || (rangeName != "24h" && rangeName != "7d" && rangeName != "30d" && rangeName != "90d") {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "range 仅支持 24h、7d、30d 或 90d"})
		return performance.Filter{}, false
	}
	to := time.Now().UTC()
	from := to.Add(-period)
	fromRaw, toRaw := strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to"))
	if (fromRaw == "") != (toRaw == "") {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "from 和 to 必须同时提供"})
		return performance.Filter{}, false
	}
	if fromRaw != "" {
		var err error
		from, err = time.Parse(time.RFC3339, fromRaw)
		if err == nil {
			to, err = time.Parse(time.RFC3339, toRaw)
		}
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "from 和 to 必须是 RFC3339 时间"})
			return performance.Filter{}, false
		}
	}
	filter := performance.Filter{
		Dimension: dimension, Module: strings.TrimSpace(r.URL.Query().Get("module")),
		From: from, To: to, Limit: queryLimitMax(r, 50, 100),
	}
	if filter.Dimension != performance.DimensionAgentVersion && filter.Dimension != performance.DimensionModelProfile {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "dimension 仅支持 agent_version 或 model_profile"})
		return performance.Filter{}, false
	}
	return filter, true
}

func namedDuration(raw, fallback string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = fallback
	}
	switch raw {
	case "7d":
		return 7 * 24 * time.Hour, true
	case "30d":
		return 30 * 24 * time.Hour, true
	case "90d":
		return 90 * 24 * time.Hour, true
	default:
		value, err := time.ParseDuration(raw)
		return value, err == nil && value > 0
	}
}

func writePerformanceError(w http.ResponseWriter, err error) {
	if errors.Is(err, performance.ErrInvalid) {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "成本与质量分析筛选条件无效"})
		return
	}
	if errors.Is(err, performance.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, apiError{Code: "performance_budget_not_found", Message: "预算配置不存在"})
		return
	}
	if errors.Is(err, performance.ErrConflict) {
		writeJSON(w, http.StatusConflict, apiError{Code: "performance_budget_conflict", Message: "预算范围已存在，或配置已被其他管理员更新"})
		return
	}
	if errors.Is(err, performance.ErrRecommendationStale) {
		writeJSON(w, http.StatusConflict, apiError{Code: "performance_budget_recommendation_stale", Message: "调优建议已变化，请刷新历史数据后重新决策"})
		return
	}
	if errors.Is(err, performance.ErrEffectNotReviewable) {
		writeJSON(w, http.StatusConflict, apiError{Code: "performance_budget_effect_not_reviewable", Message: "效果数据尚未形成可处置的回滚建议，或建议已变化"})
		return
	}
	if errors.Is(err, performance.ErrEffectReviewConflict) {
		writeJSON(w, http.StatusConflict, apiError{Code: "performance_budget_effect_review_conflict", Message: "效果建议的处置状态已变化，请刷新后重试"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "成本与质量数据暂时不可用"})
}
