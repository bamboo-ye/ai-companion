package document

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func wikiFixture(t *testing.T) (*Service, *MemoryStore, *Ingestor) {
	t.Helper()
	store := NewMemoryStore()
	blobs := NewMemoryBlobStore()
	service := NewService(store, blobs, 0)
	return service, store, NewIngestor(store, blobs, fixedParser{}, NoopVectorIndex{}, "wiki-test")
}
func ingestWiki(t *testing.T, s *Service, ingestor *Ingestor, user, name, content string) Document {
	t.Helper()
	ctx := context.Background()
	d, _, err := s.Upload(ctx, user, name, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ingestor.RunJob(ctx, d.JobID); err != nil {
		t.Fatal(err)
	}
	if worked, err := s.CompileNextWiki(ctx); err != nil || !worked {
		t.Fatalf("compile: %v %v", worked, err)
	}
	d, err = s.Get(ctx, user, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestWikiCompilationReplaysAndPreservesManualVersions(t *testing.T) {
	s, _, ingestor := wikiFixture(t)
	ctx := context.Background()
	d := ingestWiki(t, s, ingestor, "alice", "计划.txt", "预算：100元\n必须保留原文引用")
	pages, err := s.ListWiki(ctx, "alice", "")
	if err != nil || len(pages) != 2 {
		t.Fatalf("pages=%+v %v", pages, err)
	}
	for _, p := range pages {
		if p.Version != 1 || len(p.Evidence) == 0 || p.Sources[0].Version != SourceVersion(d) {
			t.Fatal("missing grounded version")
		}
	}
	if err = s.RebuildWiki(ctx, "alice", d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompileNextWiki(ctx); err != nil {
		t.Fatal(err)
	}
	pages, _ = s.ListWiki(ctx, "alice", "")
	for _, p := range pages {
		if p.Version != 1 {
			t.Fatal("unchanged compiler replay made another version")
		}
	}
	p, err := s.UpdateWiki(ctx, "alice", pages[0].ID, 1, "人工核对", pages[0].Body+"\n人工注释")
	if err != nil || p.Version != 2 {
		t.Fatalf("edit %v %+v", err, p)
	}
	if _, err = s.UpdateWiki(ctx, "alice", p.ID, 1, "并发覆盖", "bad"); !errors.Is(err, ErrWikiConflict) {
		t.Fatal("missing CAS")
	}
	_ = s.RebuildWiki(ctx, "alice", d.ID)
	_, err = s.CompileNextWiki(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.ReadWiki(ctx, "alice", "", p.ID)
	if err != nil || !p.Edited || p.Title != "人工核对" {
		t.Fatal("compiler overwrote edit")
	}
	versions, err := s.WikiHistory(ctx, "alice", p.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("versions %d %v", len(versions), err)
	}
}
func TestWikiACLIntersectionConflictsAndDeletionInvalidateCache(t *testing.T) {
	s, store, ingestor := wikiFixture(t)
	ctx := context.Background()
	a := ingestWiki(t, s, ingestor, "alice", "旧计划.txt", "预算：100元")
	b := ingestWiki(t, s, ingestor, "alice", "新计划.txt", "预算：200元")
	pages, _ := s.ListWiki(ctx, "alice", "")
	var aggregate WikiPage
	for _, p := range pages {
		if len(p.Sources) == 2 {
			aggregate = p
		}
	}
	if aggregate.ID == "" || len(aggregate.Conflicts) != 1 {
		t.Fatalf("aggregate=%+v", aggregate)
	}
	_ = store.ShareDocumentWithWorkspace(ctx, "alice", "team", a.ID, time.Now())
	shared, err := s.ListWiki(ctx, "bob", "team")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range shared {
		if p.ID == aggregate.ID {
			t.Fatal("private source escaped ACL intersection")
		}
	}
	if _, err = s.ReadWiki(ctx, "bob", "", aggregate.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross user wiki read")
	}
	_ = store.ShareDocumentWithWorkspace(ctx, "alice", "team", b.ID, time.Now())
	shared, _ = s.ListWiki(ctx, "bob", "team")
	found := false
	for _, p := range shared {
		found = found || p.ID == aggregate.ID
	}
	if !found {
		t.Fatal("fully shared aggregate hidden")
	}
	first, err := s.SearchWiki(ctx, "alice", "", "预算", 8, 6000)
	if err != nil || len(first.Hits) == 0 {
		t.Fatalf("search %+v %v", first, err)
	}
	cached, _ := s.SearchWiki(ctx, "alice", "", "预算", 8, 6000)
	if !cached.CacheHit {
		t.Fatal("cache not used")
	}
	// Mutating a returned result must not poison the cached reference.
	cached.Hits[0].Page.Body = "poison"
	again, _ := s.SearchWiki(ctx, "alice", "", "预算", 8, 6000)
	if again.Hits[0].Page.Body == "poison" {
		t.Fatal("cache aliases result")
	}
	if err = s.Delete(ctx, "alice", b.ID); err != nil {
		t.Fatal(err)
	}
	after, err := s.SearchWiki(ctx, "alice", "", "预算", 8, 6000)
	if err != nil || after.CacheHit {
		t.Fatal("stale cache after deletion")
	}
	for _, hit := range after.Hits {
		for _, source := range hit.Page.Sources {
			if source.DocumentID == b.ID {
				t.Fatal("deleted source leak")
			}
		}
	}
	if _, err = s.ReadWiki(ctx, "alice", "", aggregate.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted aggregate readable")
	}
	if _, err = s.CompileNextWiki(ctx); err != nil {
		t.Fatal(err)
	}
	persisted, _ := store.ListWikiPages(ctx, []string{"alice"})
	for _, p := range persisted {
		if p.ID == aggregate.ID {
			t.Fatal("invalid aggregate not retired")
		}
	}
}
func TestWikiStalenessAndBudgetAndFeedback(t *testing.T) {
	s, store, ingestor := wikiFixture(t)
	ctx := context.Background()
	d := ingestWiki(t, s, ingestor, "alice", "说明.txt", "检索预算：100\n"+strings.Repeat("完整资料。", 400))
	pages, _ := s.ListWiki(ctx, "alice", "")
	p := pages[0]
	if err := s.FeedbackWiki(ctx, "alice", p.ID, p.Version, "incorrect", "引用不准确"); err != nil {
		t.Fatal(err)
	}
	if len(store.feedback) != 1 {
		t.Fatal("feedback missing")
	}
	res, err := s.SearchWiki(ctx, "alice", "", "预算", 8, 128)
	if err != nil || res.Tokens > 128 {
		t.Fatal("search overflow")
	}
	store.mu.Lock()
	changed := store.items[d.ID]
	changed.UpdatedAt = changed.UpdatedAt.Add(time.Second)
	store.items[d.ID] = changed
	store.mu.Unlock()
	if _, err = s.ReadWiki(ctx, "alice", "", p.ID); !errors.Is(err, ErrWikiConflict) {
		t.Fatal("stale source returned")
	}
	res, err = s.SearchWiki(ctx, "alice", "", "预算", 8, 6000)
	if err != nil || len(res.Hits) != 0 {
		t.Fatal("stale search evidence")
	}
}
func TestWikiJobFencingRetryAndOwnerSerialization(t *testing.T) {
	store := NewWikiMemoryStore()
	ctx := context.Background()
	now := time.Now()
	_ = store.EnqueueWiki(ctx, "alice", "doc1", now)
	_ = store.EnqueueWiki(ctx, "alice", "doc2", now)
	var jobs []WikiJob
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := store.ClaimWiki(ctx, now)
			if e == nil {
				mu.Lock()
				jobs = append(jobs, j)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(jobs) != 1 {
		t.Fatalf("same owner has %d concurrent jobs", len(jobs))
	}
	old := jobs[0]
	_ = store.EnqueueWiki(ctx, "alice", old.DocumentID, now)
	if err := store.FinishWiki(ctx, old, nil, "", now); !errors.Is(err, ErrWikiConflict) {
		t.Fatal("old lease published")
	}
	j, err := store.ClaimWiki(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishWiki(ctx, j, nil, "provider_unavailable", now); err != nil {
		t.Fatal(err)
	}
	if store.jobs[j.DocumentID].status != "queued" || !store.jobs[j.DocumentID].next.After(now) {
		t.Fatal("failure not durably rescheduled")
	}
}
