package skill

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerBoundsConcurrentPollingAndEventClaims(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var active, peak atomic.Int32
	service, _, _ := newQueuedTestService(t, HandlerFunc(func(ctx context.Context, input map[string]any) (ToolResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ToolResult{}, ctx.Err()
		}
		return ToolResult{Output: map[string]any{"value": input["value"]}}, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ids []string
	for _, key := range []string{"a", "b", "c", "d"} {
		run, _, err := service.Start(ctx, "u1", "test.worker", key, map[string]any{"value": key})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	runner := NewRunner(service, "parallel", time.Minute, 10*time.Second)
	runner.SetMaxConcurrency(2)
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, time.Millisecond) }()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("tasks did not overlap")
		}
	}
	eventDone := make(chan error, 1)
	go func() { _, err := runner.RunID(ctx, ids[3]); eventDone <- err }()
	select {
	case <-entered:
		t.Fatal("exceeded two execution slots")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	deadline := time.After(3 * time.Second)
	for {
		completed := 0
		for _, id := range ids {
			run, _ := service.Get(ctx, "u1", id)
			if run.Status == "succeeded" {
				completed++
			}
		}
		if completed == len(ids) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("queued tasks did not drain")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("runner did not stop")
	}
	select {
	case <-eventDone:
	case <-time.After(time.Second):
		t.Fatal("event claim did not stop")
	}
	if peak.Load() != 2 || active.Load() != 0 {
		t.Fatalf("peak=%d active=%d", peak.Load(), active.Load())
	}
}

func TestRunnerCancelledBeforeClaimLeavesTaskQueued(t *testing.T) {
	service, _, _ := newQueuedTestService(t, HandlerFunc(func(context.Context, map[string]any) (ToolResult, error) {
		t.Error("executed after cancellation")
		return ToolResult{}, nil
	}))
	run, _, _ := service.Start(context.Background(), "u1", "test.worker", "cancelled", map[string]any{"value": "ok"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := NewRunner(service, "worker", time.Minute, time.Second)
	if _, err := runner.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	got, _ := service.Get(context.Background(), "u1", run.ID)
	if got.Status != "queued" {
		t.Fatalf("status=%s", got.Status)
	}
}

func TestSkillResourceClassesBoundHeavyTasksIndependently(t *testing.T) {
	runner := NewRunner(nil, "test", time.Minute, time.Second)
	runner.SetResourceConcurrency(1, 1)
	releaseRender, err := runner.acquireResource(context.Background(), "office.pptx_generate")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRender()
	releaseParse, err := runner.acquireResource(context.Background(), "office.document_extract")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseParse()
	for _, name := range []string{"office.pdf_translate", "office.tabular_profile"} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		if release, err := runner.acquireResource(ctx, name); !errors.Is(err, context.DeadlineExceeded) {
			if release != nil {
				release()
			}
			t.Fatalf("resource cap bypassed by %s: %v", name, err)
		}
		cancel()
	}
}

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
