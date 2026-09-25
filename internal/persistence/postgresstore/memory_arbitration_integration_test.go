package postgresstore

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func TestMemoryArbitrationRoundTrip(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	userID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := identity.User{ID: userID, Email: userID + "@arbitration.test", PasswordHash: "test", DisplayName: "仲裁测试", Timezone: "UTC", Locale: "zh-CN", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err = store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(ctx, "DELETE FROM app.long_term_memories WHERE user_id=$1", userID)
		_, _ = store.db.ExecContext(ctx, "DELETE FROM app.users WHERE id=$1", userID)
	})
	service := memory.NewService(store)
	first, err := service.Create(ctx, userID, "我喜欢安静的工作环境")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(ctx, userID, "我不喜欢安静的工作环境")
	if err != nil {
		t.Fatal(err)
	}
	if second.Arbitration == nil {
		t.Fatal("missing arbitration")
	}
	reader, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	loaded, err := reader.GetMemory(ctx, userID, second.ID)
	if err != nil || loaded.Arbitration == nil || loaded.Arbitration.InputHash != second.Arbitration.InputHash {
		t.Fatalf("roundtrip: %+v %v", loaded, err)
	}
	pinned := true
	_, err = service.Update(ctx, userID, second.ID, memory.UpdateInput{Pinned: &pinned})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = reader.GetMemory(ctx, userID, second.ID)
	if err != nil || !loaded.Pinned || loaded.Arbitration == nil {
		t.Fatal("update discarded record")
	}
	if err = service.Delete(ctx, userID, first.ID); err != nil {
		t.Fatal(err)
	}
	items, err := service.List(ctx, userID)
	if err != nil || len(items) != 1 || items[0].Arbitration != nil {
		t.Fatal("deleted reference leaked through record")
	}
	replacement, err := service.Correct(ctx, userID, second.ID, "现在喜欢安静的工作环境")
	if err != nil || replacement.Arbitration != nil || replacement.SupersedesID != second.ID {
		t.Fatalf("correction: %+v %v", replacement, err)
	}
	if _, err = reader.GetMemory(ctx, userID, second.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatal("old memory remained active")
	}
	other, _ := id.New()
	if _, err = reader.GetMemory(ctx, other, replacement.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Fatal("cross-owner memory read")
	}
}
