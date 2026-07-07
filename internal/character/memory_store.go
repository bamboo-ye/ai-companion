package character

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu       sync.RWMutex
	items    map[string]Character
	versions map[string][]PersonaVersion
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[string]Character{}, versions: map[string][]PersonaVersion{}}
}

func (s *MemoryStore) Create(_ context.Context, character Character, persona PersonaVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[character.ID] = character
	s.versions[character.ID] = []PersonaVersion{persona}
	return nil
}

func (s *MemoryStore) List(_ context.Context, userID string) ([]Character, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Character, 0)
	for _, item := range s.items {
		if item.UserID == userID && item.Status == "active" {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *MemoryStore) Get(_ context.Context, userID, characterID string) (Character, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[characterID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return Character{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) Update(_ context.Context, character Character, persona PersonaVersion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.items[character.ID]
	if !ok || current.UserID != character.UserID {
		return ErrNotFound
	}
	s.items[character.ID] = character
	s.versions[character.ID] = append(s.versions[character.ID], persona)
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, userID, characterID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[characterID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return ErrNotFound
	}
	item.Status = "deleted"
	item.UpdatedAt = now
	s.items[characterID] = item
	return nil
}

func (s *MemoryStore) ListPersonaVersions(_ context.Context, userID, characterID string) ([]PersonaVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[characterID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return nil, ErrNotFound
	}
	result := append([]PersonaVersion(nil), s.versions[characterID]...)
	return result, nil
}
