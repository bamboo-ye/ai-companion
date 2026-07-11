package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/windcry1/ai-companion/internal/identity"
)

func (s *Store) ListUserAccounts(ctx context.Context, filter identity.UserAccountFilter) ([]identity.User, error) {
	query := `SELECT BIN_TO_UUID(id),email,password_hash,display_name,timezone,locale,status,created_at,updated_at FROM users WHERE 1=1`
	args := make([]any, 0, 4)
	if filter.Status != "" {
		query += ` AND status=?`
		args = append(args, filter.Status)
	}
	if filter.Query != "" {
		query += ` AND (email LIKE ? OR display_name LIKE ?)`
		like := "%" + filter.Query + "%"
		args = append(args, like, like)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]identity.User, 0)
	for rows.Next() {
		user, scanErr := scanUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, user)
	}
	return items, rows.Err()
}

func (s *Store) GetUserAccount(ctx context.Context, userID string) (identity.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id),email,password_hash,display_name,timezone,locale,status,created_at,updated_at FROM users WHERE id=UUID_TO_BIN(?)`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return identity.User{}, identity.ErrNotFound
	}
	return user, err
}

func (s *Store) SetUserStatus(ctx context.Context, input identity.UserModerationInput) (identity.User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return identity.User{}, err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM users WHERE id=UUID_TO_BIN(?) FOR UPDATE`, input.UserID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return identity.User{}, identity.ErrNotFound
	} else if err != nil {
		return identity.User{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET status=?,updated_at=? WHERE id=UUID_TO_BIN(?)`, input.Status, input.Now, input.UserID); err != nil {
		return identity.User{}, err
	}
	if input.Status == "disabled" {
		if _, err = tx.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at=COALESCE(revoked_at,?),last_used_at=? WHERE user_id=UUID_TO_BIN(?) AND revoked_at IS NULL`, input.Now, input.Now, input.UserID); err != nil {
			return identity.User{}, err
		}
	}
	metadata, _ := json.Marshal(map[string]string{"actor": input.Actor, "reason": input.Reason, "from_status": current, "to_status": input.Status})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('operator',NULL,'user.status.update','user',UUID_TO_BIN(?),?,?)`, input.UserID, metadata, input.Now); err != nil {
		return identity.User{}, err
	}
	if err = tx.Commit(); err != nil {
		return identity.User{}, err
	}
	return s.GetUserAccount(ctx, input.UserID)
}

func (s *Store) ListAuditLogs(ctx context.Context, filter identity.AuditLogFilter) ([]identity.AuditLog, error) {
	query := `SELECT id,actor_type,COALESCE(BIN_TO_UUID(actor_id),''),action,resource_type,COALESCE(BIN_TO_UUID(resource_id),''),COALESCE(trace_id,''),metadata,occurred_at FROM audit_logs WHERE 1=1`
	args := make([]any, 0, 3)
	if filter.ResourceType != "" {
		query += ` AND resource_type=?`
		args = append(args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		query += ` AND resource_id=UUID_TO_BIN(?)`
		args = append(args, filter.ResourceID)
	}
	if filter.ActorType != "" {
		query += ` AND actor_type=?`
		args = append(args, filter.ActorType)
	}
	if filter.Action != "" {
		query += ` AND action=?`
		args = append(args, filter.Action)
	}
	query += ` ORDER BY occurred_at DESC,id DESC LIMIT ?`
	args = append(args, filter.Limit)
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
			if actor := operatorFromAuditMetadata(record.Metadata); actor != "" {
				record.ActorLabel = actor
			}
		}
		items = append(items, record)
	}
	return items, rows.Err()
}

func scanAuditLog(row rowScanner) (identity.AuditLog, error) {
	var record identity.AuditLog
	if err := row.Scan(&record.ID, &record.ActorType, &record.ActorID, &record.Action, &record.ResourceType, &record.ResourceID, &record.TraceID, &record.Metadata, &record.OccurredAt); err != nil {
		return identity.AuditLog{}, err
	}
	return record, nil
}

func operatorFromAuditMetadata(metadata json.RawMessage) string {
	var decoded map[string]any
	if json.Unmarshal(metadata, &decoded) != nil {
		return ""
	}
	if actor, _ := decoded["actor"].(string); strings.TrimSpace(actor) != "" {
		return strings.TrimSpace(actor)
	}
	return ""
}
