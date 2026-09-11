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
	agentRuns     map[string][]time.Time
	modelCosts    map[string][]modelCostUsage
	adjustments   map[string][]UsageAdjustment
}

type modelCostUsage struct {
	value     int
	createdAt time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		subscriptions: map[string]Subscription{},
		documents:     map[string]int{},
		skillRuns:     map[string][]time.Time{},
		workspaces:    map[string]int{},
		agentRuns:     map[string][]time.Time{},
		modelCosts:    map[string][]modelCostUsage{},
		adjustments:   map[string][]UsageAdjustment{},
	}
}

func (s *MemoryStore) SetExtendedUsage(userID string, agentRuns []time.Time, modelCosts map[time.Time]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentRuns[userID] = append([]time.Time(nil), agentRuns...)
	s.modelCosts[userID] = nil
	for createdAt, value := range modelCosts {
		s.modelCosts[userID] = append(s.modelCosts[userID], modelCostUsage{value: value, createdAt: createdAt})
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

func (s *MemoryStore) CountAgentRunsInPeriod(_ context.Context, userID string, start, end time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return countTimes(s.agentRuns[userID], start, end), nil
}

func (s *MemoryStore) SumModelCostMicrosInPeriod(_ context.Context, userID string, start, end time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, item := range s.modelCosts[userID] {
		if !item.createdAt.Before(start) && item.createdAt.Before(end) {
			total += item.value
		}
	}
	return total, nil
}

func (s *MemoryStore) SumUsageAdjustments(_ context.Context, userID, resource string, start, end *time.Time) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, item := range s.adjustments[userID] {
		if item.Resource != resource || !samePeriod(item.PeriodStart, start) || !samePeriod(item.PeriodEnd, end) {
			continue
		}
		total += item.Delta
	}
	return total, nil
}

func (s *MemoryStore) CreateUsageAdjustment(_ context.Context, item UsageAdjustment) (UsageAdjustment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.adjustments[item.UserID] = append([]UsageAdjustment{item}, s.adjustments[item.UserID]...)
	return item, nil
}

func (s *MemoryStore) ListUsageAdjustments(_ context.Context, userID string, limit int) ([]UsageAdjustment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.adjustments[userID]
	if len(items) > limit {
		items = items[:limit]
	}
	return append([]UsageAdjustment(nil), items...), nil
}

func countTimes(items []time.Time, start, end time.Time) int {
	total := 0
	for _, item := range items {
		if !item.Before(start) && item.Before(end) {
			total++
		}
	}
	return total
}

func samePeriod(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
