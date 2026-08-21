package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/opsauth"
)

var _ opsauth.Store = (*Store)(nil)

func (s *Store) FindOperatorByTokenHash(ctx context.Context, tokenHash string) (opsauth.Account, error) {
	account, err := scanOperatorAccount(s.db.QueryRowContext(ctx, operatorAccountSelect+`
		WHERE token_hash=$1`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrUnauthorized
	}
	return account, err
}

func (s *Store) RecordOperatorAuthenticated(ctx context.Context, id string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE app.operator_accounts
		SET last_authenticated_at=$1,updated_at=$1
		WHERE id=$2`,
		now, id,
	)
	return err
}

func (s *Store) ListOperators(ctx context.Context, filter opsauth.OperatorFilter) ([]opsauth.Account, error) {
	query := operatorAccountSelect + ` WHERE TRUE`
	args := make([]any, 0, 3)
	if filter.Role != "" {
		args = append(args, filter.Role)
		query += ` AND role=$` + placeholder(len(args))
	}
	if filter.Status != "" {
		args = append(args, filter.Status)
		query += ` AND status=$` + placeholder(len(args))
	}
	args = append(args, filter.Limit)
	query += ` ORDER BY created_at DESC,id ASC LIMIT $` + placeholder(len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]opsauth.Account, 0)
	for rows.Next() {
		item, scanErr := scanOperatorAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetOperator(ctx context.Context, id string) (opsauth.Account, error) {
	account, err := scanOperatorAccount(s.db.QueryRowContext(ctx, operatorAccountSelect+`
		WHERE id=$1`, id))
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
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.operator_accounts (
			id,display_name,role,status,token_hash,totp_secret,mfa_enabled,
			created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		account.ID, account.DisplayName, account.Role, account.Status,
		account.TokenHash, account.TOTPSecret, account.MFAEnabled,
		account.CreatedAt, account.UpdatedAt,
	); err != nil {
		if isUniqueViolation(err) {
			return opsauth.Account{}, opsauth.ErrConflict
		}
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, account.ID, map[string]string{
		"role": account.Role, "status": account.Status,
	}); err != nil {
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
	var role, current string
	var mfaEnabled bool
	err = tx.QueryRowContext(ctx, `
		SELECT role,status,mfa_enabled
		FROM app.operator_accounts
		WHERE id=$1
		FOR UPDATE`, id,
	).Scan(&role, &current, &mfaEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	}
	if err != nil {
		return opsauth.Account{}, err
	}
	if status == "disabled" {
		if err = preventPostgresOperatorAdminLockout(ctx, tx, role, current, mfaEnabled, true, false); err != nil {
			return opsauth.Account{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.operator_accounts SET status=$1,updated_at=$2 WHERE id=$3`,
		status, audit.Now, id,
	); err != nil {
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, id, map[string]string{
		"from_status": current, "to_status": status,
	}); err != nil {
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
	var lockedID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM app.operator_accounts WHERE id=$1 FOR UPDATE`, id,
	).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	}
	if err != nil {
		return opsauth.Account{}, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.operator_accounts SET token_hash=$1,updated_at=$2 WHERE id=$3`,
		tokenHash, audit.Now, id,
	); err != nil {
		if isUniqueViolation(err) {
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
	var role, current string
	var previous bool
	err = tx.QueryRowContext(ctx, `
		SELECT role,status,mfa_enabled
		FROM app.operator_accounts
		WHERE id=$1
		FOR UPDATE`, id,
	).Scan(&role, &current, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return opsauth.Account{}, opsauth.ErrNotFound
	}
	if err != nil {
		return opsauth.Account{}, err
	}
	if !enabled {
		if err = preventPostgresOperatorAdminLockout(ctx, tx, role, current, previous, false, true); err != nil {
			return opsauth.Account{}, err
		}
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE app.operator_accounts
		SET totp_secret=$1,mfa_enabled=$2,updated_at=$3
		WHERE id=$4`,
		secret, enabled, audit.Now, id,
	); err != nil {
		return opsauth.Account{}, err
	}
	if err = insertOperatorAudit(ctx, tx, audit, id, map[string]string{
		"from_mfa_enabled": postgresBoolString(previous),
		"to_mfa_enabled":   postgresBoolString(enabled),
	}); err != nil {
		return opsauth.Account{}, err
	}
	if err = tx.Commit(); err != nil {
		return opsauth.Account{}, err
	}
	return s.GetOperator(ctx, id)
}

func preventPostgresOperatorAdminLockout(ctx context.Context, tx *sql.Tx, role, status string, targetMFAEnabled, disablingAccount, disablingMFA bool) error {
	if role != "admin" || status != "active" {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id,mfa_enabled
		FROM app.operator_accounts
		WHERE role='admin' AND status='active'
		FOR UPDATE`)
	if err != nil {
		return err
	}
	defer rows.Close()
	activeAdmins := 0
	activeMFAAdmins := 0
	for rows.Next() {
		var id string
		var mfaEnabled bool
		if err = rows.Scan(&id, &mfaEnabled); err != nil {
			return err
		}
		activeAdmins++
		if mfaEnabled {
			activeMFAAdmins++
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if disablingAccount && activeAdmins <= 1 {
		return opsauth.ErrAdminLockout
	}
	if targetMFAEnabled && (disablingAccount || disablingMFA) && activeMFAAdmins <= 1 {
		return opsauth.ErrAdminLockout
	}
	return nil
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
	_, err := tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,metadata,occurred_at)
		VALUES ('operator',NULL,$1,'operator_account',$2,$3)`,
		audit.Action, payload, audit.Now,
	)
	return err
}

func postgresBoolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

const operatorAccountSelect = `
	SELECT id,display_name,role,status,token_hash,totp_secret,mfa_enabled,
		created_at,updated_at,last_authenticated_at
	FROM app.operator_accounts`

func scanOperatorAccount(row rowScanner) (opsauth.Account, error) {
	var account opsauth.Account
	var last sql.NullTime
	err := row.Scan(
		&account.ID, &account.DisplayName, &account.Role, &account.Status,
		&account.TokenHash, &account.TOTPSecret, &account.MFAEnabled,
		&account.CreatedAt, &account.UpdatedAt, &last,
	)
	if last.Valid {
		value := last.Time
		account.LastAuthenticatedAt = &value
	}
	return account, err
}
