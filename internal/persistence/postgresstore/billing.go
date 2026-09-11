package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/billing"
)

var _ billing.Store = (*Store)(nil)
var _ billing.UsageStore = (*Store)(nil)

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

func (s *Store) CountAgentRunsInPeriod(ctx context.Context, userID string, start, end time.Time) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent.runs WHERE user_id=$1 AND created_at>=$2 AND created_at<$3`, userID, start, end).Scan(&total)
	return total, err
}

func (s *Store) SumModelCostMicrosInPeriod(ctx context.Context, userID string, start, end time.Time) (int, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(cost_micros),0)::bigint FROM app.model_usage_observations WHERE user_id=$1 AND observed_at>=$2 AND observed_at<$3`, userID, start, end).Scan(&total)
	return int(total), err
}

func (s *Store) SumUsageAdjustments(ctx context.Context, userID, resource string, start, end *time.Time) (int, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(delta),0)::bigint
		FROM app.billing_usage_adjustments
		WHERE user_id=$1 AND resource=$2
			AND (($3::timestamptz IS NULL AND period_start IS NULL) OR (period_start=$3 AND period_end=$4))`,
		userID, resource, start, end,
	).Scan(&total)
	return int(total), err
}

func (s *Store) CreateUsageAdjustment(ctx context.Context, item billing.UsageAdjustment) (billing.UsageAdjustment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return billing.UsageAdjustment{}, err
	}
	defer tx.Rollback()
	err = scanUsageAdjustment(tx.QueryRowContext(ctx, `
		INSERT INTO app.billing_usage_adjustments (
			id,user_id,resource,delta,reason,actor,period_start,period_end,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id::text,user_id::text,resource,delta,reason,actor,period_start,period_end,created_at`,
		item.ID, item.UserID, item.Resource, item.Delta, item.Reason, item.Actor,
		item.PeriodStart, item.PeriodEnd, item.CreatedAt,
	), &item)
	if err != nil {
		return billing.UsageAdjustment{}, err
	}
	metadata, err := json.Marshal(map[string]any{
		"user_id": item.UserID, "resource": item.Resource, "delta": item.Delta,
		"reason": item.Reason, "period_start": item.PeriodStart, "period_end": item.PeriodEnd,
	})
	if err != nil {
		return billing.UsageAdjustment{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO eventing.audit_logs (
		actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at
	) VALUES ('operator',NULL,'billing.usage.adjust','billing_usage_adjustment',$1,$2,$3)`, item.ID, metadata, item.CreatedAt); err != nil {
		return billing.UsageAdjustment{}, err
	}
	if err = tx.Commit(); err != nil {
		return billing.UsageAdjustment{}, err
	}
	return item, nil
}

func (s *Store) ListUsageAdjustments(ctx context.Context, userID string, limit int) ([]billing.UsageAdjustment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text,user_id::text,resource,delta,reason,actor,period_start,period_end,created_at
		FROM app.billing_usage_adjustments WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]billing.UsageAdjustment, 0)
	for rows.Next() {
		var item billing.UsageAdjustment
		if err = scanUsageAdjustment(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanUsageAdjustment(row rowScanner, item *billing.UsageAdjustment) error {
	return row.Scan(&item.ID, &item.UserID, &item.Resource, &item.Delta, &item.Reason,
		&item.Actor, &item.PeriodStart, &item.PeriodEnd, &item.CreatedAt)
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
