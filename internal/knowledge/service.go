// Package knowledge defines the common evidence contract without importing domains.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/windcry1/ai-companion/internal/contextengine"
	"sort"
	"strings"
	"time"
)

var ErrValidation = errors.New("invalid knowledge request")
var ErrForbidden = errors.New("knowledge scope forbidden")
var ErrNotFound = errors.New("knowledge source unavailable")
var ErrConflict = errors.New("knowledge revision changed")

const Version = "knowledge-evidence-v1"

type Scope struct{ UserID, WorkspaceID, Module string }
type Locator struct {
	PageStart int    `json:"page_start,omitempty"`
	PageEnd   int    `json:"page_end,omitempty"`
	Section   string `json:"section,omitempty"`
	RowID     string `json:"row_id,omitempty"`
	ColumnID  string `json:"column_id,omitempty"`
	RegionID  string `json:"region_id,omitempty"`
}
type Ref struct {
	Kind        string  `json:"kind"`
	SourceID    string  `json:"source_id"`
	Revision    string  `json:"source_revision"`
	FragmentID  string  `json:"fragment_id,omitempty"`
	ContentHash string  `json:"content_hash"`
	Locator     Locator `json:"locator,omitempty"`
}
type Item struct {
	Ref                 Ref        `json:"ref"`
	Title               string     `json:"title"`
	Content             string     `json:"content"`
	Score               float64    `json:"score"`
	CitationURL         string     `json:"citation_url,omitempty"`
	EvidenceState       string     `json:"evidence_state"`
	Dependencies        []Ref      `json:"dependencies,omitempty"`
	DependenciesOmitted int        `json:"dependencies_omitted,omitempty"`
	Truncated           bool       `json:"truncated,omitempty"`
	ValidFrom           *time.Time `json:"valid_from,omitempty"`
	ValidTo             *time.Time `json:"valid_to,omitempty"`
}
type Query struct {
	Query       string     `json:"query"`
	Kinds       []string   `json:"source_kinds,omitempty"`
	DocumentIDs []string   `json:"document_ids,omitempty"`
	WorkspaceID string     `json:"workspace_id,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	AsOf        *time.Time `json:"as_of,omitempty"`
	Limit       int        `json:"limit,omitempty"`
	Budget      int        `json:"budget,omitempty"`
}
type Batch struct {
	Items     []Item
	Degraded  []string
	IndexSpec string
}
type Provider interface {
	Search(context.Context, Scope, Query) (Batch, error)
	Read(context.Context, Scope, Ref) (Item, error)
}
type Manifest struct {
	Version       string            `json:"version"`
	Strategy      string            `json:"strategy"`
	Scope         string            `json:"scope"`
	Candidates    map[string]int    `json:"candidates"`
	IndexSpecs    map[string]string `json:"index_specs"`
	Degraded      []string          `json:"degraded"`
	Omitted       int               `json:"omitted"`
	Tokens        int               `json:"estimated_tokens"`
	LatencyMS     int64             `json:"latency_ms"`
	EvidenceState string            `json:"evidence_state"`
}
type Result struct {
	Hits     []Item   `json:"hits"`
	Manifest Manifest `json:"manifest"`
}
type ReadInput struct {
	Ref         Ref    `json:"ref"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Offset      int    `json:"offset,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}
type ReadResult struct {
	Item       Item `json:"item"`
	Offset     int  `json:"offset"`
	NextOffset int  `json:"next_offset"`
	HasMore    bool `json:"has_more"`
	Total      int  `json:"total_characters"`
}
type Evidence struct {
	Ref   Ref    `json:"ref"`
	Quote string `json:"quote"`
}
type Service struct {
	feedback  FeedbackStore
	providers map[string]Provider
	authorize func(context.Context, Scope) error
}

func New(authorize func(context.Context, Scope) error) *Service {
	return &Service{providers: map[string]Provider{}, authorize: authorize, feedback: &MemoryFeedbackStore{}}
}
func (s *Service) Register(kind string, p Provider) { s.providers[kind] = p }
func Hash(text string) string                       { sum := sha256.Sum256([]byte(text)); return hex.EncodeToString(sum[:]) }
func (s *Service) allowed(ctx context.Context, scope Scope, kind string) error {
	if scope.UserID == "" {
		return ErrForbidden
	}
	if scope.Module != "work" && scope.Module != "companion" && scope.Module != "life" {
		return ErrForbidden
	}
	if scope.WorkspaceID != "" {
		if scope.Module != "work" || kind == "memory" {
			return ErrForbidden
		}
		if s.authorize == nil {
			return ErrForbidden
		}
	}
	if (kind == "document" || kind == "wiki") && scope.Module != "work" {
		return ErrForbidden
	}
	if s.providers[kind] == nil {
		return ErrValidation
	}
	if s.authorize != nil {
		return s.authorize(ctx, scope)
	}
	return nil
}
func (s *Service) Search(ctx context.Context, scope Scope, q Query) (Result, error) {
	start := time.Now()
	q.Query = strings.TrimSpace(q.Query)
	if n := len([]rune(q.Query)); n < 2 || n > 1000 {
		return Result{}, ErrValidation
	}
	if len(q.DocumentIDs) > 100 || q.Limit < 0 || q.Limit > 12 || q.Budget < 0 || q.Budget > 6000 || q.AsOf != nil || (q.Mode != "" && q.Mode != "lookup") {
		return Result{}, ErrValidation
	}
	if q.WorkspaceID != scope.WorkspaceID {
		return Result{}, ErrForbidden
	}
	if q.Limit == 0 {
		q.Limit = 8
	}
	if q.Budget == 0 {
		q.Budget = 3600
	}
	if q.Budget < 256 {
		return Result{}, ErrValidation
	}
	kinds := q.Kinds
	if len(kinds) == 0 {
		kinds = []string{"builtin"}
		if scope.WorkspaceID == "" {
			kinds = append(kinds, "memory")
		}
		if scope.Module == "work" {
			kinds = append(kinds, "document", "wiki")
		}
		if len(q.DocumentIDs) > 0 {
			kinds = []string{"document", "wiki"}
		}
	}
	if len(kinds) > 4 {
		return Result{}, ErrValidation
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		if len(q.DocumentIDs) > 0 && kind != "document" && kind != "wiki" {
			return Result{}, ErrValidation
		}
		if seen[kind] {
			return Result{}, ErrValidation
		}
		seen[kind] = true
		if err := s.allowed(ctx, scope, kind); err != nil {
			return Result{}, err
		}
	}
	out := Result{Hits: []Item{}, Manifest: Manifest{Version: Version, Strategy: "scoped-rrf-v1", Scope: "personal", Candidates: map[string]int{}, IndexSpecs: map[string]string{}, Degraded: []string{}, EvidenceState: "insufficient"}}
	if scope.WorkspaceID != "" {
		out.Manifest.Scope = "workspace"
	}
	candidates := []Item{}
	for _, kind := range kinds {
		batch, err := s.providers[kind].Search(ctx, scope, q)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			out.Manifest.Degraded = append(out.Manifest.Degraded, kind+"_unavailable")
			continue
		}
		out.Manifest.Candidates[kind] = len(batch.Items)
		out.Manifest.IndexSpecs[kind] = batch.IndexSpec
		out.Manifest.Degraded = append(out.Manifest.Degraded, batch.Degraded...)
		for rank, item := range batch.Items {
			if item.Ref.Kind != kind {
				out.Manifest.Degraded = append(out.Manifest.Degraded, kind+"_invalid_reference")
				continue
			}
			// Re-read authoritative content, including scope and revision, after retrieval.
			current, err := s.read(ctx, scope, item.Ref)
			if err != nil {
				out.Manifest.Omitted++
				continue
			}
			current.Score = 1 / float64(60+rank+1)
			current.Ref.ContentHash = Hash(current.Content)
			if current.EvidenceState == "" {
				current.EvidenceState = "unassessed"
			}
			candidates = append(candidates, current)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return refKey(candidates[i].Ref) < refKey(candidates[j].Ref)
		}
		return candidates[i].Score > candidates[j].Score
	})
	dedup := map[string]bool{}
	for _, item := range candidates {
		key := refKey(item.Ref)
		if dedup[key] {
			out.Manifest.Omitted++
			continue
		}
		dedup[key] = true
		runes := []rune(item.Content)
		if len(runes) > 600 {
			item.Content = string(runes[:600])
			item.Truncated = true
		}
		if len(item.Dependencies) > 12 {
			item.DependenciesOmitted = len(item.Dependencies) - 12
			item.Dependencies = item.Dependencies[:12]
		}
		encoded, _ := json.Marshal(item)
		cost := contextengine.EstimateTokens(string(encoded)) + 8
		if len(out.Hits) >= q.Limit || out.Manifest.Tokens+cost > q.Budget-256 {
			out.Manifest.Omitted++
			continue
		}
		out.Hits = append(out.Hits, item)
		out.Manifest.Tokens += cost
		if item.EvidenceState == "disputed" {
			out.Manifest.EvidenceState = "disputed"
		} else if out.Manifest.EvidenceState == "insufficient" {
			out.Manifest.EvidenceState = "unassessed"
		}
	}
	if len(out.Hits) == 0 && len(out.Manifest.Degraded) > 0 {
		out.Manifest.EvidenceState = "unknown"
	}
	out.Manifest.LatencyMS = time.Since(start).Milliseconds()
	return out, nil
}
func refKey(r Ref) string { return r.Kind + ":" + r.SourceID + ":" + r.Revision + ":" + r.FragmentID }
func (s *Service) read(ctx context.Context, scope Scope, ref Ref) (Item, error) {
	if ref.SourceID == "" || len(ref.SourceID) > 160 || len(ref.FragmentID) > 160 || len(ref.Revision) > 160 {
		return Item{}, ErrValidation
	}
	if err := s.allowed(ctx, scope, ref.Kind); err != nil {
		return Item{}, err
	}
	item, err := s.providers[ref.Kind].Read(ctx, scope, ref)
	if err != nil {
		return Item{}, err
	}
	item.Ref.ContentHash = Hash(item.Content)
	if ref.Revision != "" && item.Ref.Revision != ref.Revision {
		return Item{}, ErrConflict
	}
	if ref.ContentHash != "" && item.Ref.ContentHash != ref.ContentHash {
		return Item{}, ErrConflict
	}
	return item, nil
}
func (s *Service) Read(ctx context.Context, scope Scope, in ReadInput) (ReadResult, error) {
	if in.WorkspaceID != scope.WorkspaceID || in.Offset < 0 || in.Limit < 0 || in.Limit > 2400 {
		return ReadResult{}, ErrValidation
	}
	item, err := s.read(ctx, scope, in.Ref)
	if err != nil {
		return ReadResult{}, err
	}
	runes := []rune(item.Content)
	if in.Offset > len(runes) {
		return ReadResult{}, ErrValidation
	}
	limit := in.Limit
	if limit == 0 {
		limit = 2400
	}
	end := min(in.Offset+limit, len(runes))
	item.Content = string(runes[in.Offset:end])
	item.Truncated = end < len(runes) || in.Offset > 0
	if len(item.Dependencies) > 12 {
		item.DependenciesOmitted = len(item.Dependencies) - 12
		item.Dependencies = item.Dependencies[:12]
	}
	return ReadResult{item, in.Offset, end, end < len(runes), len(runes)}, nil
}
func (s *Service) Validate(ctx context.Context, scope Scope, refs []Evidence) error {
	if len(refs) == 0 || len(refs) > 48 {
		return ErrValidation
	}
	encoded, _ := json.Marshal(refs)
	if len(encoded) > 96000 {
		return ErrValidation
	}
	for _, e := range refs {
		if e.Ref.Revision == "" || e.Ref.ContentHash == "" || strings.TrimSpace(e.Quote) == "" {
			return ErrValidation
		}
		item, err := s.read(ctx, scope, e.Ref)
		if err != nil {
			return err
		}
		if !strings.Contains(item.Content, e.Quote) {
			return fmt.Errorf("%w: quote is not in source", ErrConflict)
		}
	}
	return nil
}
func (s *Service) Trace(ctx context.Context, scope Scope, ref Ref) ([]Ref, error) {
	item, err := s.read(ctx, scope, ref)
	if err != nil {
		return nil, err
	}
	out := []Ref{item.Ref}
	for _, dep := range item.Dependencies {
		current, err := s.read(ctx, scope, dep)
		if err != nil {
			return nil, err
		}
		out = append(out, current.Ref)
		if len(out) >= 49 {
			break
		}
	}
	return out, nil
}
