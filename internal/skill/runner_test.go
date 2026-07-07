package skill

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newQueuedTestService(t *testing.T, handler Handler) (*Service, *MemoryStore, *MemoryFileStore) {
	t.Helper()
	registry := NewRegistry()
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.worker", Version: "1.0.0", DisplayName: "Worker", Category: "test", RiskLevel: "none", Enabled: true,
		ToolName: "test.worker", ExecutionMode: "worker", TimeoutMS: 5_000, MaxSteps: 8,
		InputSchema:  objectSchema([]string{"value"}, map[string]any{"value": map[string]any{"type": "string"}}),
		OutputSchema: objectSchema([]string{"value"}, map[string]any{"value": map[string]any{"type": "string"}}),
	}, Handler: handler})
	if err != nil {
		t.Fatal(err)
	}
	store, files := NewMemoryStore(), NewMemoryFileStore()
	service := NewService(store, files, registry)
	service.SetWorkerQueue(true)
	return service, store, files
}

func TestQueuedSkillExecutesOnlyAfterWorkerClaim(t *testing.T) {
	calls := 0
	service, _, _ := newQueuedTestService(t, HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		calls++
		return ToolResult{Output: map[string]any{"value": input["value"]}}, nil
	}))
	run, created, err := service.Start(context.Background(), "u1", "test.worker", "queue-1", map[string]any{"value": "ok"})
	if err != nil || !created || run.Status != "queued" || calls != 0 {
		t.Fatalf("queued run=%#v calls=%d err=%v", run, calls, err)
	}
	runner := NewRunner(service, "worker-1", time.Minute, 10*time.Second)
	processed, err := runner.RunOnce(context.Background())
	if err != nil || !processed || calls != 1 {
		t.Fatalf("processed=%v calls=%d err=%v", processed, calls, err)
	}
	completed, err := service.Get(context.Background(), "u1", run.ID)
	if err != nil || completed.Status != "succeeded" || completed.WorkerID != "" || completed.LeaseExpiresAt != nil {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestRunIDClaimsOnlyTheKafkaTarget(t *testing.T) {
	service, _, _ := newQueuedTestService(t, HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		return ToolResult{Output: map[string]any{"value": input["value"]}}, nil
	}))
	first, _, err := service.Start(context.Background(), "u1", "test.worker", "target-first", map[string]any{"value": "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.Start(context.Background(), "u1", "test.worker", "target-second", map[string]any{"value": "second"})
	if err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(service, "worker-kafka", time.Minute, 10*time.Second)
	processed, err := runner.RunID(context.Background(), second.ID)
	if err != nil || !processed {
		t.Fatalf("RunID processed=%v err=%v", processed, err)
	}
	remaining, _ := service.Get(context.Background(), "u1", first.ID)
	completed, _ := service.Get(context.Background(), "u1", second.ID)
	if remaining.Status != "queued" || completed.Status != "succeeded" {
		t.Fatalf("first=%s second=%s", remaining.Status, completed.Status)
	}
}

func TestExpiredLeaseCanBeTakenOverAndRevisionFencesOldWorker(t *testing.T) {
	calls := 0
	service, _, _ := newQueuedTestService(t, HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		calls++
		return ToolResult{Output: map[string]any{"value": input["value"]}}, nil
	}))
	base := time.Date(2026, 7, 5, 2, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	queued, _, err := service.Start(context.Background(), "u1", "test.worker", "takeover-1", map[string]any{"value": "fenced"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Claim(context.Background(), "worker-old", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(2 * time.Minute) }
	second, err := service.Claim(context.Background(), "worker-new", time.Minute)
	if err != nil || second.ID != first.ID || second.Revision != first.Revision+1 {
		t.Fatalf("takeover first=%#v second=%#v err=%v", first, second, err)
	}
	if err = service.RenewLease(context.Background(), first, "worker-old", time.Minute); !errors.Is(err, ErrConflict) {
		t.Fatalf("old renew error=%v", err)
	}
	if _, err = service.ExecuteClaimed(context.Background(), first, "worker-old"); !errors.Is(err, ErrConflict) {
		t.Fatalf("old completion error=%v", err)
	}
	completed, err := service.ExecuteClaimed(context.Background(), second, "worker-new")
	if err != nil || completed.Status != "succeeded" || completed.ID != queued.ID || calls != 2 {
		t.Fatalf("completed=%#v calls=%d err=%v", completed, calls, err)
	}
}

func TestLeaseRenewalPreventsPrematureTakeover(t *testing.T) {
	service, _, _ := newQueuedTestService(t, HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		return ToolResult{Output: map[string]any{"value": input["value"]}}, nil
	}))
	base := time.Date(2026, 7, 5, 3, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if _, _, err := service.Start(context.Background(), "u1", "test.worker", "renew-1", map[string]any{"value": "ok"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := service.Claim(context.Background(), "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(30 * time.Second) }
	if err = service.RenewLease(context.Background(), claimed, "worker-1", time.Minute); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return base.Add(70 * time.Second) }
	if _, err = service.Claim(context.Background(), "worker-2", time.Minute); !errors.Is(err, ErrNoQueuedRun) {
		t.Fatalf("premature claim error=%v", err)
	}
}
