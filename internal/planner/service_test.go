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

func TestMissingDateAndDSTTimesRequireClarification(t *testing.T) {
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	fuzzy, _ := ParseReminder("提醒我交报告", "Asia/Shanghai", time.Date(2026, 7, 3, 16, 0, 0, 0, shanghai))
	if fuzzy.Status != "needs_clarification" || fuzzy.DueAt != nil {
		t.Fatalf("fuzzy reminder = %#v", fuzzy)
	}
	newYork, _ := time.LoadLocation("America/New_York")
	dst, _ := ParseReminder("2026-03-08 2点30分提醒我检查任务", "America/New_York", time.Date(2026, 3, 1, 12, 0, 0, 0, newYork))
	if dst.Status != "needs_clarification" || len(dst.NeedsClarification) == 0 || dst.NeedsClarification[len(dst.NeedsClarification)-1] != "dst_conflict" {
		t.Fatalf("DST reminder = %#v", dst)
	}
}

func TestDateOnlyReminderCanBeConfirmedAndAppearsUnscheduledInTodayPlan(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	location, _ := time.LoadLocation("Asia/Shanghai")
	service.now = func() time.Time { return time.Date(2026, 7, 3, 16, 0, 0, 0, location) }
	item, err := service.ParseReminder(ctx, "u1", "date-only-message", "明天提醒我交报告", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "pending_confirmation" || item.TimePrecision != "date" || item.LocalDue != "2026-07-04" || item.DueAt == nil || len(item.NeedsClarification) != 0 {
		t.Fatalf("date-only reminder = %#v", item)
	}
	if local := item.DueAt.In(location); local.Format("2006-01-02 15:04") != "2026-07-04 00:00" {
		t.Fatalf("date-only due = %s", local)
	}
	confirmed, created, err := service.ConfirmReminder(ctx, "u1", item.ID, "confirm-date-only")
	if err != nil || !created || confirmed.Status != "active" {
		t.Fatalf("confirm date-only = %#v created=%v err=%v", confirmed, created, err)
	}
	today, err := service.Today(ctx, "u1", "2026-07-04", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if len(today.Items) != 1 || today.Items[0].ID != item.ID || today.Items[0].StartsAt != nil || today.Items[0].Source != "reminder" {
		t.Fatalf("date-only reminder in today plan = %#v", today.Items)
	}
}

func TestShortChineseReminderDatesChooseNearestFutureOccurrence(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	tests := []struct {
		name      string
		reference time.Time
		text      string
		wantDate  string
		wantTitle string
	}{
		{
			name: "later day in current month", reference: time.Date(2026, 7, 17, 9, 0, 0, 0, location),
			text: "21号提醒我报名领证", wantDate: "2026-07-21", wantTitle: "报名领证",
		},
		{
			name: "day already passed rolls to next month", reference: time.Date(2026, 7, 22, 9, 0, 0, 0, location),
			text: "21号提醒我报名领证", wantDate: "2026-08-21", wantTitle: "报名领证",
		},
		{
			name: "month and day roll to next year", reference: time.Date(2026, 12, 30, 9, 0, 0, 0, location),
			text: "1月2号提醒我报名领证", wantDate: "2027-01-02", wantTitle: "报名领证",
		},
		{
			name: "invalid day in current month finds next valid month", reference: time.Date(2026, 4, 30, 9, 0, 0, 0, location),
			text: "31号提醒我报名领证", wantDate: "2026-05-31", wantTitle: "报名领证",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item, err := ParseReminder(test.text, "Asia/Shanghai", test.reference)
			if err != nil {
				t.Fatal(err)
			}
			if item.Status != "pending_confirmation" || item.TimePrecision != "date" || item.LocalDue != test.wantDate || item.Title != test.wantTitle || len(item.NeedsClarification) != 0 {
				t.Fatalf("reminder = %#v", item)
			}
		})
	}
}

func TestRelativeWeekDatesUseCalendarBoundaries(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	reference := time.Date(2026, 8, 21, 10, 0, 0, 0, location) // Friday
	tests := []struct {
		text     string
		wantDate string
	}{
		{text: "这周结束前提醒我订机票", wantDate: "2026-08-23"},
		{text: "本周末提醒我提交材料", wantDate: "2026-08-23"},
		{text: "下周三提醒我参加评审", wantDate: "2026-08-26"},
		{text: "周四提醒我复盘项目", wantDate: "2026-08-27"},
		{text: "每周四提醒我整理周报", wantDate: "2026-08-27"},
	}
	for _, test := range tests {
		t.Run(test.text, func(t *testing.T) {
			item, err := ParseReminder(test.text, "Asia/Shanghai", reference)
			if err != nil || item.LocalDue != test.wantDate || item.DueAt == nil || len(item.NeedsClarification) != 0 {
				t.Fatalf("reminder = %#v err=%v", item, err)
			}
			if strings.HasPrefix(test.text, "每周") && (item.Recurrence != "weekly" || item.Title != "整理周报") {
				t.Fatalf("weekly reminder = %#v", item)
			}
		})
	}
}

func TestReminderSlotsMergeIndependentFollowUpFields(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	reference := time.Date(2026, 8, 21, 10, 0, 0, 0, location)
	item, err := ParseReminderWithSlots("这周结束前", "订机票", "这周结束前", "Asia/Shanghai", reference)
	if err != nil || item.Title != "订机票" || item.LocalDue != "2026-08-23" || item.Status != "pending_confirmation" || len(item.NeedsClarification) != 0 {
		t.Fatalf("merged reminder = %#v err=%v", item, err)
	}

	dateFromCurrentMessage, err := ParseReminderWithSlots("下周五", "提交报销材料", "", "Asia/Shanghai", reference)
	if err != nil || dateFromCurrentMessage.Title != "提交报销材料" || dateFromCurrentMessage.LocalDue != "2026-08-28" {
		t.Fatalf("independent title slot = %#v err=%v", dateFromCurrentMessage, err)
	}
}

func TestParseSchedulePreservesFallbackDateAndSupportsColonClock(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	reference := time.Date(2026, 7, 17, 10, 0, 0, 0, location)
	fallback := time.Date(2026, 7, 21, 0, 0, 0, 0, location)
	schedule, err := ParseSchedule("把报名领证提醒改到15:30", "Asia/Shanghai", reference, &fallback)
	if err != nil || schedule.LocalDue != "2026-07-21 15:30" || schedule.TimePrecision != "minute" {
		t.Fatalf("schedule = %#v err=%v", schedule, err)
	}
	dateOnly, err := ParseSchedule("改到明天", "Asia/Shanghai", reference, &fallback)
	if err != nil || dateOnly.LocalDue != "2026-07-18" || dateOnly.TimePrecision != "date" {
		t.Fatalf("date-only schedule = %#v err=%v", dateOnly, err)
	}
	if !LooksLikeScheduleChange("把交报告提醒改到明天下午4点") || LooksLikeScheduleChange("明天下午4点提醒我交报告") {
		t.Fatal("schedule-change intent classification is incorrect")
	}
}

func TestUnqualifiedClockUsesOnlyFutureInterpretations(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	tests := []struct {
		name          string
		reference     time.Time
		text          string
		wantLocalDue  string
		wantClarifier string
	}{
		{
			name: "afternoon is the only future interpretation", reference: time.Date(2026, 7, 17, 14, 0, 0, 0, location),
			text: "今天3点提醒我开会", wantLocalDue: "2026-07-17 15:00",
		},
		{
			name: "both interpretations remain future", reference: time.Date(2026, 7, 17, 9, 0, 0, 0, location),
			text: "今天10点提醒我开会", wantClarifier: "day_period",
		},
		{
			name: "future date remains ambiguous", reference: time.Date(2026, 7, 17, 14, 0, 0, 0, location),
			text: "明天3点提醒我开会", wantClarifier: "day_period",
		},
		{
			name: "both interpretations have passed", reference: time.Date(2026, 7, 17, 21, 0, 0, 0, location),
			text: "今天8点提醒我开会", wantClarifier: "future_time",
		},
		{
			name: "explicit period can still be past", reference: time.Date(2026, 7, 17, 16, 0, 0, 0, location),
			text: "今天下午3点提醒我开会", wantClarifier: "future_time",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item, err := ParseReminder(test.text, "Asia/Shanghai", test.reference)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantLocalDue != "" {
				if item.LocalDue != test.wantLocalDue || item.DueAt == nil || len(item.NeedsClarification) != 0 {
					t.Fatalf("reminder = %#v", item)
				}
				return
			}
			if item.DueAt != nil || !containsClarification(item.NeedsClarification, test.wantClarifier) {
				t.Fatalf("reminder = %#v", item)
			}
		})
	}
}

func TestParseScheduleRejectsAmbiguousAndPastTimes(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	reference := time.Date(2026, 7, 17, 9, 0, 0, 0, location)
	if _, err := ParseSchedule("改到今天10点", "Asia/Shanghai", reference, nil); !errors.Is(err, ErrDayPeriodRequired) {
		t.Fatalf("ambiguous schedule error = %v", err)
	}
	if _, err := ParseSchedule("改到今天上午8点", "Asia/Shanghai", reference, nil); !errors.Is(err, ErrPastSchedule) {
		t.Fatalf("past schedule error = %v", err)
	}
	if _, err := ParseSchedule("把下午评审会改到明天3点", "Asia/Shanghai", reference, nil); !errors.Is(err, ErrDayPeriodRequired) {
		t.Fatalf("day period in title must not qualify the new clock: %v", err)
	}
	canonical, err := ParseSchedule("2026-07-18 09:00", "Asia/Shanghai", reference, nil)
	if err != nil || canonical.LocalDue != "2026-07-18 09:00" {
		t.Fatalf("canonical 24-hour schedule = %#v err=%v", canonical, err)
	}
}

func containsClarification(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
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
	item, _ := service.ParseReminder(context.Background(), "u1", "", "提醒我交报告", "Asia/Shanghai")
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

func TestTodayPlanCombinesPlanItemsAndTodayReminders(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	location, _ := time.LoadLocation("Asia/Shanghai")
	service.now = func() time.Time { return time.Date(2026, 7, 16, 8, 0, 0, 0, location) }
	added, err := service.AddTodayItem(ctx, "u1", "2026-07-16", "Asia/Shanghai", "整理项目周报全部内容", "chat")
	if err != nil {
		t.Fatal(err)
	}
	reminder, err := service.ParseReminder(ctx, "u1", "m1", "今天下午3点提醒我参加评审会", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.ConfirmReminder(ctx, "u1", reminder.ID, "confirm-review"); err != nil {
		t.Fatal(err)
	}
	today, err := service.Today(ctx, "u1", "2026-07-16", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if len(today.Items) != 2 || today.Items[0].ID == "" {
		t.Fatalf("today plan = %#v", today)
	}
	foundPlan, foundReminder := false, false
	for _, item := range today.Items {
		foundPlan = foundPlan || item.ID == added.ID && item.Title == "整理项目周报全部内容" && item.Source == "chat"
		foundReminder = foundReminder || item.ID == reminder.ID && item.Source == "reminder" && strings.Contains(item.Title, "评审会")
	}
	if !foundPlan || !foundReminder {
		t.Fatalf("combined items = %#v", today.Items)
	}
}

func TestAddTodayItemDoesNotDeduplicateGenericWebSource(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore())
	first, err := service.AddTodayItem(ctx, "u1", "2026-07-16", "Asia/Shanghai", "整理合同", "web")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AddTodayItem(ctx, "u1", "2026-07-16", "Asia/Shanghai", "提交周报", "web")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("generic web submissions were deduplicated: first=%#v second=%#v", first, second)
	}
	today, err := service.Today(ctx, "u1", "2026-07-16", "Asia/Shanghai")
	if err != nil || len(today.Items) != 2 {
		t.Fatalf("today items = %#v, %v", today.Items, err)
	}
}

func TestTodayPlanCarriesOverActiveOverdueRemindersAndCompletesItems(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 17, 9, 0, 0, 0, location)
	service.now = func() time.Time { return now }

	planItem, err := service.AddTodayItem(ctx, "u1", "2026-07-17", "Asia/Shanghai", "整理合同", "web")
	if err != nil {
		t.Fatal(err)
	}
	overdueDue := time.Date(2026, 7, 15, 0, 0, 0, 0, location).UTC()
	completedPastDue := time.Date(2026, 7, 14, 0, 0, 0, 0, location).UTC()
	for _, reminder := range []Reminder{
		{ID: "overdue-active", UserID: "u1", Title: "补办材料", DueAt: &overdueDue, LocalDue: "2026-07-15", Timezone: "Asia/Shanghai", TimePrecision: "date", Status: "active", CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "past-completed", UserID: "u1", Title: "已经完成的旧提醒", DueAt: &completedPastDue, LocalDue: "2026-07-14", Timezone: "Asia/Shanghai", TimePrecision: "date", Status: "completed", CreatedAt: now.Add(-72 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
	} {
		if err = store.CreateReminder(ctx, reminder); err != nil {
			t.Fatal(err)
		}
	}

	today, err := service.Today(ctx, "u1", "2026-07-17", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if len(today.Items) != 2 {
		t.Fatalf("today items = %#v", today.Items)
	}
	if today.Items[0].ID != "overdue-active" || !today.Items[0].Overdue || today.Items[0].LocalDate != "2026-07-15" {
		t.Fatalf("overdue item = %#v", today.Items[0])
	}
	if today.Items[1].ID != planItem.ID || today.Items[1].LocalDate != "2026-07-17" {
		t.Fatalf("plan item = %#v", today.Items[1])
	}

	if err = service.CompletePlanItem(ctx, "u1", planItem.ID); err != nil {
		t.Fatal(err)
	}
	if err = service.CompletePlanItem(ctx, "u1", planItem.ID); err != nil {
		t.Fatalf("duplicate plan completion must be idempotent: %v", err)
	}
	afterPlanCompletion, _ := service.Today(ctx, "u1", "2026-07-17", "Asia/Shanghai")
	if afterPlanCompletion.Items[1].Status != "completed" {
		t.Fatalf("completed plan item = %#v", afterPlanCompletion.Items[1])
	}

	if err = service.CompleteReminder(ctx, "u1", "overdue-active"); err != nil {
		t.Fatal(err)
	}
	afterReminderCompletion, _ := service.Today(ctx, "u1", "2026-07-17", "Asia/Shanghai")
	if len(afterReminderCompletion.Items) != 1 || afterReminderCompletion.Items[0].ID != planItem.ID {
		t.Fatalf("completed overdue reminder should stop carrying over: %#v", afterReminderCompletion.Items)
	}
}

func TestReminderAndPlanSchedulesUpdateOnlyWithCurrentConfirmation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, location)
	service.now = func() time.Time { return now }

	planItem, err := service.AddTodayItem(ctx, "u1", "2026-07-17", "Asia/Shanghai", "整理合同", "web")
	if err != nil {
		t.Fatal(err)
	}
	planExpected := planItem.UpdatedAt
	startsAt := time.Date(2026, 7, 17, 15, 30, 0, 0, location)
	now = now.Add(time.Minute)
	scheduledPlan, err := service.SchedulePlanItem(ctx, "u1", planItem.ID, startsAt, &planExpected)
	if err != nil || scheduledPlan.StartsAt == nil || scheduledPlan.StartsAt.In(location).Format("15:04") != "15:30" {
		t.Fatalf("scheduled plan = %#v err=%v", scheduledPlan, err)
	}
	if _, err = service.SchedulePlanItem(ctx, "u1", planItem.ID, startsAt, &planExpected); err != nil {
		t.Fatalf("same plan update must be idempotent: %v", err)
	}
	otherTime := time.Date(2026, 7, 17, 16, 0, 0, 0, location)
	if _, err = service.SchedulePlanItem(ctx, "u1", planItem.ID, otherTime, &planExpected); !errors.Is(err, ErrStaleUpdate) {
		t.Fatalf("stale plan update error = %v", err)
	}

	due := time.Date(2026, 7, 18, 0, 0, 0, 0, location).UTC()
	reminder := Reminder{ID: "reminder-reschedule", UserID: "u1", Title: "交报告", DueAt: &due, LocalDue: "2026-07-18", Timezone: "Asia/Shanghai", TimePrecision: "date", Status: "active", SystemSyncStatus: "not_requested", CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if err = store.CreateReminder(ctx, reminder); err != nil {
		t.Fatal(err)
	}
	reminderExpected := reminder.UpdatedAt
	now = now.Add(time.Minute)
	rescheduled, err := service.RescheduleReminder(ctx, "u1", reminder.ID, "2026-07-18 16:00", "Asia/Shanghai", &reminderExpected)
	if err != nil || rescheduled.LocalDue != "2026-07-18 16:00" || rescheduled.TimePrecision != "minute" {
		t.Fatalf("rescheduled reminder = %#v err=%v", rescheduled, err)
	}
	if _, err = service.RescheduleReminder(ctx, "u1", reminder.ID, "2026-07-18 16:00", "Asia/Shanghai", &reminderExpected); err != nil {
		t.Fatalf("same reminder update must be idempotent: %v", err)
	}
	if _, err = service.RescheduleReminder(ctx, "u1", reminder.ID, "2026-07-18 17:00", "Asia/Shanghai", &reminderExpected); !errors.Is(err, ErrStaleUpdate) {
		t.Fatalf("stale reminder update error = %v", err)
	}
}

func TestSchedulePlanItemRejectsPastTimeButKeepsIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	location, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 7, 17, 14, 0, 0, 0, location)
	service := NewServiceWithClock(NewMemoryStore(), func() time.Time { return now })
	item, err := service.AddTodayItem(ctx, "u1", "2026-07-17", "Asia/Shanghai", "整理合同", "web")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SchedulePlanItem(ctx, "u1", item.ID, time.Date(2026, 7, 17, 13, 0, 0, 0, location), &item.UpdatedAt); !errors.Is(err, ErrPastSchedule) {
		t.Fatalf("past plan schedule error = %v", err)
	}
	future := time.Date(2026, 7, 17, 15, 0, 0, 0, location)
	scheduled, err := service.SchedulePlanItem(ctx, "u1", item.ID, future, &item.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 7, 17, 16, 0, 0, 0, location)
	if _, err = service.SchedulePlanItem(ctx, "u1", item.ID, future, &scheduled.UpdatedAt); err != nil {
		t.Fatalf("idempotent replay after start time = %v", err)
	}
}
