package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/windcry1/ai-companion/internal/identity"
)

var _ identity.AdminStore = (*Store)(nil)

func (s *Store) ListUserAccounts(ctx context.Context, filter identity.UserAccountFilter) ([]identity.User, error) {
	query := userSelect + ` WHERE TRUE`
	args := make([]any, 0, 4)
	if filter.Status != "" {
		args = append(args, filter.Status)
		query += ` AND status=$` + placeholder(len(args))
	}
	if filter.Query != "" {
		args = append(args, "%"+filter.Query+"%")
		query += ` AND (email ILIKE $` + placeholder(len(args)) +
			` OR display_name ILIKE $` + placeholder(len(args)) + `)`
	}
	args = append(args, filter.Limit)
	query += ` ORDER BY created_at DESC LIMIT $` + placeholder(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]identity.User, 0)
	for rows.Next() {
		item, scanErr := scanUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetUserAccount(ctx context.Context, userID string) (identity.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE id=$1`, userID))
}

func (s *Store) SetUserStatus(ctx context.Context, input identity.UserModerationInput) (identity.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.User{}, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM app.users WHERE id=$1 FOR UPDATE`, input.UserID,
	).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.User{}, identity.ErrNotFound
	}
	if err != nil {
		return identity.User{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.users SET status=$1,updated_at=$2 WHERE id=$3`,
		input.Status, input.Now, input.UserID,
	); err != nil {
		return identity.User{}, err
	}
	if input.Status == "disabled" {
		if _, err = tx.ExecContext(ctx, `
			UPDATE app.refresh_sessions SET
				revoked_at=COALESCE(revoked_at,$1),last_used_at=$1
			WHERE user_id=$2 AND revoked_at IS NULL`,
			input.Now, input.UserID,
		); err != nil {
			return identity.User{}, err
		}
	}
	metadata, _ := json.Marshal(map[string]string{
		"actor": input.Actor, "reason": input.Reason,
		"from_status": current, "to_status": input.Status,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('operator',NULL,'user.status.update','user',$1,$2,$3)`,
		input.UserID, metadata, input.Now,
	); err != nil {
		return identity.User{}, err
	}
	if err = tx.Commit(); err != nil {
		return identity.User{}, err
	}
	return s.GetUserAccount(ctx, input.UserID)
}

func (s *Store) ListAuditLogs(ctx context.Context, filter identity.AuditLogFilter) ([]identity.AuditLog, error) {
	query := auditLogSelect + ` WHERE TRUE`
	args := make([]any, 0, 5)
	if filter.ResourceType != "" {
		args = append(args, filter.ResourceType)
		query += ` AND resource_type=$` + placeholder(len(args))
	}
	if filter.ResourceID != "" {
		args = append(args, filter.ResourceID)
		query += ` AND resource_id=$` + placeholder(len(args))
	}
	if filter.ActorType != "" {
		args = append(args, filter.ActorType)
		query += ` AND actor_type=$` + placeholder(len(args))
	}
	if filter.Action != "" {
		args = append(args, filter.Action)
		query += ` AND action=$` + placeholder(len(args))
	}
	args = append(args, filter.Limit)
	query += ` ORDER BY occurred_at DESC,id DESC LIMIT $` + placeholder(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]identity.AuditLog, 0)
	for rows.Next() {
		record, scanErr := scanAuditLog(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if record.ActorType == "operator" {
			record.ActorLabel = operatorFromAuditMetadata(record.Metadata)
		}
		items = append(items, record)
	}
	return items, rows.Err()
}

const auditLogSelect = `
	SELECT id,actor_type,COALESCE(actor_id::text,''),action,resource_type,
		COALESCE(resource_id::text,''),COALESCE(trace_id,''),metadata,occurred_at
	FROM eventing.audit_logs`

func scanAuditLog(row rowScanner) (identity.AuditLog, error) {
	var record identity.AuditLog
	err := row.Scan(
		&record.ID, &record.ActorType, &record.ActorID, &record.Action,
		&record.ResourceType, &record.ResourceID, &record.TraceID,
		&record.Metadata, &record.OccurredAt,
	)
	return record, err
}

func operatorFromAuditMetadata(metadata json.RawMessage) string {
	var decoded map[string]any
	if json.Unmarshal(metadata, &decoded) != nil {
		return ""
	}
	actor, _ := decoded["actor"].(string)
	return strings.TrimSpace(actor)
}
