package billing

import (
	"context"
	"testing"
	"time"
)

func TestSummaryCombinesUnifiedUsageAndAppendOnlyAdjustments(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, []Plan{{
		Code: "free", DisplayName: "Free", Status: "active",
		Limits: Limits{Documents: 10, SkillRunsPerMonth: 20, Workspaces: 3, AgentRunsPerMonth: 4, ModelCostMicrosMonthly: 1_000_000},
	}})
	now := time.Date(2026, time.September, 9, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	store.SetUsage("user-1", 2, 1, []time.Time{now.Add(-time.Hour)})
	store.SetExtendedUsage("user-1", []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour)}, map[time.Time]int{now.Add(-time.Hour): 750_000})

	created, err := service.AdjustUsage(context.Background(), CreateUsageAdjustmentInput{
		UserID: "user-1", Resource: ResourceModelCost, Delta: -250_000,
		Reason: "provider credit", Actor: "billing-admin", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.PeriodStart == nil || created.PeriodEnd == nil {
		t.Fatal("monthly adjustment must pin the subscription period")
	}
	summary, err := service.Summary(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	assertUsage := func(resource string, actual, adjustment, used int) {
		t.Helper()
		for _, item := range summary.Usage {
			if item.Resource == resource {
				if item.Actual != actual || item.Adjustment != adjustment || item.Used != used {
					t.Fatalf("%s usage=%+v", resource, item)
				}
				return
			}
		}
		t.Fatalf("missing usage %s", resource)
	}
	assertUsage(ResourceAgentRuns, 2, 0, 2)
	assertUsage(ResourceModelCost, 750_000, -250_000, 500_000)
	items, err := service.UsageAdjustments(context.Background(), "user-1", 10)
	if err != nil || len(items) != 1 || items[0].ID == "" {
		t.Fatalf("adjustments=%+v err=%v", items, err)
	}
}

func TestCheckEnforcesOptionalAgentAndCostLimits(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, []Plan{{Code: "free", Status: "active", Limits: Limits{
		Documents: 10, SkillRunsPerMonth: 20, Workspaces: 3, AgentRunsPerMonth: 1, ModelCostMicrosMonthly: 100,
	}}})
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	store.SetExtendedUsage("user-1", []time.Time{now}, map[time.Time]int{now: 100})
	if err := service.Check(context.Background(), "user-1", ResourceAgentRuns); err == nil {
		t.Fatal("expected Agent Run quota error")
	}
	if err := service.Check(context.Background(), "user-1", ResourceModelCost); err == nil {
		t.Fatal("expected model cost quota error")
	}
}
