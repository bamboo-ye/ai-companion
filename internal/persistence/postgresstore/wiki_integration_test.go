package postgresstore

import (
	"context"
	"errors"
	"github.com/windcry1/ai-companion/internal/document"
	"os"
	"sync"
	"testing"
	"time"
)

func TestWikiShardCacheScopesRevisionsOwnersAndExpiry(t *testing.T) {
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
	owner, doc, key := mustID(t), mustID(t), "source-model-batch-hash"
	t.Cleanup(func() { _, _ = store.db.ExecContext(ctx, "DELETE FROM app.wiki_shards WHERE owner_id=$1", owner) })
	data := []byte(`{"pages":[]}`)
	if err := store.SaveWikiShard(ctx, owner, doc, key, data); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadWikiShard(ctx, owner, doc, key)
	if err != nil || len(got) == 0 {
		t.Fatalf("cached %s %v", got, err)
	}
	for _, scope := range [][3]string{{mustID(t), doc, key}, {owner, mustID(t), key}, {owner, doc, "new-model-version"}} {
		if _, err := store.LoadWikiShard(ctx, scope[0], scope[1], scope[2]); !errors.Is(err, document.ErrNotFound) {
			t.Fatalf("cross-scope cache hit: %v", err)
		}
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE app.wiki_shards SET expires_at=$1 WHERE owner_id=$2", time.Now().Add(-time.Hour), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadWikiShard(ctx, owner, doc, key); !errors.Is(err, document.ErrNotFound) {
		t.Fatalf("expired shard returned: %v", err)
	}
}

func TestWikiSQLTransactionsFencingAndRevisionHistory(t *testing.T) {
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
	owner, doc, pageID := mustID(t), mustID(t), mustID(t)
	now := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		for _, table := range []string{"wiki_feedback", "wiki_versions", "wiki_pages", "wiki_jobs", "wiki_owners"} {
			_, _ = store.db.ExecContext(ctx, "DELETE FROM app."+table+" WHERE owner_id=$1", owner)
		}
	})
	if err = store.EnqueueWiki(ctx, owner, doc, now); err != nil {
		t.Fatal(err)
	}
	if err = store.EnqueueWiki(ctx, owner, mustID(t), now); err != nil {
		t.Fatal(err)
	}
	var jobs []document.WikiJob
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, e := store.ClaimWiki(ctx, now)
			if e == nil {
				mu.Lock()
				jobs = append(jobs, job)
				mu.Unlock()
			} else if !errors.Is(e, document.ErrNotFound) {
				t.Errorf("claim: %v", e)
			}
		}()
	}
	wg.Wait()
	if len(jobs) != 1 {
		t.Fatalf("concurrent same-owner claims %d: %+v", len(jobs), jobs)
	}
	job := jobs[0]
	p := document.WikiPage{ID: pageID, OwnerID: owner, Kind: "source", Title: "来源", Body: "预算：100", Sources: []document.WikiSource{{DocumentID: job.DocumentID, Version: "version-a", Name: "计划"}}, UpdatedAt: now}
	if err = store.FinishWiki(ctx, job, []document.WikiPage{p}, "", now); err != nil {
		t.Fatal(err)
	}
	pages, err := store.ListWikiPages(ctx, []string{owner})
	if err != nil || len(pages) != 1 || pages[0].Version != 1 || pages[0].OwnerID != owner {
		t.Fatalf("published %+v %v", pages, err)
	}
	if err = store.FinishWiki(ctx, job, []document.WikiPage{p}, "", now); !errors.Is(err, document.ErrWikiConflict) {
		t.Fatal("replayed expired writer published")
	}
	p = pages[0]
	p.Edited = true
	p.Body = "人工校正"
	p, err = store.SaveWikiPage(ctx, p, 1)
	if err != nil || p.Version != 2 {
		t.Fatal(err)
	}
	if _, err = store.SaveWikiPage(ctx, p, 1); !errors.Is(err, document.ErrWikiConflict) {
		t.Fatal("CAS accepted stale editor")
	}
	versions, err := store.WikiVersions(ctx, owner, pageID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("history %v %v", versions, err)
	}
	other, err := store.ListWikiPages(ctx, []string{mustID(t)})
	if err != nil || len(other) != 0 {
		t.Fatal("owner filtering failed")
	}
	feedback := document.WikiFeedback{ID: mustID(t), PageID: pageID, Version: 2, Rating: "incorrect", CreatedAt: now}
	if err = store.SaveWikiFeedback(ctx, owner, feedback); err != nil {
		t.Fatal(err)
	}
	if err = store.EnqueueWiki(ctx, owner, job.DocumentID, now); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := store.ClaimWiki(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Token == job.Token {
		t.Fatal("lease token reused")
	}
	if err = store.FinishWiki(ctx, reclaimed, nil, "failed", now); err != nil {
		t.Fatal(err)
	}
	var status string
	var next time.Time
	if err = store.db.QueryRowContext(ctx, "SELECT status,next_attempt FROM app.wiki_jobs WHERE document_id=$1", reclaimed.DocumentID).Scan(&status, &next); err != nil || status != "queued" || !next.After(now) {
		t.Fatalf("durable retry %s %v %v", status, next, err)
	}
}
