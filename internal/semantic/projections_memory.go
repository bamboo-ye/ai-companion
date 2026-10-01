package semantic

import (
	"context"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"sort"
	"sync"
	"time"
)

type projectionState struct {
	p                        Projection
	state                    string
	available, expires, seen time.Time
}
type MemoryProjectionStore struct {
	mu    sync.Mutex
	items map[string]projectionState
}

func NewMemoryProjectionStore() *MemoryProjectionStore {
	return &MemoryProjectionStore{items: map[string]projectionState{}}
}
func (s *MemoryProjectionStore) Queue(_ context.Context, items []Projection, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range items {
		v, ok := s.items[p.Key]
		if !ok || v.seen.Before(now.Add(-24*time.Hour)) {
			v = projectionState{p: p, state: "queued", available: now}
		}
		v.seen = now
		s.items[p.Key] = v
	}
	return nil
}
func (s *MemoryProjectionStore) Lookup(_ context.Context, keys []string) (map[string][]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]float32{}
	for _, key := range keys {
		if v, ok := s.items[key]; ok && v.state == "ready" {
			out[key] = append([]float32(nil), v.p.Vector...)
		}
	}
	return out, nil
}
func (s *MemoryProjectionStore) ReadyKeys(_ context.Context, keys []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for _, key := range keys {
		if v, ok := s.items[key]; ok && v.state == "ready" {
			out[key] = true
		}
	}
	return out, nil
}
func (s *MemoryProjectionStore) States(_ context.Context, keys []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for _, key := range keys {
		if v, ok := s.items[key]; ok {
			out[key] = v.state
		}
	}
	return out, nil
}
func (s *MemoryProjectionStore) Claim(_ context.Context, spec string, now time.Time) (Projection, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := []string{}
	for key := range s.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		v := s.items[key]
		if v.p.Spec != spec || v.available.After(now) || !(v.state == "queued" || (v.state == "processing" && !v.expires.After(now))) {
			continue
		}
		token, err := id.New()
		if err != nil {
			return Projection{}, false, err
		}
		v.p.Lease = token
		v.p.Attempts++
		v.state = "processing"
		v.expires = now.Add(2 * time.Minute)
		s.items[key] = v
		return v.p, true, nil
	}
	return Projection{}, false, nil
}
func (s *MemoryProjectionStore) Finish(_ context.Context, p Projection, vector []float32, code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[p.Key]
	if !ok || v.state != "processing" || v.p.Lease != p.Lease || !v.expires.After(now) {
		return ErrProjectionLease
	}
	if code == "obsolete" {
		delete(s.items, p.Key)
		return nil
	}
	v.state = "ready"
	v.p.Vector = append([]float32(nil), vector...)
	if code != "" {
		v.state = "queued"
		v.available = now.Add(time.Duration(1<<min(p.Attempts, 8)) * time.Second)
		if p.Attempts >= 5 {
			v.state = "failed"
		}
	}
	v.p.Lease = ""
	s.items[p.Key] = v
	return nil
}
func (s *MemoryProjectionStore) Renew(_ context.Context, p Projection, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.items[p.Key]
	if !ok || v.state != "processing" || v.p.Lease != p.Lease || !v.expires.After(now) {
		return ErrProjectionLease
	}
	v.expires = now.Add(2 * time.Minute)
	s.items[p.Key] = v
	return nil
}
func (s *MemoryProjectionStore) DeleteSource(_ context.Context, kind, owner, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.items {
		if v.p.Kind == kind && v.p.Owner == owner && (source == "" || v.p.SourceID == source) {
			delete(s.items, k)
		}
	}
	return nil
}
func (s *MemoryProjectionStore) Prune(ctx context.Context, before time.Time, cleanup func(context.Context, []Projection) error) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, v := range s.items {
		if v.seen.Before(before) && (v.state != "processing" || v.expires.Before(before)) {
			if cleanup != nil {
				if err := cleanup(ctx, []Projection{v.p}); err != nil {
					return n, err
				}
			}
			delete(s.items, k)
			n++
		}
	}
	return n, nil
}
func (s *MemoryProjectionStore) Stats(_ context.Context) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int64{}
	for _, v := range s.items {
		out[v.state]++
	}
	return out, nil
}
func (s *MemoryProjectionStore) Retry(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, v := range s.items {
		if v.state == "failed" {
			v.state = "queued"
			v.p.Attempts = 0
			v.available = time.Now().UTC()
			s.items[k] = v
			n++
		}
	}
	return n, nil
}
func (s *MemoryProjectionStore) Rebuild(_ context.Context, specs []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed := map[string]bool{}
	for _, spec := range specs {
		allowed[spec] = true
	}
	var n int64
	for key, v := range s.items {
		if allowed[v.p.Spec] && v.state != "processing" {
			v.state = "queued"
			v.p.Vector = nil
			v.p.Attempts = 0
			v.available = time.Now().UTC()
			s.items[key] = v
			n++
		}
	}
	return n, nil
}
