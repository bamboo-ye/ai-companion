package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/incident"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestOperatorAlertRuleAndIncidentWorkflow(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "incident-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := incident.NewMemoryStore()
	server.SetIncidentStore(store)
	created := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/alert-rules", "ops-token", "admin", map[string]any{
		"name": "API errors", "service": "api", "level": "ERROR", "window_minutes": 5,
		"threshold": 2, "severity": "critical", "enabled": true, "reason": "test rule",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	rules, _ := store.ListAlertRules(t.Context())
	subscribed := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/alert-subscriptions", "ops-token", "admin", map[string]any{
		"name": "On-call", "channel": "email", "target": "oncall@example.com", "minimum_severity": "warning",
		"notify_on_open": true, "notify_on_resolved": true, "enabled": true, "reason": "test subscription",
	})
	if subscribed.Code != http.StatusCreated {
		t.Fatalf("subscribed=%d %s", subscribed.Code, subscribed.Body.String())
	}
	store.SetCount(rules[0].ID, 3)
	evaluated := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/alerts/evaluate", "ops-token", "support", nil)
	if evaluated.Code != http.StatusOK || !strings.Contains(evaluated.Body.String(), `"opened":1`) {
		t.Fatalf("evaluated=%d %s", evaluated.Code, evaluated.Body.String())
	}
	listed := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/incidents?status=open", "ops-token", "viewer", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"status":"open"`) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	incidents, _ := server.incidents.Incidents(t.Context(), incident.IncidentFilter{Limit: 10})
	notifications := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/incidents/"+incidents[0].ID+"/notifications", "ops-token", "viewer", nil)
	if notifications.Code != http.StatusOK || !strings.Contains(notifications.Body.String(), `"target":"o***@example.com"`) {
		t.Fatalf("notifications=%d %s", notifications.Code, notifications.Body.String())
	}
	evidence := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/incidents/"+incidents[0].ID+"/evidence?format=markdown", "ops-token", "viewer", nil)
	if evidence.Code != http.StatusOK || !strings.Contains(evidence.Header().Get("Content-Disposition"), "evidence.md") || !strings.Contains(evidence.Body.String(), "事故证据报告") {
		t.Fatalf("evidence=%d headers=%v body=%s", evidence.Code, evidence.Header(), evidence.Body.String())
	}
}

func TestRenderForecastBudgetEvidenceMarkdown(t *testing.T) {
	now := time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC)
	evidence, err := json.Marshal(map[string]any{
		"signal_status": "projected_exceeded", "used_cost_micros": 200_000, "cost_limit_micros": 1_000_000,
		"utilization": .2, "projected_cost_micros": 1_200_000, "projected_utilization": 1.2,
		"burn_rate_micros_per_hour": 50_000, "required_savings_micros": 200_000,
		"forecast_confidence": "high", "forecast_sample_size": 120, "forecast_observed_hours": 168,
		"projected_limit_exceeded_at": now.Add(8 * time.Hour), "period_ends_at": now.Add(16 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := renderIncidentEvidenceMarkdown(incident.EvidenceBundle{Incident: incident.Incident{
		ID: "incident-forecast", RuleName: "工作每日预算", SourceType: "performance_budget", Status: "open", Severity: "warning",
		Title: "成本预算预计将超限：工作每日预算", Service: "work", Level: "WARN", EventPrefix: "performance.budget.projected_exceeded",
		ObservedValue: 120, Threshold: 100, WindowMinutes: 1440, OpenedAt: now, Evidence: evidence,
	}, GeneratedAt: now})
	for _, expected := range []string{"预计周期成本：$1.2000（120.0%）", "需要节省：$0.2000", "预计超额时间：2026-09-08T16:00:00Z", "120 个样本 / 168 小时"} {
		if !strings.Contains(result, expected) {
			t.Fatalf("missing %q in evidence:\n%s", expected, result)
		}
	}
}
