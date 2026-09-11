package postgresstore

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
)

func TestAgentOperationsQueries(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	items, err := store.ListAgentRuns(ctx, agent.RunOperationsFilter{Limit: 5})
	if err != nil {
		t.Fatalf("ListAgentRuns() error = %v", err)
	}
	if len(items) == 0 {
		return
	}
	first := items[0]
	if first.ID == "" || first.Status == "" || first.CreatedAt.IsZero() {
		t.Fatalf("ListAgentRuns() returned incomplete summary: %#v", first)
	}
	exact, err := store.ListAgentRuns(ctx, agent.RunOperationsFilter{Query: first.ID, Limit: 1})
	if err != nil || len(exact) != 1 || exact[0].ID != first.ID {
		t.Fatalf("ListAgentRuns(exact) = %#v, %v", exact, err)
	}
	if _, err = store.GetAgentRun(ctx, first.ID); err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if _, err = store.ListAgentToolCalls(ctx, first.ID); err != nil {
		t.Fatalf("ListAgentToolCalls() error = %v", err)
	}
	if len(items) > 1 {
		cursorTime := items[0].CreatedAt
		page, pageErr := store.ListAgentRuns(ctx, agent.RunOperationsFilter{
			CursorCreatedAt: &cursorTime, CursorID: items[0].ID, Limit: 5,
		})
		if pageErr != nil {
			t.Fatalf("ListAgentRuns(cursor) error = %v", pageErr)
		}
		if len(page) > 0 && page[0].ID == items[0].ID {
			t.Fatalf("cursor repeated boundary run %s", items[0].ID)
		}
	}
}
