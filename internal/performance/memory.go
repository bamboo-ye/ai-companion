package performance

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore keeps the default server and HTTP tests usable without a
// database. Fixtures can be replaced atomically by tests or development tools.
type MemoryStore struct {
	mu        sync.RWMutex
	versions  []VersionMetrics
	models    []ModelMetrics
	window    WindowMetrics
	trends    []TrendPoint
	budgets   map[string]Budget
	alerts    map[string]string
	decisions map[string]BudgetPolicyDecision
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{budgets: make(map[string]Budget), alerts: make(map[string]string), decisions: make(map[string]BudgetPolicyDecision)}
}

func (s *MemoryStore) ListVersionMetrics(context.Context, Filter) ([]VersionMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]VersionMetrics(nil), s.versions...), nil
}

func (s *MemoryStore) ListTrend(context.Context, TrendFilter) ([]TrendPoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TrendPoint(nil), s.trends...), nil
}

func (s *MemoryStore) ListBudgets(context.Context) ([]Budget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Budget, 0, len(s.budgets))
	for _, item := range s.budgets {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.Before(items[j].CreatedAt) })
	return items, nil
}

func (s *MemoryStore) GetBudget(_ context.Context, budgetID string) (Budget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, exists := s.budgets[budgetID]
	if !exists {
		return Budget{}, ErrNotFound
	}
	return item, nil
}

func (s *MemoryStore) ListBudgetForecastOutcomes(context.Context, ForecastHistoryFilter) ([]BudgetForecastOutcome, error) {
	return []BudgetForecastOutcome{}, nil
}

func (s *MemoryStore) ListBudgetPolicyDecisions(_ context.Context, budgetID string) ([]BudgetPolicyDecision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]BudgetPolicyDecision, 0, len(s.decisions))
	for _, item := range s.decisions {
		if budgetID == "" || item.BudgetID == budgetID {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].DecidedAt.After(items[j].DecidedAt) })
	return items, nil
}

func (s *MemoryStore) SaveBudgetPolicyDecision(_ context.Context, item BudgetPolicyDecision) (BudgetPolicyDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := item.BudgetID + "\x00" + item.RecommendationKey
	if current, exists := s.decisions[key]; exists {
		item.ID = current.ID
		item.AppliedAt, item.AppliedBy, item.AppliedBudgetRevision = current.AppliedAt, current.AppliedBy, current.AppliedBudgetRevision
		item.EffectObservationDays, item.EffectMinSamples = current.EffectObservationDays, current.EffectMinSamples
		item.EffectReview = current.EffectReview
	}
	s.decisions[key] = item
	return item, nil
}

func (s *MemoryStore) AcknowledgeBudgetPolicyEffect(_ context.Context, effect BudgetPolicyEffect, review BudgetPolicyEffectReview) (BudgetPolicyEffectReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, decision := range s.decisions {
		if decision.ID != effect.DecisionID || decision.BudgetID != effect.BudgetID {
			continue
		}
		if decision.AppliedAt == nil || (decision.EffectReview != nil && decision.EffectReview.Status == "closed") {
			return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
		}
		if decision.EffectReview != nil && decision.EffectReview.RollbackAppliedAt != nil && review.Disposition != "rollback_planned" {
			return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
		}
		if decision.EffectReview != nil {
			review.RollbackAppliedAt = decision.EffectReview.RollbackAppliedAt
			review.RollbackAppliedBy = decision.EffectReview.RollbackAppliedBy
			review.RollbackBudgetRevision = decision.EffectReview.RollbackBudgetRevision
		}
		reviewCopy := review
		decision.EffectReview = &reviewCopy
		s.decisions[key] = decision
		return review, nil
	}
	return BudgetPolicyEffectReview{}, ErrNotFound
}

func (s *MemoryStore) CloseBudgetPolicyEffect(_ context.Context, effect BudgetPolicyEffect, actor, reason string, now time.Time) (BudgetPolicyEffectReview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, decision := range s.decisions {
		if decision.ID != effect.DecisionID || decision.BudgetID != effect.BudgetID {
			continue
		}
		if decision.EffectReview == nil || decision.EffectReview.Status != "acknowledged" {
			return BudgetPolicyEffectReview{}, ErrEffectReviewConflict
		}
		review := *decision.EffectReview
		review.Status, review.ClosedReason, review.ClosedBy, review.ClosedAt = "closed", reason, actor, &now
		decision.EffectReview = &review
		s.decisions[key] = decision
		return review, nil
	}
	return BudgetPolicyEffectReview{}, ErrNotFound
}

func (s *MemoryStore) CreateBudget(_ context.Context, item Budget, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, current := range s.budgets {
		if current.Module == item.Module && current.Period == item.Period {
			return ErrConflict
		}
	}
	s.budgets[item.ID] = item
	return nil
}

func (s *MemoryStore) UpdateBudget(_ context.Context, item Budget, expectedRevision int, _ string) (Budget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.budgets[item.ID]
	if !exists {
		return Budget{}, ErrNotFound
	}
	if current.Revision != expectedRevision {
		return Budget{}, ErrConflict
	}
	for id, candidate := range s.budgets {
		if id != item.ID && candidate.Module == item.Module && candidate.Period == item.Period {
			return Budget{}, ErrConflict
		}
	}
	item.CreatedAt, item.CreatedBy = current.CreatedAt, current.CreatedBy
	s.budgets[item.ID] = item
	if item.Enabled && item.ForecastAlertsEnabled {
		for key, decision := range s.decisions {
			if decision.BudgetID == item.ID && decision.Decision == "accepted" && decision.AppliedAt == nil && decision.BudgetRevision == expectedRevision && decision.ProposedLookbackDays == item.ForecastLookbackDays && decision.ProposedMinSamples == item.ForecastMinSamples {
				appliedAt := item.UpdatedAt
				decision.AppliedAt, decision.AppliedBy, decision.AppliedBudgetRevision = &appliedAt, item.UpdatedBy, item.Revision
				decision.EffectObservationDays, decision.EffectMinSamples = item.EffectObservationDays, item.EffectMinSamples
				s.decisions[key] = decision
				break
			}
		}
		for key, decision := range s.decisions {
			if decision.BudgetID == item.ID && decision.AppliedBudgetRevision == expectedRevision &&
				decision.CurrentLookbackDays == item.ForecastLookbackDays && decision.CurrentMinSamples == item.ForecastMinSamples &&
				decision.EffectReview != nil && decision.EffectReview.Status == "acknowledged" &&
				decision.EffectReview.Disposition == "rollback_planned" && decision.EffectReview.RollbackAppliedAt == nil {
				appliedAt := item.UpdatedAt
				review := *decision.EffectReview
				review.RollbackAppliedAt, review.RollbackAppliedBy, review.RollbackBudgetRevision = &appliedAt, item.UpdatedBy, item.Revision
				decision.EffectReview = &review
				s.decisions[key] = decision
				break
			}
		}
	}
	return item, nil
}

func (s *MemoryStore) ReconcileBudgetIncident(_ context.Context, item BudgetStatus, _ time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.budgets[item.ID]; !exists {
		return "", ErrNotFound
	} else if current.Revision != item.Revision {
		return "", ErrConflict
	}
	previous := s.alerts[item.ID]
	signal := item.AlertStatus
	if signal == "" {
		signal = item.Status
	}
	if signal != "warning" && signal != "exceeded" && signal != "projected_exceeded" {
		if previous == "" {
			return "steady", nil
		}
		delete(s.alerts, item.ID)
		return "resolved", nil
	}
	if previous == "" {
		s.alerts[item.ID] = signal
		return "opened", nil
	}
	if previous != "exceeded" && signal == "exceeded" {
		s.alerts[item.ID] = signal
		return "escalated", nil
	}
	s.alerts[item.ID] = signal
	return "updated", nil
}

func (s *MemoryStore) ListModelMetrics(context.Context, Filter) ([]ModelMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ModelMetrics(nil), s.models...), nil
}

func (s *MemoryStore) MeasureWindow(_ context.Context, filter WindowFilter) (WindowMetrics, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := s.window
	result.From = filter.From
	result.To = filter.To
	return result, nil
}

func (s *MemoryStore) SetFixtures(versions []VersionMetrics, models []ModelMetrics, window WindowMetrics) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versions = append([]VersionMetrics(nil), versions...)
	s.models = append([]ModelMetrics(nil), models...)
	s.window = window
}
