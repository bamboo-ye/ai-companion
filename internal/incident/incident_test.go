package incident

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEvaluationOpensAcknowledgesAndResolvesIncident(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	rule, err := service.CreateRule(context.Background(), AlertRule{Name: "Agent errors", Service: "agent-worker", Level: "ERROR", WindowMinutes: 5, Threshold: 2, Severity: "critical", Enabled: true}, "admin", "coverage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateSubscription(context.Background(), AlertSubscription{Name: "On-call", Channel: "email", Target: "oncall@example.com", MinimumSeverity: "warning", NotifyOnOpen: true, NotifyOnResolved: true, Enabled: true}, "admin", "coverage"); err != nil {
		t.Fatal(err)
	}
	store.SetCount(rule.ID, 3)
	now := time.Now().UTC()
	report, err := service.Evaluate(context.Background(), now)
	if err != nil || report.Opened != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	items, _ := service.Incidents(context.Background(), IncidentFilter{Limit: 10})
	if len(items) != 1 || items[0].Status != "open" {
		t.Fatalf("items=%+v", items)
	}
	notifications, err := service.Notifications(context.Background(), items[0].ID)
	if err != nil || len(notifications) != 1 || notifications[0].Transition != "opened" || notifications[0].Target != "o***@example.com" {
		t.Fatalf("notifications=%+v err=%v", notifications, err)
	}
	acknowledged, err := service.Acknowledge(context.Background(), items[0].ID, "support", "investigating")
	if err != nil || acknowledged.Status != "acknowledged" {
		t.Fatalf("incident=%+v err=%v", acknowledged, err)
	}
	store.SetCount(rule.ID, 0)
	report, err = service.Evaluate(context.Background(), now.Add(time.Minute))
	if err != nil || report.Resolved != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	evidence, err := service.Evidence(context.Background(), items[0].ID)
	if err != nil || evidence.Incident.Status != "resolved" || len(evidence.Notifications) != 2 || len(evidence.Activity) != 3 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}

func TestSubscriptionValidationAndDuplicate(t *testing.T) {
	service := NewService(NewMemoryStore())
	if _, err := service.CreateSubscription(context.Background(), AlertSubscription{Name: "Bad", Channel: "email", Target: "not-an-email", MinimumSeverity: "warning", NotifyOnOpen: true}, "admin", "coverage"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid email err=%v", err)
	}
	input := AlertSubscription{Name: "On-call", Channel: "email", Target: "oncall@example.com", MinimumSeverity: "critical", NotifyOnOpen: true, Enabled: true}
	if _, err := service.CreateSubscription(context.Background(), input, "admin", "coverage"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSubscription(context.Background(), input, "admin", "coverage"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate err=%v", err)
	}
	if _, err := service.CreateSubscription(context.Background(), AlertSubscription{Name: "Bad console", Channel: "console", Target: "invalid target", MinimumSeverity: "warning", NotifyOnOpen: true}, "admin", "coverage"); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid console target err=%v", err)
	}
}

func TestConsoleSubscriptionCreatesImmediateDurableInboxNotification(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	rule, err := service.CreateRule(context.Background(), AlertRule{Name: "API errors", Level: "ERROR", WindowMinutes: 5, Threshold: 1, Severity: "warning", Enabled: true}, "admin", "coverage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateSubscription(context.Background(), AlertSubscription{Name: "Operations inbox", Channel: "console", Target: "operations", MinimumSeverity: "warning", NotifyOnOpen: true, Enabled: true}, "admin", "coverage"); err != nil {
		t.Fatal(err)
	}
	store.SetCount(rule.ID, 1)
	if _, err = service.Evaluate(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	items, _ := service.Incidents(context.Background(), IncidentFilter{Limit: 10})
	notifications, err := service.Notifications(context.Background(), items[0].ID)
	if err != nil || len(notifications) != 1 || notifications[0].Channel != "console" || notifications[0].Target != "operations" || notifications[0].Status != "sent" || notifications[0].SentAt == nil {
		t.Fatalf("notifications=%+v err=%v", notifications, err)
	}
}

func TestDisablingRuleResolvesAndNotifiesActiveIncident(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	rule, err := service.CreateRule(ctx, AlertRule{Name: "Warnings", Level: "WARN", WindowMinutes: 5, Threshold: 1, Severity: "warning", Enabled: true}, "admin", "coverage")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateSubscription(ctx, AlertSubscription{Name: "On-call", RuleID: rule.ID, Channel: "email", Target: "oncall@example.com", MinimumSeverity: "warning", NotifyOnOpen: true, NotifyOnResolved: true, Enabled: true}, "admin", "coverage"); err != nil {
		t.Fatal(err)
	}
	store.SetCount(rule.ID, 1)
	if report, evaluateErr := service.Evaluate(ctx, time.Now().UTC()); evaluateErr != nil || report.Opened != 1 {
		t.Fatalf("report=%+v err=%v", report, evaluateErr)
	}
	items, _ := service.Incidents(ctx, IncidentFilter{Limit: 10})
	if _, err = service.SetRuleEnabled(ctx, rule.ID, false, "admin", "maintenance"); err != nil {
		t.Fatal(err)
	}
	evidence, err := service.Evidence(ctx, items[0].ID)
	if err != nil || evidence.Incident.Status != "resolved" || len(evidence.Notifications) != 2 {
		t.Fatalf("evidence=%+v err=%v", evidence, err)
	}
}
