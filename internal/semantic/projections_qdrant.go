package semantic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

type QdrantProjectionIndex struct {
	base, root, key string
	dimensions      int
	client          *http.Client
	mu              sync.Mutex
	ready           map[string]bool
}

func NewQdrantProjectionIndex(base, root, key string, dimensions int) *QdrantProjectionIndex {
	return &QdrantProjectionIndex{base: strings.TrimRight(base, "/"), root: root, key: key, dimensions: dimensions, client: &http.Client{Timeout: 15 * time.Second}, ready: map[string]bool{}}
}
func (q *QdrantProjectionIndex) Identity() string {
	u, _ := url.Parse(q.base)
	if u == nil {
		return q.root
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String() + ":" + q.root + ":ann-v1"
}
func (q *QdrantProjectionIndex) collection(spec string) string {
	return q.root + "_knowledge_" + digest(spec)[:24]
}
func projectionPointID(key string) string {
	h := digest(key)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
func (q *QdrantProjectionIndex) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, q.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if q.key != "" {
		req.Header.Set("api-key", q.key)
	}
	tracectx.InjectHTTP(ctx, req.Header)
	r, err := q.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return r.StatusCode, fmt.Errorf("knowledge vector index returned %d", r.StatusCode)
	}
	if out != nil {
		err = json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(out)
	}
	return r.StatusCode, err
}
func (q *QdrantProjectionIndex) ensure(ctx context.Context, spec string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	name := q.collection(spec)
	if q.ready[name] {
		return nil
	}
	path := "/collections/" + url.PathEscape(name)
	status, err := q.call(ctx, http.MethodGet, path, nil, nil)
	if status == 404 {
		status, err = q.call(ctx, http.MethodPut, path, map[string]any{"vectors": map[string]any{"size": q.dimensions, "distance": "Cosine"}, "metadata": map[string]any{"source": "ai-companion-knowledge", "spec": spec}}, nil)
	}
	if status == http.StatusConflict {
		err = nil
	}
	if err != nil {
		return err
	}
	for _, field := range []string{"projection_key", "source_id"} {
		if _, err = q.call(ctx, http.MethodPut, path+"/index?wait=true", map[string]any{"field_name": field, "field_schema": "keyword"}, nil); err != nil {
			return err
		}
	}
	q.ready[name] = true
	return nil
}
func (q *QdrantProjectionIndex) Upsert(ctx context.Context, p Projection, vector []float32) error {
	if err := q.ensure(ctx, p.Spec); err != nil {
		return err
	}
	status, err := q.call(ctx, http.MethodPut, "/collections/"+url.PathEscape(q.collection(p.Spec))+"/points?wait=true", map[string]any{"points": []any{map[string]any{"id": projectionPointID(p.Key), "vector": vector, "payload": map[string]any{"projection_key": p.Key, "source_id": p.SourceID, "owner_id": p.Owner, "kind": p.Kind, "revision": p.Revision}}}}, nil)
	if status == 404 {
		q.mu.Lock()
		delete(q.ready, q.collection(p.Spec))
		q.mu.Unlock()
	}
	return err
}
func (q *QdrantProjectionIndex) Search(ctx context.Context, vector []float32, items []Projection) (map[string]float64, error) {
	out := map[string]float64{}
	if len(items) == 0 {
		return out, nil
	}
	spec := items[0].Spec
	keys := []string{}
	allowed := map[string]string{}
	for _, p := range items {
		if p.Spec != spec {
			return nil, fmt.Errorf("mixed projection specifications")
		}
		keys = append(keys, p.Key)
		allowed[p.Key] = p.SourceID
	}
	var result struct {
		Result struct {
			Groups []struct {
				Hits []struct {
					Score   float64 `json:"score"`
					Payload struct {
						Key    string `json:"projection_key"`
						Source string `json:"source_id"`
					} `json:"payload"`
				} `json:"hits"`
			} `json:"groups"`
		} `json:"result"`
	}
	_, err := q.call(ctx, http.MethodPost, "/collections/"+url.PathEscape(q.collection(spec))+"/points/query/groups", map[string]any{"query": vector, "group_by": "source_id", "group_size": 1, "limit": 24, "with_payload": true, "filter": map[string]any{"must": []any{map[string]any{"key": "projection_key", "match": map[string]any{"any": keys}}}}}, &result)
	if err != nil {
		return nil, err
	}
	for _, g := range result.Result.Groups {
		for _, hit := range g.Hits {
			if source, ok := allowed[hit.Payload.Key]; ok && source == hit.Payload.Source {
				if score, exists := out[source]; !exists || hit.Score > score {
					out[source] = hit.Score
				}
			}
		}
	}
	return out, nil
}

// Deletion also targets retired model collections derived from the stored spec.
// Payloads contain no source text; only projections owned by this namespace are removed.
func (q *QdrantProjectionIndex) Delete(ctx context.Context, items []Projection) error {
	bySpec := map[string][]string{}
	for _, p := range items {
		if strings.Contains(p.Spec, "-ann-") {
			bySpec[p.Spec] = append(bySpec[p.Spec], projectionPointID(p.Key))
		}
	}
	for spec, ids := range bySpec {
		status, err := q.call(ctx, http.MethodPost, "/collections/"+url.PathEscape(q.collection(spec))+"/points/delete?wait=true", map[string]any{"points": ids}, nil)
		if err != nil && status != 404 {
			return err
		}
	}
	return nil
}
