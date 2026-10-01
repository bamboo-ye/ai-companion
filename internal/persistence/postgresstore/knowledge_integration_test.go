package postgresstore

import (
	"context"
	"errors"
	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/knowledge"
	"github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/semantic"
	"os"
	"testing"
	"time"
)

func TestKnowledgeDurableScopesProjectionsAndFeedback(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, _ := id.New()
	now := time.Now().UTC()
	user := identity.User{ID: owner, Email: owner + "@knowledge.test", PasswordHash: "fixture", DisplayName: "Knowledge Test", Timezone: "UTC", Locale: "zh-CN", Status: "active", CreatedAt: now, UpdatedAt: now}
	if err = store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = store.db.ExecContext(ctx, "DELETE FROM app.knowledge_feedback WHERE user_id=$1", owner)
		_, _ = store.db.ExecContext(ctx, "DELETE FROM app.long_term_memories WHERE user_id=$1", owner)
		_, _ = store.db.ExecContext(ctx, "DELETE FROM app.users WHERE id=$1", owner)
	})
	m := memory.NewService(store)
	first, err := m.Create(ctx, owner, "知识检索的权限必须实时验证")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Create(ctx, owner, "来源版本更新后要重新核对")
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.ListKnowledgeMemories(ctx, owner, "", 1)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	next, err := store.ListKnowledgeMemories(ctx, owner, items[0].ID, 1)
	if err != nil || len(next) != 1 || next[0].ID <= items[0].ID {
		t.Fatal(next, err)
	}
	for _, workspace := range []string{"", owner} {
		docs, err := store.ListKnowledgeDocuments(ctx, owner, workspace, "", 256)
		if err != nil || len(docs) != 0 {
			t.Fatal(docs, err)
		}
	}
	owners, err := store.ListKnowledgeOwners(ctx, "", 10000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range owners {
		found = found || v == owner
	}
	if !found {
		t.Fatal("owner not discoverable")
	}
	projections := store.KnowledgeProjectionStore()
	defer projections.DeleteSource(ctx, "memory", owner, "")
	c := semantic.New(semantic.Config{EmbeddingModel: "fixture", Dimensions: 2, EmbeddingRevision: owner})
	parts := c.SourceProjections(owner, "memory", first.ID, memory.KnowledgeRevision(first), first.Content)
	if err = projections.Queue(ctx, parts, now); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := projections.Claim(ctx, c.Version(), now.Add(time.Second))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok, err = projections.Claim(ctx, c.Version(), now.Add(time.Second)); err != nil || ok {
		t.Fatal("duplicate SQL claim", err)
	}
	later := now.Add(3 * time.Minute)
	reclaimed, ok, err := projections.Claim(ctx, c.Version(), later)
	if err != nil || !ok || claimed.Lease == reclaimed.Lease {
		t.Fatal("lease recovery", err)
	}
	if err = projections.Renew(ctx, reclaimed, later.Add(time.Second)); err != nil {
		t.Fatal("renew current lease", err)
	}
	if err = projections.Renew(ctx, claimed, later.Add(time.Second)); !errors.Is(err, semantic.ErrProjectionLease) {
		t.Fatal("renew stale lease", err)
	}
	if err = projections.Finish(ctx, claimed, []float32{1, 0}, "", later); !errors.Is(err, semantic.ErrProjectionLease) {
		t.Fatal("old lease published", err)
	}
	if err = projections.Finish(ctx, reclaimed, []float32{1, 0}, "", later); err != nil {
		t.Fatal(err)
	}
	if err = projections.Queue(ctx, parts, later); err != nil {
		t.Fatal(err)
	}
	vectors, err := projections.Lookup(ctx, []string{parts[0].Key})
	if err != nil || len(vectors) != 1 || vectors[parts[0].Key][0] != 1 {
		t.Fatal(vectors, err)
	}
	if _, ok, err = projections.Claim(ctx, c.Version(), later); err != nil || ok {
		t.Fatal("idempotent queue reset ready vector", err)
	}
	if err = projections.DeleteSource(ctx, "memory", owner, first.ID); err != nil {
		t.Fatal(err)
	}
	vectors, err = projections.Lookup(ctx, []string{parts[0].Key})
	if err != nil || len(vectors) != 0 {
		t.Fatal("delete failed", err)
	}
	stale := parts[0]
	stale.Key = stale.Key[:60] + "dead"
	if err = projections.Queue(ctx, []semantic.Projection{stale}, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = projections.Prune(ctx, now.Add(-24*time.Hour), func(context.Context, []semantic.Projection) error { return errors.New("vector deletion unavailable") }); err == nil {
		t.Fatal("cleanup failure committed SQL deletion")
	}
	var cleaned []semantic.Projection
	n, err := projections.Prune(ctx, now.Add(-24*time.Hour), func(_ context.Context, items []semantic.Projection) error {
		cleaned = append(cleaned, items...)
		return nil
	})
	if err != nil || n != 1 || len(cleaned) != 1 || cleaned[0].Key != stale.Key {
		t.Fatal("durable cleanup", n, cleaned, err)
	}
	feedback := store.KnowledgeFeedbackStore()
	fid, _ := id.New()
	f := knowledge.Feedback{ID: fid, UserID: owner, Rating: "incorrect", Ref: knowledge.Ref{Kind: "memory", SourceID: first.ID}, CreatedAt: now, Status: "pending"}
	if err = feedback.SaveKnowledgeFeedback(ctx, f); err != nil {
		t.Fatal(err)
	}
	list, err := feedback.ListKnowledgeFeedback(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, v := range list {
		found = found || v.ID == fid
	}
	if !found {
		t.Fatal("feedback missing")
	}
	if err = feedback.ResolveKnowledgeFeedback(ctx, fid, "operator", "已完成来源核对"); err != nil {
		t.Fatal(err)
	}
	if err = feedback.ResolveKnowledgeFeedback(ctx, fid, "operator", "重复操作"); !errors.Is(err, knowledge.ErrConflict) {
		t.Fatal("feedback CAS", err)
	}
}
