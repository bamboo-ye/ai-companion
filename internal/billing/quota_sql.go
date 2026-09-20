package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

// SQLQuotaStore keeps a policy update and its before/after audit in one transaction.
type SQLQuotaStore struct {
	DB       *sql.DB
	Postgres bool
}

func (s SQLQuotaStore) query(q string) string {
	if !s.Postgres {
		return q
	}
	q = strings.ReplaceAll(q, "billing_quota_policies", "app.billing_quota_policies")
	q = strings.ReplaceAll(q, "audit_logs", "eventing.audit_logs")
	for i := 1; strings.Contains(q, "?"); i++ {
		q = strings.Replace(q, "?", fmt.Sprintf("$%d", i), 1)
	}
	return q
}

func (s SQLQuotaStore) GetQuotaPolicies(ctx context.Context, userID string) (QuotaPolicies, error) {
	p := QuotaPolicies{User: QuotaPolicy{UserID: userID}}
	rows, err := s.DB.QueryContext(ctx, s.query(`SELECT scope_key,limits_json,revision,actor,reason,updated_at FROM billing_quota_policies WHERE scope_key IN (?,?)`), quotaKey(""), quotaKey(userID))
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		var policy QuotaPolicy
		if err = rows.Scan(&key, &raw, &policy.Revision, &policy.Actor, &policy.Reason, &policy.UpdatedAt); err != nil {
			return p, err
		}
		if err = json.Unmarshal(raw, &policy.Limits); err != nil {
			return p, err
		}
		if key == "global" {
			p.Global = policy
		} else {
			policy.UserID = userID
			p.User = policy
		}
	}
	return p, rows.Err()
}

func (s SQLQuotaStore) SaveQuotaPolicy(ctx context.Context, p QuotaPolicy, expected int64) (QuotaPolicy, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return QuotaPolicy{}, err
	}
	defer tx.Rollback()
	key := quotaKey(p.UserID)
	var before QuotaLimits
	var raw []byte
	var revision int64
	err = tx.QueryRowContext(ctx, s.query(`SELECT limits_json,revision FROM billing_quota_policies WHERE scope_key=?`), key).Scan(&raw, &revision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return QuotaPolicy{}, err
	}
	if revision != expected {
		return QuotaPolicy{}, ErrConflict
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &before); err != nil {
			return QuotaPolicy{}, err
		}
	}
	raw, err = json.Marshal(p.Limits)
	if err != nil {
		return QuotaPolicy{}, err
	}
	var result sql.Result
	if expected == 0 {
		q := `INSERT INTO billing_quota_policies (scope_key,limits_json,revision,actor,reason,updated_at) VALUES (?,?,?,?,?,?)`
		// Concurrent first writers must not replace one another.
		if s.Postgres {
			q += ` ON CONFLICT (scope_key) DO NOTHING`
		}
		result, err = tx.ExecContext(ctx, s.query(q), key, string(raw), p.Revision, p.Actor, p.Reason, p.UpdatedAt)
	} else {
		result, err = tx.ExecContext(ctx, s.query(`UPDATE billing_quota_policies SET limits_json=?,revision=?,actor=?,reason=?,updated_at=? WHERE scope_key=? AND revision=?`), string(raw), p.Revision, p.Actor, p.Reason, p.UpdatedAt, key, expected)
	}
	if err != nil {
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return QuotaPolicy{}, ErrConflict
		}
		return QuotaPolicy{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return QuotaPolicy{}, err
	}
	if count != 1 {
		return QuotaPolicy{}, ErrConflict
	}
	metadata, err := json.Marshal(map[string]any{"scope": key, "user_id": p.UserID, "actor": p.Actor, "reason": p.Reason, "before": before, "after": p.Limits, "revision": p.Revision})
	if err != nil {
		return QuotaPolicy{}, err
	}
	_, err = tx.ExecContext(ctx, s.query(`INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('operator',NULL,'billing.quota.update','billing_quota_policy',NULL,?,?)`), string(metadata), p.UpdatedAt)
	if err != nil {
		return QuotaPolicy{}, err
	}
	if err = tx.Commit(); err != nil {
		return QuotaPolicy{}, err
	}
	return p, nil
}
