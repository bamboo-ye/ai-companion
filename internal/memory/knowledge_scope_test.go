package memory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestRecallBeyondFiveHundredAndValidity(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewService(store)
	now := time.Now().UTC()
	for i := 0; i < 770; i++ {
		key := fmt.Sprintf("memory-%04d", i)
		store.items[key] = Memory{ID: key, UserID: "alice", Content: "日常事项", Status: "active", UpdatedAt: now, ValidFrom: now.Add(-time.Hour)}
	}
	target := store.items["memory-0769"]
	target.Content = "喜欢冰岛极光摄影"
	store.items[target.ID] = target
	hits, err := s.RecallContext(ctx, "alice", "冰岛极光", 8)
	if err != nil || len(hits) != 1 || hits[0].Source.ID != target.ID || hits[0].Source.Version == "" {
		t.Fatal(hits, err)
	}
	target.ValidFrom = now.Add(time.Hour)
	store.items[target.ID] = target
	hits, err = s.RecallContext(ctx, "alice", "冰岛极光", 8)
	if err != nil || len(hits) != 0 {
		t.Fatal("future fact visible", hits, err)
	}
}
