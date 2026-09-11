package postgresstore

import (
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/incident"
)

func TestForecastBudgetIncidentEmailContent(t *testing.T) {
	item := incident.Incident{
		ID: "incident-forecast", RuleName: "工作每日预算", SourceType: "performance_budget",
		Severity: "warning", Service: "work", EventPrefix: "performance.budget.projected_exceeded",
		ObservedValue: 120, Summary: "预计周期末使用 $1.2000 / $1.0000（120%）",
	}
	template, subject, body := incidentEmailContent(item, "opened")
	if template != "performance.budget.forecast.warning.v1" || !strings.Contains(subject, "预计将超限") || !strings.Contains(body, "不会自动修改生产模型") {
		t.Fatalf("opened email template=%q subject=%q body=%q", template, subject, body)
	}
	item.Status, item.Resolution = "resolved", "budget forecast returned below projected limit"
	template, subject, body = incidentEmailContent(item, "resolved")
	if template != "performance.budget.forecast.resolved.v1" || !strings.Contains(subject, "风险已解除") || !strings.Contains(body, "预测已回到安全范围") {
		t.Fatalf("resolved email template=%q subject=%q body=%q", template, subject, body)
	}
}
