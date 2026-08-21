package planner

import (
	"context"
	"sort"
	"sync"
	"time"
)

type MemoryStore struct {
	mu          sync.RWMutex
	plans       map[string]Plan
	reminders   map[string]Reminder
	idempotency map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{plans: map[string]Plan{}, reminders: map[string]Reminder{}, idempotency: map[string]string{}}
}

func (s *MemoryStore) CreatePlan(_ context.Context, item Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans[item.ID] = item
	return nil
}

func (s *MemoryStore) ListPlans(_ context.Context, userID, localDate string) ([]Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Plan, 0)
	for _, item := range s.plans {
		if item.UserID == userID && item.Status == "active" && (localDate == "" || item.LocalDate == localDate) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].LocalDate < items[j].LocalDate })
	return items, nil
}

func (s *MemoryStore) AddPlanItem(_ context.Context, userID, planID string, item PlanItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[planID]
	if !ok || plan.UserID != userID || plan.Status != "active" {
		return ErrNotFound
	}
	plan.Items = append(plan.Items, item)
	plan.UpdatedAt = item.UpdatedAt
	s.plans[planID] = plan
	return nil
}

func (s *MemoryStore) FindPlanItemBySource(_ context.Context, userID, localDate, source string) (PlanItem, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, plan := range s.plans {
		if plan.UserID != userID || plan.LocalDate != localDate || plan.Status != "active" {
			continue
		}
		for _, item := range plan.Items {
			if item.Source == source {
				return item, nil
			}
		}
	}
	return PlanItem{}, ErrNotFound
}

func (s *MemoryStore) GetPlanItem(_ context.Context, userID, itemID string) (PlanItem, Plan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, plan := range s.plans {
		if plan.UserID != userID || plan.Status != "active" {
			continue
		}
		for _, item := range plan.Items {
			if item.ID == itemID {
				return item, plan, nil
			}
		}
	}
	return PlanItem{}, Plan{}, ErrNotFound
}

func (s *MemoryStore) UpdatePlanItemSchedule(_ context.Context, userID string, updated PlanItem, expectedUpdatedAt *time.Time, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for planID, plan := range s.plans {
		if plan.UserID != userID || plan.Status != "active" {
			continue
		}
		for index := range plan.Items {
			item := plan.Items[index]
			if item.ID != updated.ID {
				continue
			}
			if expectedUpdatedAt != nil && !item.UpdatedAt.Equal(*expectedUpdatedAt) {
				if sameTime(item.StartsAt, updated.StartsAt) {
					return nil
				}
				return ErrStaleUpdate
			}
			if item.Status != "pending" && item.Status != "in_progress" {
				return ErrNotFound
			}
			item.StartsAt, item.EndsAt, item.UpdatedAt = updated.StartsAt, updated.EndsAt, now
			plan.Items[index] = item
			plan.UpdatedAt = now
			s.plans[planID] = plan
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) CompletePlanItem(_ context.Context, userID, itemID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for planID, plan := range s.plans {
		if plan.UserID != userID || plan.Status != "active" {
			continue
		}
		for index := range plan.Items {
			if plan.Items[index].ID != itemID {
				continue
			}
			if plan.Items[index].Status == "completed" {
				return nil
			}
			if plan.Items[index].Status != "pending" && plan.Items[index].Status != "in_progress" {
				return ErrNotFound
			}
			plan.Items[index].Status = "completed"
			plan.Items[index].UpdatedAt = now
			plan.UpdatedAt = now
			s.plans[planID] = plan
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) CreateReminder(_ context.Context, item Reminder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reminders[item.ID] = item
	return nil
}

func (s *MemoryStore) GetReminder(_ context.Context, userID, reminderID string) (Reminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.reminders[reminderID]
	if !ok || item.UserID != userID || item.Status == "cancelled" {
		return Reminder{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) FindReminderBySourceMessage(_ context.Context, userID, sourceMessageID string) (Reminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.reminders {
		if item.UserID == userID && item.SourceMessageID == sourceMessageID && item.Status != "cancelled" {
			return item, nil
		}
	}
	return Reminder{}, ErrNotFound
}

func (s *MemoryStore) ConfirmReminder(_ context.Context, item Reminder, key string, now time.Time) (Reminder, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ownerKey := item.UserID + ":" + key
	if owner, ok := s.idempotency[ownerKey]; ok {
		if owner != item.ID {
			return Reminder{}, false, ErrIdempotencyReuse
		}
		return s.reminders[owner], false, nil
	}
	stored, ok := s.reminders[item.ID]
	if !ok || stored.UserID != item.UserID {
		return Reminder{}, false, ErrNotFound
	}
	if stored.Status == "active" || stored.Status == "completed" {
		return stored, false, nil
	}
	stored.Status = "active"
	stored.UpdatedAt = now
	s.reminders[stored.ID] = stored
	s.idempotency[ownerKey] = stored.ID
	return stored, true, nil
}

func (s *MemoryStore) ListReminders(_ context.Context, userID string, start, end *time.Time, limit int) ([]Reminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Reminder, 0)
	for _, item := range s.reminders {
		if item.UserID != userID || item.Status == "cancelled" {
			continue
		}
		if start != nil && (item.DueAt == nil || item.DueAt.Before(*start)) {
			continue
		}
		if end != nil && (item.DueAt == nil || !item.DueAt.Before(*end)) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DueAt == nil {
			return false
		}
		if items[j].DueAt == nil {
			return true
		}
		return items[i].DueAt.Before(*items[j].DueAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) ListTodayReminders(_ context.Context, userID string, start, end time.Time, limit int) ([]Reminder, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Reminder, 0)
	for _, item := range s.reminders {
		if item.UserID != userID || item.DueAt == nil || !item.DueAt.Before(end) {
			continue
		}
		if item.Status != "active" && (item.Status != "completed" || item.DueAt.Before(start)) {
			continue
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DueAt.Before(*items[j].DueAt) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *MemoryStore) CompleteReminder(_ context.Context, userID, reminderID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.reminders[reminderID]
	if !ok || item.UserID != userID || item.Status != "active" {
		return ErrNotFound
	}
	item.Status = "completed"
	item.UpdatedAt = now
	s.reminders[item.ID] = item
	return nil
}

func (s *MemoryStore) RescheduleReminder(_ context.Context, updated Reminder, expectedUpdatedAt *time.Time, now time.Time) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.reminders[updated.ID]
	if !ok || item.UserID != updated.UserID || item.Status != "active" {
		return Reminder{}, ErrNotFound
	}
	if expectedUpdatedAt != nil && !item.UpdatedAt.Equal(*expectedUpdatedAt) {
		if item.DueAt != nil && updated.DueAt != nil && item.DueAt.Equal(*updated.DueAt) && item.TimePrecision == updated.TimePrecision {
			return item, nil
		}
		return Reminder{}, ErrStaleUpdate
	}
	item.DueAt = updated.DueAt
	item.LocalDue = updated.LocalDue
	item.Timezone = updated.Timezone
	item.TimePrecision = updated.TimePrecision
	item.SystemSyncStatus = updated.SystemSyncStatus
	item.UpdatedAt = now
	s.reminders[item.ID] = item
	return item, nil
}

func (s *MemoryStore) UpdateReminderSync(_ context.Context, userID, reminderID string, result SyncResult, now time.Time) (Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.reminders[reminderID]
	if !ok || item.UserID != userID || item.Status == "cancelled" {
		return Reminder{}, ErrNotFound
	}
	item.SystemSyncStatus = result.Status
	item.ExternalProvider = result.Provider
	item.ExternalID = result.ExternalID
	item.ExternalRevision = result.Revision
	item.UpdatedAt = now
	s.reminders[item.ID] = item
	return item, nil
}

func sameTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
