package planner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseReminderReturnsAbsoluteLocalTime(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	reference := time.Date(2026, 7, 3, 16, 0, 0, 0, location)
	item, err := ParseReminder("明早九点提醒我交报告", "Asia/Shanghai", reference)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "pending_confirmation" || item.Title != "交报告" || item.LocalDue != "2026-07-04 09:00" || item.DueAt == nil {
		t.Fatalf("reminder = %#v", item)
	}
	if item.DueAt.In(location).Format("2006-01-02 15:04") != item.LocalDue {
		t.Fatalf("absolute/local mismatch = %v %s", item.DueAt, item.LocalDue)
	}
	payload, _ := json.Marshal(item)
	if !strings.Contains(string(payload), `"needs_clarification":[]`) {
		t.Fatalf("successful reminder must expose an empty clarification array: %s", payload)
	}
}

func TestFuzzyAndDSTTimesRequireClarification(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	fuzzy, _ := ParseReminder("明早提醒我交报告", "Asia/Shanghai", time.Date(2026, 7, 3, 16, 0, 0, 0, shanghai))
	if fuzzy.Status != "needs_clarification" || fuzzy.DueAt != nil {
		t.Fatalf("fuzzy reminder = %#v", fuzzy)
	}
	newYork, _ := time.LoadLocation("America/New_York")
	dst, _ := ParseReminder("2026-03-08 2点30分提醒我检查任务", "America/New_York", time.Date(2026, 3, 1, 12, 0, 0, 0, newYork))
	if dst.Status != "needs_clarification" || len(dst.NeedsClarification) == 0 || dst.NeedsClarification[len(dst.NeedsClarification)-1] != "dst_conflict" {
		t.Fatalf("DST reminder = %#v", dst)
	}
}

func TestReminderConfirmationAndPermissionDenialRemainVisible(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	location, _ := time.LoadLocation("Asia/Shanghai")
	service.now = func() time.Time { return time.Date(2026, 7, 3, 16, 0, 0, 0, location) }
	item, err := service.ParseReminder(ctx, "u1", "m1", "明早九点提醒我交报告", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := service.ConfirmReminder(ctx, "u1", item.ID, "confirm-report")
	if err != nil || !created || first.Status != "active" {
		t.Fatalf("confirm = %#v %v %v", first, created, err)
	}
	second, created, err := service.ConfirmReminder(ctx, "u1", item.ID, "confirm-report")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("duplicate confirm = %#v %v %v", second, created, err)
	}
	denied, err := service.ReportSystemSync(ctx, "u1", item.ID, SyncResult{Status: "permission_denied", Provider: "ios_eventkit", ErrorCode: "permission_denied"})
	if err != nil || denied.Status != "active" || denied.SystemSyncStatus != "permission_denied" {
		t.Fatalf("denied sync = %#v %v", denied, err)
	}
	items, _ := service.ListReminders(ctx, "u1", nil, nil, 10)
	if len(items) != 1 || items[0].Status != "active" {
		t.Fatalf("application reminder lost after denial: %#v", items)
	}
	if err = service.CompleteReminder(ctx, "u1", item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAmbiguousReminderCannotBeConfirmed(t *testing.T) {
	service := NewService(NewMemoryStore())
	service.now = func() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }
	item, _ := service.ParseReminder(context.Background(), "u1", "", "明早提醒我交报告", "Asia/Shanghai")
	if _, _, err := service.ConfirmReminder(context.Background(), "u1", item.ID, "confirm"); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("confirm error = %v", err)
	}
}

func TestCreateDailyPlanValidatesItems(t *testing.T) {
	service := NewService(NewMemoryStore())
	plan, err := service.CreatePlan(context.Background(), "u1", PlanInput{Title: "明日计划", LocalDate: "2026-07-04", Timezone: "Asia/Shanghai", Items: []PlanItemInput{{Title: "提交报告", Priority: "high", EstimatedMinutes: 45, Source: "memo"}}})
	if err != nil || len(plan.Items) != 1 || plan.Items[0].Priority != "high" {
		t.Fatalf("plan = %#v %v", plan, err)
	}
}
