package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseTaxiCandidateUsesAbsoluteLocalDate(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	reference := time.Date(2026, 7, 3, 10, 30, 0, 0, location)
	item, err := Parse("昨晚打车 36 元", "Asia/Shanghai", reference)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "pending" || item.Direction != "expense" || item.Currency != "CNY" || item.AmountMinor != 3600 || item.Category != "transport" {
		t.Fatalf("candidate = %#v", item)
	}
	if item.OccurredAt == nil || item.OccurredAt.In(location).Format("2006-01-02 15:04") != "2026-07-02 20:00" {
		t.Fatalf("occurred_at = %v", item.OccurredAt)
	}
	if len(item.NeedsClarification) != 0 || item.Confidence < .99 {
		t.Fatalf("candidate confidence/clarification = %#v", item)
	}
	payload, _ := json.Marshal(item)
	if !strings.Contains(string(payload), `"needs_clarification":[]`) {
		t.Fatalf("successful candidate must expose an empty clarification array: %s", payload)
	}
}

func TestAmbiguousCandidateCannotBeConfirmed(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store)
	service.now = func() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }
	item, err := service.ParseCandidate(context.Background(), "u1", "", "打车 36 元", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "needs_clarification" || len(item.NeedsClarification) != 1 || item.NeedsClarification[0] != "occurred_at" {
		t.Fatalf("candidate = %#v", item)
	}
	if _, _, err = service.Confirm(context.Background(), "u1", item.ID, "confirm-1", ""); !errors.Is(err, ErrConfirmation) {
		t.Fatalf("confirm error = %v", err)
	}
}

func TestConfirmIsIdempotentAndSupportsCRUDAndSummary(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store)
	location, _ := time.LoadLocation("Asia/Shanghai")
	service.now = func() time.Time { return time.Date(2026, 7, 3, 10, 0, 0, 0, location) }
	candidate, err := service.ParseCandidate(ctx, "u1", "m1", "昨晚打车 36 元", "Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := service.Confirm(ctx, "u1", candidate.ID, "confirm-taxi", "客户拜访")
	if err != nil || !created {
		t.Fatalf("first confirm = %#v %v %v", first, created, err)
	}
	second, created, err := service.Confirm(ctx, "u1", candidate.ID, "confirm-taxi", "客户拜访")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second confirm = %#v %v %v", second, created, err)
	}
	third, created, err := service.Confirm(ctx, "u1", candidate.ID, "another-key", "客户拜访")
	if err != nil || created || third.ID != first.ID {
		t.Fatalf("candidate duplicate confirm = %#v %v %v", third, created, err)
	}
	amount := int64(4000)
	updated, err := service.Update(ctx, "u1", first.ID, UpdateInput{AmountMinor: &amount})
	if err != nil || updated.AmountMinor != 4000 {
		t.Fatalf("update = %#v %v", updated, err)
	}
	summary, err := service.Summary(ctx, "u1", "2026-07", "Asia/Shanghai", "CNY")
	if err != nil || summary.ExpenseMinor != 4000 || summary.EntryCount != 1 || len(summary.ExpenseByCategory) != 1 {
		t.Fatalf("summary = %#v %v", summary, err)
	}
	if err = service.Delete(ctx, "u1", first.ID); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List(ctx, "u1", EntryFilter{})
	if len(items) != 0 {
		t.Fatalf("deleted entry remained visible: %#v", items)
	}
}

func TestIdempotencyKeyCannotBeReusedAcrossCandidates(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore())
	service.now = func() time.Time { return time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC) }
	first, _ := service.ParseCandidate(ctx, "u1", "", "昨天打车 10 元", "Asia/Shanghai")
	second, _ := service.ParseCandidate(ctx, "u1", "", "昨天吃饭 20 元", "Asia/Shanghai")
	if _, _, err := service.Confirm(ctx, "u1", first.ID, "same-key", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Confirm(ctx, "u1", second.ID, "same-key", ""); !errors.Is(err, ErrIdempotencyReuse) {
		t.Fatalf("reuse error = %v", err)
	}
}
