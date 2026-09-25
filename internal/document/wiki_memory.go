package document

import (
	"context"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"sort"
	"strings"
	"sync"
	"time"
)

type wikiMemoryJob struct {
	job         WikiJob
	status      string
	next, lease time.Time
}
type WikiMemoryStore struct {
	mu       sync.Mutex
	pages    map[string]WikiPage
	versions map[string][]WikiPage
	jobs     map[string]wikiMemoryJob
	feedback []WikiFeedback
	shards   map[string]wikiShard
}

func NewWikiMemoryStore() *WikiMemoryStore {
	return &WikiMemoryStore{pages: map[string]WikiPage{}, versions: map[string][]WikiPage{}, jobs: map[string]wikiMemoryJob{}}
}
func copyWiki(p WikiPage) WikiPage {
	r := cloneWikiResult(WikiSearchResult{Hits: []WikiSearchHit{{Page: p}}})
	out := r.Hits[0].Page
	out.OwnerID = p.OwnerID
	return out
}
func (s *WikiMemoryStore) ListWikiPages(_ context.Context, owners []string) ([]WikiPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed := map[string]bool{}
	for _, owner := range owners {
		allowed[owner] = true
	}
	pages := []WikiPage{}
	for _, p := range s.pages {
		if allowed[p.OwnerID] {
			pages = append(pages, copyWiki(p))
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].UpdatedAt.After(pages[j].UpdatedAt) })
	return pages, nil
}
func (s *WikiMemoryStore) save(p WikiPage, expected int) (WikiPage, error) {
	old := s.pages[p.ID]
	if old.Version != expected {
		return WikiPage{}, ErrWikiConflict
	}
	if old.Version > 0 && sameWikiContent(old, p) {
		return old, nil
	}
	p.Version = expected + 1
	s.pages[p.ID] = copyWiki(p)
	s.versions[p.ID] = append(s.versions[p.ID], copyWiki(p))
	return p, nil
}
func (s *WikiMemoryStore) SaveWikiPage(_ context.Context, p WikiPage, expected int) (WikiPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.save(p, expected)
	if err != nil {
		return WikiPage{}, err
	}
	if expected > 0 && !p.Edited {
		for _, source := range p.Sources {
			s.jobs[source.DocumentID] = wikiMemoryJob{job: WikiJob{UserID: p.OwnerID, DocumentID: source.DocumentID}, status: "queued", next: p.UpdatedAt}
		}
	}
	return saved, nil
}
func (s *WikiMemoryStore) WikiVersions(_ context.Context, user, page string) ([]WikiPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WikiPage{}
	for _, p := range s.versions[page] {
		if p.OwnerID == user {
			out = append(out, copyWiki(p))
		}
	}
	return out, nil
}
func (s *WikiMemoryStore) EnqueueWiki(_ context.Context, user, doc string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[doc] = wikiMemoryJob{job: WikiJob{DocumentID: doc, UserID: user}, status: "queued", next: now}
	return nil
}
func (s *WikiMemoryStore) ClaimWiki(_ context.Context, now time.Time) (WikiJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, j := range s.jobs {
		busy := false
		for otherKey, other := range s.jobs {
			if otherKey != key && other.job.UserID == j.job.UserID && other.status == "processing" && other.lease.After(now) {
				busy = true
			}
		}
		if busy {
			continue
		}
		if (j.status == "queued" && !j.next.After(now)) || (j.status == "processing" && !j.lease.After(now)) {
			token, err := id.New()
			if err != nil {
				return WikiJob{}, err
			}
			j.job.Token = token
			j.job.Attempts++
			j.status = "processing"
			j.lease = now.Add(10 * time.Minute)
			s.jobs[key] = j
			return j.job, nil
		}
	}
	return WikiJob{}, ErrNotFound
}
func (s *WikiMemoryStore) FinishWiki(_ context.Context, job WikiJob, pages []WikiPage, code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[job.DocumentID]
	if !ok || j.job.Token != job.Token || j.status != "processing" || !j.lease.After(now) {
		return ErrWikiConflict
	}
	if code != "" {
		j.status = "queued"
		j.next = now.Add(time.Duration(min(job.Attempts*job.Attempts, 60)) * time.Minute)
		if job.Attempts >= 5 {
			j.status = "failed"
		}
		s.jobs[job.DocumentID] = j
		return nil
	}
	if pages != nil {
		for _, p := range pages {
			if p.OwnerID != job.UserID {
				return ErrValidation
			}
		}
		keep := map[string]bool{}
		for _, p := range pages {
			keep[p.ID] = true
			old := s.pages[p.ID]
			if old.Edited {
				continue
			}
			if _, err := s.save(p, old.Version); err != nil {
				return err
			}
		}
		for key, p := range s.pages {
			if p.OwnerID == job.UserID && (strings.HasSuffix(p.Compiler, ":aggregate") || (len(p.Sources) == 1 && p.Sources[0].DocumentID == job.DocumentID)) && !p.Edited && !keep[key] {
				delete(s.pages, key)
			}
		}
	}
	j.status = "completed"
	s.jobs[job.DocumentID] = j
	return nil
}
func (s *WikiMemoryStore) SaveWikiFeedback(_ context.Context, user string, f WikiFeedback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pages[f.PageID]
	if !ok || p.OwnerID != user {
		return ErrNotFound
	}
	if p.Version != f.Version {
		return ErrWikiConflict
	}
	s.feedback = append(s.feedback, f)
	if f.Rating != "helpful" {
		for _, source := range p.Sources {
			s.jobs[source.DocumentID] = wikiMemoryJob{job: WikiJob{UserID: user, DocumentID: source.DocumentID}, status: "queued", next: f.CreatedAt}
		}
	}
	return nil
}
