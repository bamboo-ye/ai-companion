package document

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
)

// ScopedVectorIndex also filters immutable source revisions before ANN ranking.
// Empty revisions permit legacy points during migration; authoritative chunks
// are still rechecked before they can leave the document service.
type ScopedVectorIndex interface {
	SearchScoped(context.Context, string, string, map[string]string, int) ([]SearchHit, error)
}

func (q *QdrantIndex) SearchScoped(ctx context.Context, user, query string, versions map[string]string, limit int) ([]SearchHit, error) {
	if len(versions) == 0 {
		return nil, nil
	}
	ids := []string{}
	revisions := []string{}
	for id, v := range versions {
		ids = append(ids, id)
		revisions = append(revisions, v)
	}
	sort.Strings(ids)
	sort.Strings(revisions)
	must := []any{map[string]any{"key": "document_id", "match": map[string]any{"any": ids}}}
	if user != "" {
		must = append(must, map[string]any{"key": "user_id", "match": map[string]any{"value": user}})
	}
	filter := map[string]any{"must": must, "should": []any{map[string]any{"key": "source_revision", "match": map[string]any{"any": revisions}}, map[string]any{"key": "source_revision", "match": map[string]any{"value": ""}}, map[string]any{"is_empty": map[string]any{"key": "source_revision"}}}}
	return q.searchWithFilter(ctx, query, filter, limit)
}
func (r *RolloutIndex) SearchScoped(ctx context.Context, user, query string, versions map[string]string, limit int) ([]SearchHit, error) {
	ids := []string{}
	for id := range versions {
		ids = append(ids, id)
	}
	return r.search(ctx, query, ids, limit, func(index VectorIndex) ([]SearchHit, error) {
		if scoped, ok := index.(ScopedVectorIndex); ok {
			return scoped.SearchScoped(ctx, user, query, versions, limit)
		}
		if user == "" {
			return index.SearchDocuments(ctx, query, ids, limit)
		}
		return index.Search(ctx, user, query, ids, limit)
	})
}
func (s *Service) indexScopeSearch(ctx context.Context, user, query string, allowed map[string]Document, ids []string, limit int) ([]SearchHit, error) {
	if scoped, ok := s.index.(ScopedVectorIndex); ok {
		versions := map[string]string{}
		for id, d := range allowed {
			versions[id] = SourceVersion(d)
		}
		return scoped.SearchScoped(ctx, user, query, versions, limit)
	}
	if user == "" {
		return s.index.SearchDocuments(ctx, query, ids, limit)
	}
	return s.index.Search(ctx, user, query, ids, limit)
}

// Include retired semantic collections when cleaning a deleted source. Discovery
// is limited to this configured root and its exact historical naming convention;
// only owner+document points are removed, never collections or unrelated data.
func (q *QdrantIndex) deleteManagedDocument(ctx context.Context, user, doc string) error {
	status, response, err := q.do(ctx, http.MethodGet, "/collections", nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("list managed vector collections: %d", status)
	}
	var data struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err = json.Unmarshal([]byte(response), &data); err != nil {
		return err
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(q.managedRoot) + "_semantic_[a-f0-9]{12}$")
	for _, entry := range data.Result.Collections {
		if entry.Name != q.managedRoot && !pattern.MatchString(entry.Name) {
			continue
		}
		single := *q
		single.collection = entry.Name
		single.managedRoot = ""
		if err = single.DeleteDocument(ctx, user, doc); err != nil {
			return err
		}
	}
	return nil
}
func indexIdentity(index VectorIndex) string {
	switch v := index.(type) {
	case *QdrantIndex:
		u, _ := url.Parse(v.baseURL)
		base := ""
		if u != nil {
			u.User = nil
			u.RawQuery = ""
			base = u.String()
		}
		return base + ":" + v.collection
	case *RolloutIndex:
		return "dual:" + indexIdentity(v.Legacy) + ":" + indexIdentity(v.Candidate)
	default:
		return fmt.Sprintf("%T", index)
	}
}
func (s *Service) DocumentIndexSpec() string {
	return fmt.Sprintf("document-index-%x", sha256.Sum256([]byte(indexIdentity(s.index))))[:39]
}
