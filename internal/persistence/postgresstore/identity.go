package postgresstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/windcry1/ai-companion/internal/identity"
)

var _ identity.Store = (*Store)(nil)

func (s *Store) CreateUser(ctx context.Context, user identity.User) error {
	if user.Status == "" {
		user.Status = "active"
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app.users (
			id,email,password_hash,display_name,timezone,locale,status,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		user.ID, user.Email, user.PasswordHash, user.DisplayName, user.Timezone,
		user.Locale, user.Status, user.CreatedAt, user.UpdatedAt,
	)
	if isUniqueViolation(err) {
		return identity.ErrConflict
	}
	return err
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (identity.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE LOWER(email)=LOWER($1) AND status='active'`, email))
}

func (s *Store) GetUser(ctx context.Context, userID string) (identity.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+` WHERE id=$1 AND status='active'`, userID))
}

func (s *Store) UpsertDevice(ctx context.Context, device identity.Device) (identity.Device, error) {
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO app.user_devices (
			id,user_id,device_key,name,platform,timezone,last_seen_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (user_id,device_key) DO UPDATE SET
			name=EXCLUDED.name,
			platform=EXCLUDED.platform,
			timezone=EXCLUDED.timezone,
			last_seen_at=EXCLUDED.last_seen_at
		RETURNING id::text,user_id::text,device_key,name,platform,timezone,last_seen_at`,
		device.ID, device.UserID, device.DeviceKey, device.Name, device.Platform,
		device.Timezone, device.LastSeen,
	).Scan(
		&device.ID, &device.UserID, &device.DeviceKey, &device.Name,
		&device.Platform, &device.Timezone, &device.LastSeen,
	)
	return device, err
}

func (s *Store) CreateSession(ctx context.Context, session identity.Session) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app.refresh_sessions (
			id,user_id,device_id,token_hash,expires_at,last_used_at,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		session.ID, session.UserID, session.DeviceID, session.TokenHash,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt,
	)
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (identity.Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE id=$1`, id))
}

func (s *Store) FindSessionByTokenHash(ctx context.Context, hash string) (identity.Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE token_hash=$1`, hash))
}

func (s *Store) RotateSession(ctx context.Context, currentID string, next identity.Session, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanSession(tx.QueryRowContext(ctx, sessionSelect+` WHERE id=$1 FOR UPDATE`, currentID))
	if err != nil || current.RevokedAt != nil {
		return identity.ErrUnauthorized
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.refresh_sessions (
			id,user_id,device_id,token_hash,expires_at,last_used_at,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		next.ID, next.UserID, next.DeviceID, next.TokenHash,
		next.ExpiresAt, next.LastUsedAt, next.CreatedAt,
	); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE app.refresh_sessions
		SET revoked_at=$1,last_used_at=$1,rotated_to_id=$2
		WHERE id=$3 AND revoked_at IS NULL`,
		now, next.ID, currentID,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return identity.ErrUnauthorized
	}
	return tx.Commit()
}

func (s *Store) RevokeSession(ctx context.Context, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.refresh_sessions
		SET revoked_at=COALESCE(revoked_at,$1)
		WHERE id=$2`,
		now, id,
	)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return identity.ErrNotFound
	}
	return nil
}

func (s *Store) RevokeUserSessions(ctx context.Context, userID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE app.refresh_sessions
		SET revoked_at=$1
		WHERE user_id=$2 AND revoked_at IS NULL`,
		now, userID,
	)
	return err
}

const userSelect = `
	SELECT id::text,email,password_hash,display_name,timezone,locale,status,created_at,updated_at
	FROM app.users`

const sessionSelect = `
	SELECT
		id::text,user_id::text,device_id::text,token_hash,expires_at,created_at,
		last_used_at,revoked_at,COALESCE(rotated_to_id::text,'')
	FROM app.refresh_sessions`

type rowScanner interface {
	Scan(...any) error
}

func scanUser(row rowScanner) (identity.User, error) {
	var user identity.User
	err := row.Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.Timezone, &user.Locale, &user.Status, &user.CreatedAt, &user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		err = identity.ErrNotFound
	}
	return user, err
}

func scanSession(row rowScanner) (identity.Session, error) {
	var value identity.Session
	var revoked sql.NullTime
	err := row.Scan(
		&value.ID, &value.UserID, &value.DeviceID, &value.TokenHash,
		&value.ExpiresAt, &value.CreatedAt, &value.LastUsedAt, &revoked,
		&value.RotatedToID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return value, identity.ErrNotFound
	}
	if revoked.Valid {
		value.RevokedAt = &revoked.Time
	}
	return value, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
