package semantic

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrProjectionLease = errors.New("knowledge projection lease lost")

// Projection binds an immutable fragment to the authoritative source revision.
// Only background work embeds Text; reads never schedule or embed source text.
type Projection struct {
	Key      string    `json:"key"`
	Owner    string    `json:"owner"`
	Kind     string    `json:"kind"`
	SourceID string    `json:"source_id"`
	Revision string    `json:"revision"`
	Part     int       `json:"part"`
	Spec     string    `json:"spec"`
	Text     string    `json:"text"`
	Vector   []float32 `json:"vector,omitempty"`
	Lease    string    `json:"-"`
	Attempts int       `json:"-"`
}
type ProjectionStore interface {
	Queue(context.Context, []Projection, time.Time) error
	Lookup(context.Context, []string) (map[string][]float32, error)
	ReadyKeys(context.Context, []string) (map[string]bool, error)
	States(context.Context, []string) (map[string]string, error)
	Claim(context.Context, string, time.Time) (Projection, bool, error)
	Finish(context.Context, Projection, []float32, string, time.Time) error
	Renew(context.Context, Projection, time.Time) error
	DeleteSource(context.Context, string, string, string) error
	Prune(context.Context, time.Time, func(context.Context, []Projection) error) (int64, error)
	Stats(context.Context) (map[string]int64, error)
	Retry(context.Context) (int64, error)
	Rebuild(context.Context, []string) (int64, error)
}

// ProjectionIndex stores only derived vectors and source identifiers. Authority
// and ready-state filters are resolved in SQL before every ANN query.
type ProjectionIndex interface {
	Identity() string
	Upsert(context.Context, Projection, []float32) error
	Search(context.Context, []float32, []Projection) (map[string]float64, error)
	Delete(context.Context, []Projection) error
}

func (c *Client) SetProjectionIndex(index ProjectionIndex) { c.projectionIndex = index }
func (c *Client) PruneProjections(ctx context.Context, before time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var cleanup func(context.Context, []Projection) error
	if c.projectionIndex != nil {
		cleanup = c.projectionIndex.Delete
	}
	return c.projections.Prune(ctx, before, cleanup)
}
func (c *Client) ProjectionSpec() string {
	if c != nil && c.projectionIndex != nil {
		return c.Version() + "-ann-" + digest(c.projectionIndex.Identity())[:12]
	}
	return c.Version()
}

type ProjectionStoreProvider interface{ KnowledgeProjectionStore() ProjectionStore }

func (c *Client) SetProjectionStore(store ProjectionStore) {
	if store != nil {
		c.projections = store
	}
}
func (c *Client) ProjectionStore() ProjectionStore {
	if c == nil {
		return nil
	}
	return c.projections
}

func (c *Client) SourceProjections(owner, kind, source, revision, text string) []Projection {
	if !c.EmbeddingsEnabled() {
		return nil
	}
	runes := []rune(text)
	out := []Projection{}
	for start, part := 0, 0; start < len(runes); start, part = start+1200, part+1 {
		p := Projection{Owner: owner, Kind: kind, SourceID: source, Revision: revision, Part: part, Spec: c.ProjectionSpec(), Text: string(runes[start:min(start+1400, len(runes))])}
		p.Key = digest(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s", owner, kind, source, revision, p.Spec, part, p.Text))
		out = append(out, p)
	}
	return out
}
func (c *Client) QueueSource(ctx context.Context, owner, kind, source, revision, text string) error {
	if !c.EmbeddingsEnabled() {
		return nil
	}
	return c.projections.Queue(ctx, c.SourceProjections(owner, kind, source, revision, text), time.Now().UTC())
}

func (c *Client) StoredScores(ctx context.Context, query string, items []Projection) (map[string]float64, bool, error) {
	scores := map[string]float64{}
	if !c.EmbeddingsEnabled() || len(items) == 0 {
		return scores, false, nil
	}
	keys := make([]string, 0, len(items))
	for _, p := range items {
		keys = append(keys, p.Key)
	}
	if c.projectionIndex != nil {
		ready, err := c.projections.ReadyKeys(ctx, keys)
		if err != nil {
			return scores, true, err
		}
		missing := len(ready) < len(items)
		if len(ready) == 0 {
			return scores, missing, nil
		}
		candidates := []Projection{}
		for _, p := range items {
			if ready[p.Key] {
				candidates = append(candidates, p)
			}
		}
		q, err := c.EmbedQuery(ctx, query)
		if err != nil {
			return scores, true, err
		}
		scores, err = c.projectionIndex.Search(ctx, q[0], candidates)
		return scores, missing || err != nil, err
	}
	stored, err := c.projections.Lookup(ctx, keys)
	if err != nil {
		return scores, true, err
	}
	missing := len(stored) < len(items)
	if len(stored) == 0 {
		return scores, missing, nil
	}
	q, err := c.EmbedQuery(ctx, query)
	if err != nil {
		return scores, true, err
	}
	for _, p := range items {
		if v, ok := stored[p.Key]; ok {
			score := Cosine(q[0], v)
			if old, exists := scores[p.SourceID]; !exists || score > old {
				scores[p.SourceID] = score
			}
		}
	}
	return scores, missing, nil
}

// ProcessProjection verifies authority both before sending text to the model
// and before publication. A cancelled or expired lease cannot publish.
func (c *Client) ProcessProjection(ctx context.Context, valid func(context.Context, Projection) (bool, error)) (bool, error) {
	if !c.EmbeddingsEnabled() {
		return false, nil
	}
	p, found, err := c.projections.Claim(ctx, c.ProjectionSpec(), time.Now().UTC())
	if err != nil || !found {
		return found, err
	}
	fail := func(code string, cause error) (bool, error) {
		if code == "obsolete" && c.projectionIndex != nil {
			if err := c.projectionIndex.Delete(ctx, []Projection{p}); err != nil {
				code = "vector_cleanup_unavailable"
				cause = errors.Join(cause, err)
			}
		}
		e := c.projections.Finish(ctx, p, nil, code, time.Now().UTC())
		return true, errors.Join(cause, e)
	}
	ok, err := valid(ctx, p)
	if err != nil {
		return fail("validation_unavailable", err)
	}
	if !ok {
		return fail("obsolete", nil)
	}
	v, err := c.Embed(ctx, []string{p.Text})
	if err != nil {
		return fail("embedding_unavailable", err)
	}
	ok, err = valid(ctx, p)
	if err != nil {
		return fail("validation_unavailable", err)
	}
	if !ok {
		return fail("obsolete", nil)
	}
	if c.projectionIndex != nil {
		if err := c.projectionIndex.Upsert(ctx, p, v[0]); err != nil {
			return fail("vector_index_unavailable", err)
		}
		ok, err = valid(ctx, p)
		if err != nil {
			return fail("validation_unavailable", err)
		}
		if !ok {
			return fail("obsolete", nil)
		}
	}
	return true, c.projections.Finish(ctx, p, v[0], "", time.Now().UTC())
}
