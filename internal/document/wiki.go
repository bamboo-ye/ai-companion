package document

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/windcry1/ai-companion/internal/arbitration"
	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/semantic"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const WikiCompilerVersion = "grounded-wiki-v1"

var ErrWikiConflict = errors.New("wiki version conflict")

type WikiSource struct {
	DocumentID string `json:"document_id"`
	Version    string `json:"version"`
	Name       string `json:"name"`
}
type WikiEvidence struct {
	DocumentID string `json:"document_id"`
	ChunkID    string `json:"chunk_id"`
	Quote      string `json:"quote"`
	Page       int    `json:"page"`
}
type WikiPage struct {
	ID          string              `json:"id"`
	OwnerID     string              `json:"-"`
	Version     int                 `json:"version"`
	Kind        string              `json:"kind"`
	Title       string              `json:"title"`
	Body        string              `json:"body"`
	Sources     []WikiSource        `json:"sources"`
	Evidence    []WikiEvidence      `json:"evidence"`
	Links       []string            `json:"links"`
	Conflicts   []string            `json:"conflicts"`
	Compiler    string              `json:"compiler"`
	Edited      bool                `json:"edited"`
	Stale       bool                `json:"stale"`
	UpdatedAt   time.Time           `json:"updated_at"`
	Synthesis   *WikiSynthesis      `json:"synthesis,omitempty"`
	Arbitration *arbitration.Record `json:"arbitration,omitempty"`
}

// Coverage describes semantic processing separately from the exhaustive
// source pages, including explicit fallback when the model budget is too small.
type WikiSynthesis struct {
	Batches   int    `json:"batches"`
	Completed int    `json:"completed"`
	Degraded  string `json:"degraded,omitempty"`
}
type WikiJob struct {
	DocumentID string
	UserID     string
	Token      string
	Attempts   int
}
type WikiFeedback struct {
	ID        string    `json:"id"`
	PageID    string    `json:"page_id"`
	Version   int       `json:"version"`
	Rating    string    `json:"rating"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}
type WikiStore interface {
	ListWikiPages(context.Context, []string) ([]WikiPage, error)
	SaveWikiPage(context.Context, WikiPage, int) (WikiPage, error)
	WikiVersions(context.Context, string, string) ([]WikiPage, error)
	EnqueueWiki(context.Context, string, string, time.Time) error
	ClaimWiki(context.Context, time.Time) (WikiJob, error)
	FinishWiki(context.Context, WikiJob, []WikiPage, string, time.Time) error
	SaveWikiFeedback(context.Context, string, WikiFeedback) error
}
type wikiCacheEntry struct {
	result  WikiSearchResult
	expires time.Time
}
type wikiRuntime struct {
	mu                                                sync.Mutex
	cache                                             map[string]wikiCacheEntry
	vectors                                           map[string]wikiVector
	hits, misses, searches, reads, compiles, failures atomic.Int64
}
type WikiMetrics struct {
	CacheHits   int64            `json:"cache_hits"`
	CacheMisses int64            `json:"cache_misses"`
	Searches    int64            `json:"searches"`
	Reads       int64            `json:"reads"`
	Compiles    int64            `json:"compiles"`
	Failures    int64            `json:"failures"`
	Model       semantic.Metrics `json:"model"`
}

func (s *Service) SetKnowledge(client *semantic.Client, enabled bool) {
	s.semantic = client
	s.wikiEnabled = enabled
}
func (s *Service) WikiMetrics() WikiMetrics {
	w := s.wikiRuntime
	m := WikiMetrics{CacheHits: w.hits.Load(), CacheMisses: w.misses.Load(), Searches: w.searches.Load(), Reads: w.reads.Load(), Compiles: w.compiles.Load(), Failures: w.failures.Load()}
	if s.semantic != nil {
		m.Model = s.semantic.Metrics()
	}
	return m
}
func SourceVersion(d Document) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d:%s", d.SHA256, d.ParserVersion, d.ChunkCount, d.UpdatedAt.UTC().Format(time.RFC3339Nano))))
	return hex.EncodeToString(sum[:])
}
func (s *Service) wikiDocuments(ctx context.Context, user, workspace string) ([]Document, error) {
	if !s.wikiEnabled {
		return nil, fmt.Errorf("%w: wiki disabled", ErrValidation)
	}
	if workspace != "" {
		return s.store.ListWorkspaceDocuments(ctx, workspace, 10000)
	}
	return s.store.ListDocuments(ctx, user, 10000)
}

// Workspace membership must be established by the HTTP/Agent boundary. All
// dependencies, not merely one shared source, must belong to this exact scope.
func (s *Service) ListWiki(ctx context.Context, user, workspace string) ([]WikiPage, error) {
	docs, err := s.wikiDocuments(ctx, user, workspace)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(WikiStore)
	if !ok {
		return []WikiPage{}, nil
	}
	owners := []string{}
	seen := map[string]bool{}
	allowed := map[string]Document{}
	for _, d := range docs {
		if d.Status == "ready" {
			allowed[d.ID] = d
			if !seen[d.UserID] {
				seen[d.UserID] = true
				owners = append(owners, d.UserID)
			}
		}
	}
	if workspace == "" && !seen[user] {
		owners = append(owners, user)
	}
	pages, err := store.ListWikiPages(ctx, owners)
	if err != nil {
		return nil, err
	}
	result := []WikiPage{}
	for _, p := range pages {
		visible := len(p.Sources) > 0
		p.Stale = false
		for _, source := range p.Sources {
			d, ok := allowed[source.DocumentID]
			if !ok {
				visible = false
				break
			}
			if SourceVersion(d) != source.Version {
				p.Stale = true
			}
		}
		if visible {
			result = append(result, p)
		}
	}
	return result, nil
}
func (s *Service) ReadWiki(ctx context.Context, user, workspace, pageID string) (WikiPage, error) {
	s.wikiRuntime.reads.Add(1)
	pages, err := s.ListWiki(ctx, user, workspace)
	if err != nil {
		return WikiPage{}, err
	}
	for _, p := range pages {
		if p.ID == pageID {
			if p.Stale {
				return WikiPage{}, ErrWikiConflict
			}
			return p, nil
		}
	}
	return WikiPage{}, ErrNotFound
}

type WikiSearchHit struct {
	Page  WikiPage `json:"page"`
	Score float64  `json:"score"`
}
type WikiSearchResult struct {
	Hits     []WikiSearchHit `json:"hits"`
	CacheHit bool            `json:"cache_hit"`
	Omitted  int             `json:"omitted"`
	Tokens   int             `json:"tokens"`
	Degraded bool            `json:"degraded"`
}

func (s *Service) SearchWiki(ctx context.Context, user, workspace, query string, limit, budget int) (WikiSearchResult, error) {
	s.wikiRuntime.searches.Add(1)
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 || len([]rune(query)) > 1000 {
		return WikiSearchResult{}, ErrValidation
	}
	limit = min(max(limit, 1), 8)
	budget = min(max(budget, 128), 6000)
	pages, err := s.ListWiki(ctx, user, workspace)
	if err != nil {
		return WikiSearchResult{}, err
	}
	// Cache keys incorporate ALL accessible page versions/dependencies, so newly
	// added, edited, deleted, or unshared evidence invalidates both hits and misses.
	sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
	type pageVersion struct {
		ID      string
		Version int
		Sources []WikiSource
		Stale   bool
	}
	versions := make([]pageVersion, 0, len(pages))
	for _, p := range pages {
		versions = append(versions, pageVersion{p.ID, p.Version, p.Sources, p.Stale})
	}
	fingerprint, _ := json.Marshal(versions)
	key := fmt.Sprintf("%s:%s:%s:%d:%d:%x", user, workspace, query, limit, budget, sha256.Sum256(fingerprint))
	w := s.wikiRuntime
	w.mu.Lock()
	cached, ok := w.cache[key]
	w.mu.Unlock()
	if ok && cached.expires.After(s.now()) {
		w.hits.Add(1)
		res := cloneWikiResult(cached.result)
		res.CacheHit = true
		return res, nil
	}
	w.misses.Add(1)
	candidates := []WikiSearchHit{}
	for _, p := range pages {
		if p.Stale {
			continue
		}
		score := float64(tokenOverlap(query, p.Title))*3 + float64(tokenOverlap(query, p.Body))
		if score > 0 {
			candidates = append(candidates, WikiSearchHit{p, score})
		}
	}
	// Dense recall admits multilingual/paraphrased evidence before optional rerank.
	degraded := false
	if s.semantic.EmbeddingsEnabled() && len(pages) > 0 {
		densePages := []WikiPage{}
		for _, p := range pages {
			if !p.Stale && len(densePages) < 200 {
				densePages = append(densePages, p)
			}
		}
		scores, e := s.wikiDenseScores(ctx, query, densePages)
		if e != nil {
			degraded = true
		} else {
			byID := map[string]int{}
			for i, h := range candidates {
				byID[h.Page.ID] = i
			}
			for _, p := range densePages {
				score := scores[p.ID]
				if score < .5 {
					continue
				}
				if j, ok := byID[p.ID]; ok {
					candidates[j].Score += score * 4
				} else {
					candidates = append(candidates, WikiSearchHit{p, score * 4})
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if len(candidates) > 24 {
		candidates = candidates[:24]
	}
	if s.semantic.RerankEnabled() && len(candidates) > 0 {
		docs := []string{}
		for _, h := range candidates {
			docs = append(docs, h.Page.Title+"\n"+truncateRunes(h.Page.Body, 1600))
		}
		scores, e := s.semantic.Rerank(ctx, query, docs)
		if e != nil {
			degraded = true
		} else {
			for i := range candidates {
				candidates[i].Score = scores[i]
			}
			sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
		}
	}
	res := WikiSearchResult{Hits: []WikiSearchHit{}, Degraded: degraded}
	for _, h := range candidates {
		h.Page.Body = truncateRunes(h.Page.Body, 500)
		h.Page.Evidence = nil
		encoded, _ := json.Marshal(h)
		cost := contextengine.EstimateTokens(string(encoded))
		if len(res.Hits) >= limit || res.Tokens+cost > budget {
			res.Omitted++
			continue
		}
		res.Hits = append(res.Hits, h)
		res.Tokens += cost
	}
	w.mu.Lock()
	if len(w.cache) >= 256 {
		w.cache = map[string]wikiCacheEntry{}
	}
	w.cache[key] = wikiCacheEntry{cloneWikiResult(res), s.now().Add(2 * time.Minute)}
	w.mu.Unlock()
	return res, nil
}
func cloneWikiResult(r WikiSearchResult) WikiSearchResult {
	b, _ := json.Marshal(r)
	var out WikiSearchResult
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Service) FollowWikiLinks(ctx context.Context, user, workspace, pageID string, limit, budget int) ([]WikiPage, error) {
	page, err := s.ReadWiki(ctx, user, workspace, pageID)
	if err != nil {
		return nil, err
	}
	result := []WikiPage{}
	used := 0
	for _, link := range page.Links {
		if len(result) >= min(max(limit, 1), 4) {
			break
		}
		p, e := s.ReadWiki(ctx, user, workspace, link)
		if errors.Is(e, ErrNotFound) || errors.Is(e, ErrWikiConflict) {
			continue
		}
		if e != nil {
			return nil, e
		}
		data, _ := json.Marshal(p)
		cost := contextengine.EstimateTokens(string(data))
		if used+cost > min(max(budget, 128), 6000) {
			continue
		}
		used += cost
		result = append(result, p)
	}
	return result, nil
}
func (s *Service) UpdateWiki(ctx context.Context, user, pageID string, version int, title, body string) (WikiPage, error) {
	pages, err := s.ListWiki(ctx, user, "")
	if err != nil {
		return WikiPage{}, err
	}
	for _, p := range pages {
		if p.ID == pageID && p.OwnerID == user {
			if version != p.Version {
				return WikiPage{}, ErrWikiConflict
			}
			if len([]rune(title)) < 1 || len([]rune(title)) > 160 || len(body) > 24000 {
				return WikiPage{}, ErrValidation
			}
			p.Title = title
			p.Body = body
			p.Edited = true
			p.UpdatedAt = s.now().UTC()
			return s.store.(WikiStore).SaveWikiPage(ctx, p, version)
		}
	}
	return WikiPage{}, ErrNotFound
}
func (s *Service) WikiHistory(ctx context.Context, user, pageID string) ([]WikiPage, error) {
	current, err := s.ReadWiki(ctx, user, "", pageID)
	if err != nil {
		return nil, err
	}
	versions, err := s.store.(WikiStore).WikiVersions(ctx, user, pageID)
	if err != nil {
		return nil, err
	}
	// Old revisions can cite subsequently deleted sources. Revalidate every one.
	docs, err := s.wikiDocuments(ctx, user, "")
	if err != nil {
		return nil, err
	}
	allowed := map[string]string{}
	for _, d := range docs {
		if d.Status == "ready" {
			allowed[d.ID] = SourceVersion(d)
		}
	}
	result := []WikiPage{}
	for _, p := range versions {
		valid := p.OwnerID == current.OwnerID
		for _, source := range p.Sources {
			if allowed[source.DocumentID] != source.Version {
				valid = false
			}
		}
		if valid {
			result = append(result, p)
		}
	}
	return result, nil
}
func (s *Service) RebuildWiki(ctx context.Context, user, documentID string) error {
	d, err := s.Get(ctx, user, documentID)
	if err != nil {
		return err
	}
	if d.Status != "ready" {
		return ErrValidation
	}
	store, ok := s.store.(WikiStore)
	if !ok {
		return ErrValidation
	}
	return store.EnqueueWiki(ctx, user, documentID, s.now().UTC())
}

// ResetWiki explicitly releases a manual edit for regeneration. Automatic
// source jobs alone never overwrite an editor's version.
func (s *Service) ResetWiki(ctx context.Context, user, pageID string, version int) error {
	pages, err := s.ListWiki(ctx, user, "")
	if err != nil {
		return err
	}
	for _, p := range pages {
		if p.ID == pageID && p.OwnerID == user {
			if p.Version != version {
				return ErrWikiConflict
			}
			p.Edited = false
			p.UpdatedAt = s.now().UTC()
			if _, err = s.store.(WikiStore).SaveWikiPage(ctx, p, version); err != nil {
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}
func (s *Service) FeedbackWiki(ctx context.Context, user, pageID string, version int, rating, note string) error {
	page, err := s.ReadWiki(ctx, user, "", pageID)
	if err != nil {
		return err
	}
	if page.Version != version {
		return ErrWikiConflict
	}
	if rating != "helpful" && rating != "incorrect" && rating != "outdated" {
		return ErrValidation
	}
	if len([]rune(note)) > 1000 {
		return ErrValidation
	}
	feedbackID, err := id.New()
	if err != nil {
		return err
	}
	err = s.store.(WikiStore).SaveWikiFeedback(ctx, user, WikiFeedback{feedbackID, pageID, version, rating, note, s.now().UTC()})
	return err
}
