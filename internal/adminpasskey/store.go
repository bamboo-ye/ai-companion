package adminpasskey

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrDenied    = errors.New("admin authentication denied")
	ErrConflict  = errors.New("admin authentication state changed")
	ErrRateLimit = errors.New("admin authentication rate limited")
)

// Record stores public credentials or short-lived authentication state. Bearer
// secrets are only used as hashed keys, never stored in Data.
type Record struct {
	Key      string
	Owner    string
	Kind     string
	Data     []byte
	Expires  time.Time
	Revision int64
}

type Store interface {
	Get(context.Context, string) (Record, error)
	List(context.Context, string, string) ([]Record, error)
	Insert(context.Context, Record) error
	Update(context.Context, Record) error         // compare-and-swap using Revision
	Take(context.Context, string) (Record, error) // atomic, single-use consumption
	DeleteOwner(context.Context, string, string) error
	Purge(context.Context, time.Time) error
}

type StoreProvider interface{ AdminPasskeyStore() Store }

type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]Record{}} }
func clone(r Record) Record        { r.Data = append([]byte(nil), r.Data...); return r }
func (s *MemoryStore) Get(_ context.Context, key string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[key]
	if !ok {
		return Record{}, ErrDenied
	}
	return clone(r), nil
}
func (s *MemoryStore) List(_ context.Context, kind, owner string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Record{}
	for _, r := range s.records {
		if r.Kind == kind && r.Owner == owner {
			out = append(out, clone(r))
		}
	}
	return out, nil
}
func (s *MemoryStore) Insert(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[r.Key]; ok {
		return ErrConflict
	}
	r.Revision = 1
	s.records[r.Key] = clone(r)
	return nil
}
func (s *MemoryStore) Update(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.records[r.Key]
	if !ok || old.Revision != r.Revision {
		return ErrConflict
	}
	r.Revision++
	s.records[r.Key] = clone(r)
	return nil
}
func (s *MemoryStore) Take(_ context.Context, key string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[key]
	if !ok {
		return Record{}, ErrDenied
	}
	delete(s.records, key)
	return clone(r), nil
}
func (s *MemoryStore) DeleteOwner(_ context.Context, kind, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.records {
		if r.Kind == kind && r.Owner == owner {
			delete(s.records, k)
		}
	}
	return nil
}
func (s *MemoryStore) Purge(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.records {
		if !r.Expires.After(now) {
			delete(s.records, k)
		}
	}
	return nil
}
