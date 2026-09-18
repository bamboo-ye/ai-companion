package memory

import (
	"context"
	"testing"
	"time"
)

func TestExplicitMemoryLifecycle(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore())
	if err := service.Observe(ctx, "u1", "c1", "m1", "你好"); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List(ctx, "u1")
	if len(items) != 0 {
		t.Fatalf("greeting created %d memories", len(items))
	}
	if err := service.Observe(ctx, "u1", "c1", "m2", "记住我不吃香菜。"); err != nil {
		t.Fatal(err)
	}
	if err := service.Observe(ctx, "u1", "c1", "m3", "请记住：我不吃香菜"); err != nil {
		t.Fatal(err)
	}
	items, _ = service.List(ctx, "u1")
	if len(items) != 1 {
		t.Fatalf("deduplicated memories = %d, want 1", len(items))
	}
	item := items[0]
	if item.Content != "我不吃香菜" || item.Type != "preference" || item.SourceMessageID != "m2" {
		t.Fatalf("memory = %#v", item)
	}
	recalled, _ := service.Recall(ctx, "u1", "点菜可以加香菜吗", 8)
	if len(recalled) != 1 {
		t.Fatalf("recall = %#v", recalled)
	}
	unrelated, _ := service.Recall(ctx, "u1", "我今天很累", 8)
	if len(unrelated) != 0 {
		t.Fatalf("unrelated recall = %#v", unrelated)
	}
	pinned := true
	updated, err := service.Update(ctx, "u1", item.ID, UpdateInput{Pinned: &pinned})
	if err != nil || !updated.Pinned {
		t.Fatalf("pin failed: %#v %v", updated, err)
	}
	if err = service.Delete(ctx, "u1", item.ID); err != nil {
		t.Fatal(err)
	}
	recalled, _ = service.Recall(ctx, "u1", "香菜", 8)
	if len(recalled) != 0 {
		t.Fatalf("deleted memory recalled: %#v", recalled)
	}
}

func TestSensitiveClassification(t *testing.T) {
	service := NewService(NewMemoryStore())
	if err := service.Observe(context.Background(), "u1", "c1", "m1", "记住我的手机号是13800138000"); err != nil {
		t.Fatal(err)
	}
	items, _ := service.List(context.Background(), "u1")
	if len(items) != 1 || items[0].Sensitivity != "sensitive" {
		t.Fatalf("sensitivity = %#v", items)
	}
}

func TestRecallContextExcludesExpiredAndFutureFacts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now().UTC()
	for _, key := range []string{"active", "expired", "future"} {
		item := Memory{ID: key, UserID: "u1", Content: "项目汇报" + key, NormalizedHash: key, Status: "active", Pinned: true, ValidFrom: now.Add(-time.Hour), UpdatedAt: now, SourceMessageID: "source"}
		if key == "expired" {
			item.ValidTo = &now
		}
		if key == "future" {
			item.ValidFrom = now.Add(time.Hour)
		}
		_, _, _ = store.UpsertMemory(ctx, item)
	}
	service := NewService(store)
	service.now = func() time.Time { return now }
	items, err := service.RecallContext(ctx, "u1", "项目汇报", 8)
	if err != nil || len(items) != 1 || items[0].Source.ID != "active" || items[0].Source.MessageID != "source" {
		t.Fatalf("recall = %#v %v", items, err)
	}
}

func TestRecallAppliesTypeAwareDateDecay(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	items := []Memory{
		{ID: "recent", UserID: "u1", Type: "commitment", Content: "项目汇报安排在周五", NormalizedHash: "recent", Confidence: 1, Importance: .7, Sensitivity: "normal", Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now.Add(-24 * time.Hour)},
		{ID: "old", UserID: "u1", Type: "commitment", Content: "项目汇报安排在月底", NormalizedHash: "old", Confidence: 1, Importance: .7, Sensitivity: "normal", Status: "active", ValidFrom: now, CreatedAt: now, UpdatedAt: now.Add(-180 * 24 * time.Hour)},
	}
	for _, item := range items {
		if _, _, err := store.UpsertMemory(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(store)
	service.now = func() time.Time { return now }
	recalled, err := service.Recall(ctx, "u1", "项目汇报什么时候安排", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recalled) != 2 || recalled[0] != items[0].Content {
		t.Fatalf("decayed recall = %#v", recalled)
	}
	if memoryHalfLifeDays("preference") <= memoryHalfLifeDays("commitment") {
		t.Fatal("stable preferences should decay more slowly than commitments")
	}
}
