package billing

import (
	"context"
	"sync"
	"time"
)

type MemoryStore struct {
	mu            sync.RWMutex
	subscriptions map[string]Subscription
	documents     map[string]int
	skillRuns     map[string][]time.Time
	workspaces    map[string]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		subscriptions: map[string]Subscription{},
		documents:     map[string]int{},
		skillRuns:     map[string][]time.Time{},
		workspaces:    map[string]int{},
	}
}

func (s *MemoryStore) PutSubscription(subscription Subscription) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions[subscription.UserID] = subscription
}

func (s *MemoryStore) SetUsage(userID string, documents, workspaces int, skillRuns []time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[userID] = documents
	s.workspaces[userID] = workspaces
	s.skillRuns[userID] = append([]time.Time(nil), skillRuns...)
}

func (s *MemoryStore) GetActiveSubscription(_ context.Context, userID string, now time.Time) (Subscription, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	subscription, ok := s.subscriptions[userID]
	if !ok || subscription.Status != "active" || now.Before(subscription.CurrentPeriodStart) || !now.Before(subscription.CurrentPeriodEnd) {
		return Subscription{}, ErrNotFound
	}
	return subscription, nil
}

func (s *MemoryStore) CountActiveDocuments(_ context.Context, userID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.documents[userID], nil
}

func (s *MemoryStore) CountSkillRunsInPeriod(_ context.Context, userID string, start, end time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, createdAt := range s.skillRuns[userID] {
		if !createdAt.Before(start) && createdAt.Before(end) {
			total++
		}
	}
	return total, nil
}

func (s *MemoryStore) CountOwnedWorkspaces(_ context.Context, userID string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workspaces[userID], nil
}
