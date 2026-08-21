package postgresstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
)

var _ billing.Store = (*Store)(nil)

func (s *Store) GetActiveSubscription(ctx context.Context, userID string, now time.Time) (billing.Subscription, error) {
	item, err := scanBillingSubscription(s.db.QueryRowContext(ctx, `
		SELECT id::text,user_id::text,plan_code,status,current_period_start,
			current_period_end,provider,provider_ref,created_at,updated_at
		FROM app.billing_subscriptions
		WHERE user_id=$1 AND status='active'
			AND current_period_start<=$2 AND current_period_end>$2
		ORDER BY current_period_end DESC
		LIMIT 1`,
		userID, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return billing.Subscription{}, billing.ErrNotFound
	}
	return item, err
}

func (s *Store) CountActiveDocuments(ctx context.Context, userID string) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM app.documents d
		JOIN app.files f ON f.id=d.file_id
		WHERE d.user_id=$1 AND d.ingest_status<>'deleted' AND f.status='active'`,
		userID,
	).Scan(&total)
	return total, err
}

func (s *Store) CountSkillRunsInPeriod(ctx context.Context, userID string, start, end time.Time) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM app.skill_runs
		WHERE user_id=$1 AND created_at>=$2 AND created_at<$3`,
		userID, start, end,
	).Scan(&total)
	return total, err
}

func (s *Store) CountOwnedWorkspaces(ctx context.Context, userID string) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM app.workspaces
		WHERE owner_id=$1 AND status='active'`,
		userID,
	).Scan(&total)
	return total, err
}

func scanBillingSubscription(row rowScanner) (billing.Subscription, error) {
	var item billing.Subscription
	err := row.Scan(
		&item.ID, &item.UserID, &item.PlanCode, &item.Status,
		&item.CurrentPeriodStart, &item.CurrentPeriodEnd, &item.Provider,
		&item.ProviderRef, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}
