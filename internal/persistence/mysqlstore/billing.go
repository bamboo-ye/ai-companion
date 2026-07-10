package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
)

func (s *Store) GetActiveSubscription(ctx context.Context, userID string, now time.Time) (billing.Subscription, error) {
	item, err := scanBillingSubscription(s.db.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),plan_code,status,current_period_start,current_period_end,provider,provider_ref,created_at,updated_at FROM billing_subscriptions WHERE user_id=UUID_TO_BIN(?) AND status='active' AND current_period_start<=? AND current_period_end>? ORDER BY current_period_end DESC LIMIT 1`, userID, now, now))
	if errors.Is(err, sql.ErrNoRows) {
		return billing.Subscription{}, billing.ErrNotFound
	}
	return item, err
}

func (s *Store) CountActiveDocuments(ctx context.Context, userID string) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM documents d JOIN files f ON f.id=d.file_id WHERE d.user_id=UUID_TO_BIN(?) AND d.ingest_status<>'deleted' AND f.status='active'`, userID).Scan(&total)
	return total, err
}

func (s *Store) CountSkillRunsInPeriod(ctx context.Context, userID string, start, end time.Time) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM skill_runs WHERE user_id=UUID_TO_BIN(?) AND created_at>=? AND created_at<?`, userID, start, end).Scan(&total)
	return total, err
}

func (s *Store) CountOwnedWorkspaces(ctx context.Context, userID string) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspaces WHERE owner_id=UUID_TO_BIN(?) AND status='active'`, userID).Scan(&total)
	return total, err
}

func scanBillingSubscription(row rowScanner) (billing.Subscription, error) {
	var item billing.Subscription
	if err := row.Scan(&item.ID, &item.UserID, &item.PlanCode, &item.Status, &item.CurrentPeriodStart, &item.CurrentPeriodEnd, &item.Provider, &item.ProviderRef, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return billing.Subscription{}, err
	}
	return item, nil
}
