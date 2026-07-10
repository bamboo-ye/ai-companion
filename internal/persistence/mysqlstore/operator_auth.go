package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/opsauth"
)

func (s *Store) FindOperatorByTokenHash(ctx context.Context, tokenHash string) (opsauth.Account, error) {
	account, err := scanOperatorAccount(s.db.QueryRowContext(ctx, `SELECT id,display_name,role,status,token_hash,totp_secret,mfa_enabled,created_at,updated_at,last_authenticated_at FROM operator_accounts WHERE token_hash=?`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrUnauthorized
	}
	return account, err
}

func (s *Store) RecordOperatorAuthenticated(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE operator_accounts SET last_authenticated_at=?,updated_at=? WHERE id=?`, now, now, id)
	return err
}

func (s *Store) ListOperators(ctx context.Context, filter opsauth.OperatorFilter) ([]opsauth.Account, error) {
	query := `SELECT id,display_name,role,status,token_hash,totp_secret,mfa_enabled,created_at,updated_at,last_authenticated_at FROM operator_accounts WHERE 1=1`
	args := make([]any, 0, 3)
	if filter.Role != "" {
		query += ` AND role=?`
		args = append(args, filter.Role)
	}
	if filter.Status != "" {
		query += ` AND status=?`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY created_at DESC,id ASC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]opsauth.Account, 0)
	for rows.Next() {
		account, scanErr := scanOperatorAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, account)
	}
	return items, rows.Err()
}

func (s *Store) GetOperator(ctx context.Context, id string) (opsauth.Account, error) {
	account, err := scanOperatorAccount(s.db.QueryRowContext(ctx, `SELECT id,display_name,role,status,token_hash,totp_secret,mfa_enabled,created_at,updated_at,last_authenticated_at FROM operator_accounts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	}
	return account, err
}

func (s *Store) CreateOperator(ctx context.Context, account opsauth.Account, audit opsauth.AuditInput) (opsauth.Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opsauth.Account{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO operator_accounts (id,display_name,role,status,token_hash,totp_secret,mfa_enabled,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		account.ID, account.DisplayName, account.Role, account.Status, account.TokenHash, account.TOTPSecret, account.MFAEnabled, account.CreatedAt, account.UpdatedAt); err != nil {
		if isDuplicate(err) {
			return opsauth.Account{}, opsauth.ErrConflict
		}
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, account.ID, map[string]string{"role": account.Role, "status": account.Status}); err != nil {
		return opsauth.Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return opsauth.Account{}, err
	}
	return s.GetOperator(ctx, account.ID)
}

func (s *Store) SetOperatorStatus(ctx context.Context, id, status string, audit opsauth.AuditInput) (opsauth.Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opsauth.Account{}, err
	}
	defer tx.Rollback()
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM operator_accounts WHERE id=? FOR UPDATE`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	} else if err != nil {
		return opsauth.Account{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operator_accounts SET status=?,updated_at=? WHERE id=?`, status, audit.Now, id); err != nil {
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, id, map[string]string{"from_status": current, "to_status": status}); err != nil {
		return opsauth.Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return opsauth.Account{}, err
	}
	return s.GetOperator(ctx, id)
}

func (s *Store) ResetOperatorToken(ctx context.Context, id, tokenHash string, audit opsauth.AuditInput) (opsauth.Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opsauth.Account{}, err
	}
	defer tx.Rollback()
	if err = tx.QueryRowContext(ctx, `SELECT id FROM operator_accounts WHERE id=? FOR UPDATE`, id).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	} else if err != nil {
		return opsauth.Account{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operator_accounts SET token_hash=?,updated_at=? WHERE id=?`, tokenHash, audit.Now, id); err != nil {
		if isDuplicate(err) {
			return opsauth.Account{}, opsauth.ErrConflict
		}
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, id, nil); err != nil {
		return opsauth.Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return opsauth.Account{}, err
	}
	return s.GetOperator(ctx, id)
}

func (s *Store) ResetOperatorMFA(ctx context.Context, id, secret string, enabled bool, audit opsauth.AuditInput) (opsauth.Account, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return opsauth.Account{}, err
	}
	defer tx.Rollback()
	var previous bool
	if err = tx.QueryRowContext(ctx, `SELECT mfa_enabled FROM operator_accounts WHERE id=? FOR UPDATE`, id).Scan(&previous); errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	} else if err != nil {
		return opsauth.Account{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE operator_accounts SET totp_secret=?,mfa_enabled=?,updated_at=? WHERE id=?`, secret, enabled, audit.Now, id); err != nil {
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, id, map[string]string{"from_mfa_enabled": boolString(previous), "to_mfa_enabled": boolString(enabled)}); err != nil {
		return opsauth.Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return opsauth.Account{}, err
	}
	return s.GetOperator(ctx, id)
}

func insertOperatorAudit(ctx context.Context, tx *sql.Tx, audit opsauth.AuditInput, operatorID string, extra map[string]string) error {
	metadata := map[string]string{
		"actor":       strings.TrimSpace(audit.Actor),
		"reason":      strings.TrimSpace(audit.Reason),
		"operator_id": strings.TrimSpace(operatorID),
	}
	for key, value := range extra {
		metadata[key] = value
	}
	payload, _ := json.Marshal(metadata)
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,metadata,occurred_at) VALUES ('operator',NULL,?,'operator_account',?,?)`, audit.Action, payload, audit.Now)
	return err
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func scanOperatorAccount(row rowScanner) (opsauth.Account, error) {
	var account opsauth.Account
	var last sql.NullTime
	if err := row.Scan(&account.ID, &account.DisplayName, &account.Role, &account.Status, &account.TokenHash, &account.TOTPSecret, &account.MFAEnabled, &account.CreatedAt, &account.UpdatedAt, &last); err != nil {
		return opsauth.Account{}, err
	}
	if last.Valid {
		account.LastAuthenticatedAt = &last.Time
	}
	return account, nil
}
