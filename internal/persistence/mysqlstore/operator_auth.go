package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
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
