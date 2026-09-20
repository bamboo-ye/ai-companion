package postgresstore

import (
	"context"
	"github.com/windcry1/ai-companion/internal/billing"
)

var _ billing.QuotaStore = (*Store)(nil)

func (s *Store) GetQuotaPolicies(ctx context.Context, userID string) (billing.QuotaPolicies, error) {
	return (billing.SQLQuotaStore{DB: s.db, Postgres: true}).GetQuotaPolicies(ctx, userID)
}

func (s *Store) SaveQuotaPolicy(ctx context.Context, p billing.QuotaPolicy, expected int64) (billing.QuotaPolicy, error) {
	return (billing.SQLQuotaStore{DB: s.db, Postgres: true}).SaveQuotaPolicy(ctx, p, expected)
}
