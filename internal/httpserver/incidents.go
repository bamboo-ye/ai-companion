package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/incident"
)

type createAlertRuleRequest struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Service       string `json:"service"`
	Level         string `json:"level"`
	EventPrefix   string `json:"event_prefix"`
	WindowMinutes int    `json:"window_minutes"`
	Threshold     int    `json:"threshold"`
	Severity      string `json:"severity"`
	Enabled       bool   `json:"enabled"`
	Reason        string `json:"reason"`
}

type createAlertSubscriptionRequest struct {
	Name             string `json:"name"`
	RuleID           string `json:"rule_id"`
	Channel          string `json:"channel"`
	Target           string `json:"target"`
	MinimumSeverity  string `json:"minimum_severity"`
	NotifyOnOpen     bool   `json:"notify_on_open"`
	NotifyOnResolved bool   `json:"notify_on_resolved"`
	Enabled          bool   `json:"enabled"`
	Reason           string `json:"reason"`
}

func (s *Server) listOperatorAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.incidents.Rules(r.Context())
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

func (s *Server) createOperatorAlertRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	var input createAlertRuleRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	rule, err := s.incidents.CreateRule(r.Context(), incident.AlertRule{
		Name: input.Name, Description: input.Description, Service: input.Service,
		Level: input.Level, EventPrefix: input.EventPrefix, WindowMinutes: input.WindowMinutes,
		Threshold: input.Threshold, Severity: input.Severity, Enabled: input.Enabled,
	}, currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"rule": rule})
}

func (s *Server) updateOperatorAlertRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	var input struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	rule, err := s.incidents.SetRuleEnabled(r.Context(), r.PathValue("rule_id"), input.Enabled, currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": rule})
}

func (s *Server) listOperatorAlertSubscriptions(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	items, err := s.incidents.Subscriptions(r.Context())
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": items})
}

func (s *Server) createOperatorAlertSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	var input createAlertSubscriptionRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.incidents.CreateSubscription(r.Context(), incident.AlertSubscription{
		Name: input.Name, RuleID: input.RuleID, Channel: input.Channel, Target: input.Target,
		MinimumSeverity: input.MinimumSeverity, NotifyOnOpen: input.NotifyOnOpen,
		NotifyOnResolved: input.NotifyOnResolved, Enabled: input.Enabled,
	}, currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"subscription": item})
}

func (s *Server) updateOperatorAlertSubscription(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") {
		return
	}
	var input struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.incidents.SetSubscriptionEnabled(r.Context(), r.PathValue("subscription_id"), input.Enabled, currentOperator(r).Actor, input.Reason)
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": item})
}

func (s *Server) evaluateOperatorAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	report, err := s.incidents.Evaluate(r.Context(), time.Now().UTC())
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"evaluation": report})
}

func (s *Server) listOperatorIncidents(w http.ResponseWriter, r *http.Request) {
	items, err := s.incidents.Incidents(r.Context(), incident.IncidentFilter{
		Status: strings.TrimSpace(r.URL.Query().Get("status")), Severity: strings.TrimSpace(r.URL.Query().Get("severity")), Source: strings.TrimSpace(r.URL.Query().Get("source")), Limit: queryLimit(r, 100),
	})
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": items})
}

func (s *Server) getOperatorIncident(w http.ResponseWriter, r *http.Request) {
	item, err := s.incidents.GetIncident(r.Context(), r.PathValue("incident_id"))
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incident": item})
}

func (s *Server) listOperatorIncidentNotifications(w http.ResponseWriter, r *http.Request) {
	items, err := s.incidents.Notifications(r.Context(), r.PathValue("incident_id"))
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": items})
}

func (s *Server) exportOperatorIncidentEvidence(w http.ResponseWriter, r *http.Request) {
	bundle, err := s.incidents.Evidence(r.Context(), r.PathValue("incident_id"))
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" || format == "json" {
		writeJSON(w, http.StatusOK, bundle)
		return
	}
	if format != "markdown" {
		writeIncidentError(w, incident.ErrValidation)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="incident-%s-evidence.md"`, bundle.Incident.ID))
	_, _ = w.Write([]byte(renderIncidentEvidenceMarkdown(bundle)))
}

func renderIncidentEvidenceMarkdown(bundle incident.EvidenceBundle) string {
	item := bundle.Incident
	var output strings.Builder
	fmt.Fprintf(&output, "# 事故证据报告：%s\n\n", markdownCell(item.Title))
	signalLabel := "日志信号"
	if item.SourceType == "performance_budget" {
		signalLabel = "预算信号"
	}
	fmt.Fprintf(&output, "- 事故 ID：`%s`\n- 来源：%s\n- 状态：%s\n- 级别：%s\n- 服务/范围：%s\n- %s：%s %s\n- 观测值 / 阈值：%d / %d\n- 观测窗口：%d 分钟\n- 开单时间：%s\n- 生成时间：%s\n\n", item.ID, item.SourceType, item.Status, item.Severity, markdownCell(defaultIncidentValue(item.Service, "全部服务")), signalLabel, item.Level, markdownCell(item.EventPrefix), item.ObservedValue, item.Threshold, item.WindowMinutes, item.OpenedAt.UTC().Format(time.RFC3339), bundle.GeneratedAt.UTC().Format(time.RFC3339))
	output.WriteString("## 处理时间线\n\n| 时间 | 动作 | 执行者 | 原因 |\n| --- | --- | --- | --- |\n")
	for _, activity := range bundle.Activity {
		fmt.Fprintf(&output, "| %s | %s | %s | %s |\n", activity.OccurredAt.UTC().Format(time.RFC3339), markdownCell(activity.Action), markdownCell(defaultIncidentValue(activity.Actor, activity.ActorType)), markdownCell(activity.Reason))
	}
	if len(bundle.Activity) == 0 {
		output.WriteString("| — | 暂无审计事件 | — | — |\n")
	}
	output.WriteString("\n## 通知投递\n\n| 时间 | 事件 | 渠道 | 目标 | 状态 | 尝试次数 |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, notification := range bundle.Notifications {
		fmt.Fprintf(&output, "| %s | %s | %s | %s | %s | %d |\n", notification.CreatedAt.UTC().Format(time.RFC3339), markdownCell(notification.Transition), markdownCell(notification.Channel), markdownCell(notification.Target), markdownCell(notification.Status), notification.Attempts)
	}
	if len(bundle.Notifications) == 0 {
		output.WriteString("| — | 暂无通知 | — | — | — | 0 |\n")
	}
	if item.SourceType == "performance_budget" {
		var evidence struct {
			SignalStatus             string     `json:"signal_status"`
			UsedCostMicros           int64      `json:"used_cost_micros"`
			CostLimitMicros          int64      `json:"cost_limit_micros"`
			Utilization              float64    `json:"utilization"`
			ForecastConfidence       string     `json:"forecast_confidence"`
			ForecastAlertsEnabled    bool       `json:"forecast_alerts_enabled"`
			ForecastLookbackDays     int        `json:"forecast_lookback_days"`
			ForecastMinSamples       int        `json:"forecast_min_samples"`
			ForecastSampleSize       int        `json:"forecast_sample_size"`
			ForecastObservedHours    float64    `json:"forecast_observed_hours"`
			BurnRateMicrosPerHour    float64    `json:"burn_rate_micros_per_hour"`
			ProjectedCostMicros      int64      `json:"projected_cost_micros"`
			ProjectedUtilization     float64    `json:"projected_utilization"`
			RequiredSavingsMicros    int64      `json:"required_savings_micros"`
			PeriodEndsAt             *time.Time `json:"period_ends_at"`
			ProjectedLimitExceededAt *time.Time `json:"projected_limit_exceeded_at"`
		}
		_ = json.Unmarshal(item.Evidence, &evidence)
		output.WriteString("\n## 预算证据\n\n")
		fmt.Fprintf(&output, "- 信号：%s\n- 当前成本 / 上限：%s / %s\n- 当前使用率：%.1f%%\n", markdownCell(evidence.SignalStatus), formatEvidenceCost(evidence.UsedCostMicros), formatEvidenceCost(evidence.CostLimitMicros), evidence.Utilization*100)
		if evidence.ForecastLookbackDays > 0 {
			fmt.Fprintf(&output, "- 提前预警策略：%s，%d 天历史窗口，至少 %d 个样本\n", map[bool]string{true: "启用", false: "关闭"}[evidence.ForecastAlertsEnabled], evidence.ForecastLookbackDays, evidence.ForecastMinSamples)
		}
		if evidence.ProjectedCostMicros > 0 {
			fmt.Fprintf(&output, "- 预计周期成本：%s（%.1f%%）\n- 当前消耗速度：%s / 小时\n- 需要节省：%s\n- 预测可信度：%s（%d 个样本 / %.0f 小时）\n", formatEvidenceCost(evidence.ProjectedCostMicros), evidence.ProjectedUtilization*100, formatEvidenceCost(int64(evidence.BurnRateMicrosPerHour)), formatEvidenceCost(evidence.RequiredSavingsMicros), markdownCell(evidence.ForecastConfidence), evidence.ForecastSampleSize, evidence.ForecastObservedHours)
		}
		if evidence.ProjectedLimitExceededAt != nil {
			fmt.Fprintf(&output, "- 预计超额时间：%s\n", evidence.ProjectedLimitExceededAt.UTC().Format(time.RFC3339))
		}
		if evidence.PeriodEndsAt != nil {
			fmt.Fprintf(&output, "- 当前周期结束：%s\n", evidence.PeriodEndsAt.UTC().Format(time.RFC3339))
		}
	} else {
		output.WriteString("\n## 关联脱敏日志\n\n| 时间 | 服务 | 级别 | 事件 | 消息 | Trace / Run |\n| --- | --- | --- | --- | --- | --- |\n")
	}
	for _, entry := range bundle.Logs {
		correlation := strings.TrimSpace(strings.Join([]string{entry.TraceID, entry.RunID}, " / "))
		fmt.Fprintf(&output, "| %s | %s | %s | %s | %s | %s |\n", entry.OccurredAt.UTC().Format(time.RFC3339), markdownCell(entry.Service), entry.Level, markdownCell(entry.Event), markdownCell(entry.Message), markdownCell(correlation))
	}
	if len(bundle.Logs) == 0 && item.SourceType != "performance_budget" {
		output.WriteString("| — | — | — | 当前保留窗口内没有匹配日志 | — | — |\n")
	}
	output.WriteString("\n> 本报告仅包含服务端脱敏后的运维字段，不包含 Prompt、模型回复、凭据、Cookie 或请求正文。\n")
	return output.String()
}

func formatEvidenceCost(micros int64) string {
	return fmt.Sprintf("$%.4f", float64(micros)/1_000_000)
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func defaultIncidentValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *Server) acknowledgeOperatorIncident(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	s.changeOperatorIncidentStatus(w, r, "acknowledged")
}

func (s *Server) resolveOperatorIncident(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	s.changeOperatorIncidentStatus(w, r, "resolved")
}

func (s *Server) changeOperatorIncidentStatus(w http.ResponseWriter, r *http.Request, status string) {
	var input struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var item incident.Incident
	var err error
	if status == "acknowledged" {
		item, err = s.incidents.Acknowledge(r.Context(), r.PathValue("incident_id"), currentOperator(r).Actor, input.Reason)
	} else {
		item, err = s.incidents.Resolve(r.Context(), r.PathValue("incident_id"), currentOperator(r).Actor, input.Reason)
	}
	if err != nil {
		writeIncidentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"incident": item})
}

func writeIncidentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, incident.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "告警规则、筛选条件或处理原因无效"})
	case errors.Is(err, incident.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "告警规则或事故不存在"})
	case errors.Is(err, incident.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "incident_state_conflict", Message: "事故状态不允许此操作，或相同通知订阅已经存在"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "incident_service_unavailable", Message: "告警与事故服务暂时不可用"})
	}
}
