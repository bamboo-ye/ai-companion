package email

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]Delivery
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{items: map[string]Delivery{}} }

func (s *MemoryStore) CreateDelivery(_ context.Context, item Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.items[item.ID]; exists {
		return ErrConflict
	}
	s.items[item.ID] = item
	return nil
}

func (s *MemoryStore) ListDeliveries(_ context.Context, status string, limit int) ([]Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Delivery, 0)
	for _, item := range s.items {
		if status == "" || item.Status == status {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) GetDelivery(_ context.Context, deliveryID string) (Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[deliveryID]
	if !ok {
		return Delivery{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) ListDeliveriesByResource(_ context.Context, resourceType, resourceID string) ([]Delivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Delivery, 0)
	for _, item := range s.items {
		if item.ResourceType == resourceType && item.ResourceID == resourceID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *MemoryStore) ReplayDelivery(_ context.Context, deliveryID string, now time.Time) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[deliveryID]
	if !ok {
		return Delivery{}, ErrNotFound
	}
	if item.Status != "failed" {
		return Delivery{}, ErrConflict
	}
	item.Status, item.FailureCode, item.WorkerID, item.LeaseExpiresAt = "queued", "", "", nil
	item.AvailableAt, item.UpdatedAt = now, now
	s.items[deliveryID] = item
	return item, nil
}

func (s *MemoryStore) ClaimDelivery(_ context.Context, workerID string, now time.Time, lease time.Duration) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	selectedID := ""
	var selected Delivery
	for id, item := range s.items {
		available := !item.AvailableAt.After(now) && (item.Status == "queued" || (item.Status == "processing" && item.LeaseExpiresAt != nil && !item.LeaseExpiresAt.After(now)))
		if !available {
			continue
		}
		if selectedID == "" || item.AvailableAt.Before(selected.AvailableAt) || (item.AvailableAt.Equal(selected.AvailableAt) && item.CreatedAt.Before(selected.CreatedAt)) {
			selectedID, selected = id, item
		}
	}
	if selectedID == "" {
		return Delivery{}, ErrNoDelivery
	}
	return s.claimLocked(selectedID, selected, workerID, now, lease), nil
}

func (s *MemoryStore) ClaimDeliveryByID(_ context.Context, deliveryID, workerID string, now time.Time, lease time.Duration) (Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[deliveryID]
	available := ok && !item.AvailableAt.After(now) && (item.Status == "queued" || (item.Status == "processing" && item.LeaseExpiresAt != nil && !item.LeaseExpiresAt.After(now)))
	if !available {
		return Delivery{}, ErrNoDelivery
	}
	return s.claimLocked(deliveryID, item, workerID, now, lease), nil
}

func (s *MemoryStore) claimLocked(deliveryID string, item Delivery, workerID string, now time.Time, lease time.Duration) Delivery {
	expires := now.Add(lease)
	item.Status, item.WorkerID, item.LeaseExpiresAt = "processing", workerID, &expires
	item.Attempts++
	item.UpdatedAt = now
	s.items[deliveryID] = item
	return item
}

func (s *MemoryStore) CompleteDelivery(_ context.Context, item Delivery, workerID, providerMessageID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.items[item.ID]
	if !ok || current.Status != "processing" || current.WorkerID != workerID {
		return ErrConflict
	}
	current.Status, current.Provider, current.ProviderMessageID = "sent", "memory", providerMessageID
	current.FailureCode, current.WorkerID, current.LeaseExpiresAt = "", "", nil
	current.UpdatedAt, current.SentAt = now, &now
	s.items[item.ID] = current
	return nil
}

func (s *MemoryStore) FailDelivery(_ context.Context, item Delivery, workerID, code string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.items[item.ID]
	if !ok || current.Status != "processing" || current.WorkerID != workerID {
		return ErrConflict
	}
	current.Status, current.FailureCode = "failed", code
	current.WorkerID, current.LeaseExpiresAt = "", nil
	current.UpdatedAt = now
	s.items[item.ID] = current
	return nil
}
