package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/windcry1/ai-companion/internal/identity"
)

func (s *Store) CreateUser(ctx context.Context, user identity.User) error {
	if user.Status == "" {
		user.Status = "active"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (id,email,password_hash,display_name,timezone,locale,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),?,?,?,?,?,?,?,?)`, user.ID, user.Email, user.PasswordHash, user.DisplayName, user.Timezone, user.Locale, user.Status, user.CreatedAt, user.UpdatedAt)
	if isDuplicate(err) {
		return identity.ErrConflict
	}
	return err
}

func (s *Store) FindUserByEmail(ctx context.Context, email string) (identity.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id),email,password_hash,display_name,timezone,locale,status,created_at,updated_at FROM users WHERE email=? AND status='active'`, email))
}

func (s *Store) GetUser(ctx context.Context, userID string) (identity.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id),email,password_hash,display_name,timezone,locale,status,created_at,updated_at FROM users WHERE id=UUID_TO_BIN(?) AND status='active'`, userID))
}

func (s *Store) ChangePassword(ctx context.Context, userID, currentHash, nextHash, keepSessionID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND password_hash=? AND status='active' AND EXISTS (SELECT 1 FROM refresh_sessions WHERE id=UUID_TO_BIN(?) AND user_id=users.id AND revoked_at IS NULL)`, nextHash, now, userID, currentHash, keepSessionID)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return identity.ErrUnauthorized
	}
	if _, err = tx.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at=?,last_used_at=? WHERE user_id=UUID_TO_BIN(?) AND id<>UUID_TO_BIN(?) AND revoked_at IS NULL`, now, now, userID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertDevice(ctx context.Context, device identity.Device) (identity.Device, error) {
	_, err := s.db.ExecContext(ctx, `INSERT INTO user_devices (id,user_id,device_key,name,platform,timezone,last_seen_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?) ON DUPLICATE KEY UPDATE name=VALUES(name),platform=VALUES(platform),timezone=VALUES(timezone),last_seen_at=VALUES(last_seen_at)`, device.ID, device.UserID, device.DeviceKey, device.Name, device.Platform, device.Timezone, device.LastSeen)
	if err != nil {
		return identity.Device{}, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),device_key,name,platform,timezone,last_seen_at FROM user_devices WHERE user_id=UUID_TO_BIN(?) AND device_key=?`, device.UserID, device.DeviceKey).Scan(&device.ID, &device.UserID, &device.DeviceKey, &device.Name, &device.Platform, &device.Timezone, &device.LastSeen)
	return device, err
}

func (s *Store) CreateSession(ctx context.Context, session identity.Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO refresh_sessions (id,user_id,device_id,token_hash,expires_at,last_used_at,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?)`, session.ID, session.UserID, session.DeviceID, session.TokenHash, session.ExpiresAt, session.LastUsedAt, session.CreatedAt)
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (identity.Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE id=UUID_TO_BIN(?)`, id))
}

func (s *Store) FindSessionByTokenHash(ctx context.Context, hash string) (identity.Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, sessionSelect+` WHERE token_hash=?`, hash))
}

func (s *Store) RotateSession(ctx context.Context, currentID string, next identity.Session, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := scanSession(tx.QueryRowContext(ctx, sessionSelect+` WHERE id=UUID_TO_BIN(?) FOR UPDATE`, currentID))
	if err != nil || current.RevokedAt != nil {
		return identity.ErrUnauthorized
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO refresh_sessions (id,user_id,device_id,token_hash,expires_at,last_used_at,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?)`, next.ID, next.UserID, next.DeviceID, next.TokenHash, next.ExpiresAt, next.LastUsedAt, next.CreatedAt)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at=?,last_used_at=?,rotated_to_id=UUID_TO_BIN(?) WHERE id=UUID_TO_BIN(?) AND revoked_at IS NULL`, now, now, next.ID, currentID)
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
	result, err := s.db.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at=COALESCE(revoked_at,?) WHERE id=UUID_TO_BIN(?)`, now, id)
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
	_, err := s.db.ExecContext(ctx, `UPDATE refresh_sessions SET revoked_at=? WHERE user_id=UUID_TO_BIN(?) AND revoked_at IS NULL`, now, userID)
	return err
}

const sessionSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),BIN_TO_UUID(device_id),token_hash,expires_at,created_at,last_used_at,revoked_at,COALESCE(BIN_TO_UUID(rotated_to_id),'') FROM refresh_sessions`

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (identity.User, error) {
	var user identity.User
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName, &user.Timezone, &user.Locale, &user.Status, &user.CreatedAt, &user.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = identity.ErrNotFound
	}
	return user, err
}
func scanSession(row rowScanner) (identity.Session, error) {
	var value identity.Session
	var revoked sql.NullTime
	err := row.Scan(&value.ID, &value.UserID, &value.DeviceID, &value.TokenHash, &value.ExpiresAt, &value.CreatedAt, &value.LastUsedAt, &revoked, &value.RotatedToID)
	if errors.Is(err, sql.ErrNoRows) {
		return value, identity.ErrNotFound
	}
	if revoked.Valid {
		value.RevokedAt = &revoked.Time
	}
	return value, err
}
func isDuplicate(err error) bool {
	var mysqlErr *driver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
