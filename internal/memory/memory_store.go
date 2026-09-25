package memory

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]Memory
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{items: map[string]Memory{}} }
func (s *MemoryStore) UpsertMemory(_ context.Context, item Memory) (Memory, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.items {
		if existing.UserID == item.UserID && existing.NormalizedHash == item.NormalizedHash && existing.Status == "active" {
			if item.SupersedesID == existing.ID {
				continue
			}
			if item.SupersedesID != "" {
				return item, false, ErrValidation
			}
			return cloneMemory(existing), false, nil
		}
	}
	if item.SupersedesID != "" {
		old, ok := s.items[item.SupersedesID]
		if !ok || old.UserID != item.UserID || old.Status != "active" {
			return item, false, ErrNotFound
		}
		old.Status = "superseded"
		old.ValidTo = &item.ValidFrom
		old.UpdatedAt = item.ValidFrom
		s.items[old.ID] = old
	}
	s.items[item.ID] = cloneMemory(item)
	return item, true, nil
}
func (s *MemoryStore) ListMemories(_ context.Context, userID string, limit int) ([]Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []Memory{}
	for _, item := range s.items {
		if item.UserID == userID && item.Status == "active" {
			items = append(items, cloneMemory(item))
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Pinned != items[j].Pinned {
			return items[i].Pinned
		}
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
func (s *MemoryStore) GetMemory(_ context.Context, userID, memoryID string) (Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[memoryID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return Memory{}, ErrNotFound
	}
	return cloneMemory(item), nil
}
func (s *MemoryStore) UpdateMemory(_ context.Context, item Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.items[item.ID]
	if !ok || existing.UserID != item.UserID || existing.Status != "active" {
		return ErrNotFound
	}
	s.items[item.ID] = cloneMemory(item)
	return nil
}

func cloneMemory(item Memory) Memory {
	if item.Arbitration != nil {
		data, _ := json.Marshal(item.Arbitration)
		item.Arbitration = nil
		_ = json.Unmarshal(data, &item.Arbitration)
	}
	return item
}
func (s *MemoryStore) DeleteMemory(_ context.Context, userID, memoryID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[memoryID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return ErrNotFound
	}
	item.Status = "deleted"
	item.ValidTo = &now
	item.UpdatedAt = now
	s.items[item.ID] = item
	return nil
}
func (s *MemoryStore) ClearMemories(_ context.Context, userID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, item := range s.items {
		if item.UserID == userID && item.Status == "active" {
			item.Status = "deleted"
			item.ValidTo = &now
			item.UpdatedAt = now
			s.items[id] = item
		}
	}
	return nil
}
