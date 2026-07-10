package safety

import (
	"context"
	"sync"
)

type MemoryStore struct {
	mu       sync.RWMutex
	policies map[string]UserPolicy
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{policies: map[string]UserPolicy{}}
}

func (s *MemoryStore) GetUserPolicy(_ context.Context, userID string) (UserPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy, ok := s.policies[userID]
	if !ok {
		return UserPolicy{}, ErrNotFound
	}
	return policy, nil
}

func (s *MemoryStore) UpsertUserPolicy(_ context.Context, policy UserPolicy) (UserPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[policy.UserID] = policy
	return policy, nil
}
