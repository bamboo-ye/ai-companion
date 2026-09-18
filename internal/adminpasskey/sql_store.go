package adminpasskey

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/windcry1/ai-companion/internal/opsauth"
)

type SQLStore struct {
	db       *sql.DB
	postgres bool
}

func NewSQLStore(db *sql.DB, postgres bool) *SQLStore { return &SQLStore{db: db, postgres: postgres} }
func (s *SQLStore) query(q string) string {
	table := "operator_passkey_records"
	if s.postgres {
		table = "ops.operator_passkey_records"
	}
	q = strings.ReplaceAll(q, "{table}", table)
	accounts := "operator_accounts"
	if s.postgres {
		accounts = "app.operator_accounts"
	}
	q = strings.ReplaceAll(q, "{accounts}", accounts)
	if s.postgres {
		n := 0
		for strings.Contains(q, "?") {
			n++
			q = strings.Replace(q, "?", fmt.Sprintf("$%d", n), 1)
		}
	}
	return q
}

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner) (Record, error) {
	var r Record
	err := row.Scan(&r.Key, &r.Owner, &r.Kind, &r.Data, &r.Expires, &r.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDenied
	}
	return r, err
}

const selectRecord = "SELECT record_key,owner_id,kind,data,expires_at,revision FROM {table}"

func (s *SQLStore) Get(ctx context.Context, key string) (Record, error) {
	return scanRecord(s.db.QueryRowContext(ctx, s.query(selectRecord+" WHERE record_key=?"), key))
}
func (s *SQLStore) List(ctx context.Context, kind, owner string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, s.query(selectRecord+" WHERE kind=? AND owner_id=? ORDER BY record_key"), kind, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *SQLStore) Insert(ctx context.Context, r Record) error {
	if r.Kind == "credential" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// Serialize enrollment with disable, token rotation and recovery. A request
		// authorized before revocation must not insert a usable credential after it.
		a, err := s.lockAccount(ctx, tx, r.Owner)
		if err != nil {
			return err
		}
		var credential credentialRecord
		if json.Unmarshal(r.Data, &credential) != nil || credential.EnrollmentVersion != accountVersion(a) {
			return ErrDenied
		}
		var count int
		if err = tx.QueryRowContext(ctx, s.query("SELECT COUNT(*) FROM {table} WHERE owner_id=? AND kind='credential'"), r.Owner).Scan(&count); err != nil {
			return err
		}
		if count >= 10 {
			return ErrDenied
		}
		if err = s.insert(ctx, tx, r); err != nil {
			return err
		}
		return tx.Commit()
	}
	return s.insert(ctx, s.db, r)
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (s *SQLStore) insert(ctx context.Context, db executor, r Record) error {
	_, err := db.ExecContext(ctx, s.query("INSERT INTO {table} (record_key,owner_id,kind,data,expires_at,revision) VALUES (?,?,?,?,?,1)"), r.Key, r.Owner, r.Kind, string(r.Data), r.Expires)
	var pg *pgconn.PgError
	var my *mysql.MySQLError
	if (errors.As(err, &pg) && pg.Code == "23505") || (errors.As(err, &my) && my.Number == 1062) {
		return ErrConflict
	}
	return err
}

func (s *SQLStore) lockAccount(ctx context.Context, tx *sql.Tx, owner string) (opsauth.Account, error) {
	a := opsauth.Account{ID: owner}
	err := tx.QueryRowContext(ctx, s.query("SELECT status,token_hash,session_version FROM {accounts} WHERE id=? FOR UPDATE"), owner).Scan(&a.Status, &a.TokenHash, &a.SessionVersion)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && a.Status != "active") {
		return a, ErrDenied
	}
	return a, err
}

// RecoveryStore is deliberately available only to the database-authorized CLI.
type RecoveryStore interface {
	RecoverAccount(context.Context, string, string) error
}

func (s *SQLStore) RecoverAccount(ctx context.Context, owner, reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 512 {
		return opsauth.ErrValidation
	}
	token, err := randomSecret()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.lockAccount(ctx, tx, owner); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, s.query("UPDATE {accounts} SET token_hash=?,session_version=session_version+1,updated_at=? WHERE id=?"), opsauth.HashToken(token), now, owner); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.query("DELETE FROM {table} WHERE owner_id=? AND kind IN ('credential','session','invite','register','login')"), owner); err != nil {
		return err
	}
	if err = s.insert(ctx, tx, Record{Key: Key("audit", token), Owner: owner, Kind: "audit", Data: marshal(map[string]any{"action": "passkey.account.recovered", "actor": "admin-invite-cli", "reason": reason, "at": now}), Expires: now.Add(90 * 24 * time.Hour)}); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) Update(ctx context.Context, r Record) error {
	result, err := s.db.ExecContext(ctx, s.query("UPDATE {table} SET data=?,expires_at=?,revision=revision+1 WHERE record_key=? AND revision=?"), string(r.Data), r.Expires, r.Key, r.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *SQLStore) Take(ctx context.Context, key string) (Record, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer tx.Rollback()
	r, err := scanRecord(tx.QueryRowContext(ctx, s.query(selectRecord+" WHERE record_key=? FOR UPDATE"), key))
	if err != nil {
		return Record{}, err
	}
	if _, err = tx.ExecContext(ctx, s.query("DELETE FROM {table} WHERE record_key=?"), key); err != nil {
		return Record{}, err
	}
	return r, tx.Commit()
}
func (s *SQLStore) DeleteOwner(ctx context.Context, kind, owner string) error {
	_, err := s.db.ExecContext(ctx, s.query("DELETE FROM {table} WHERE kind=? AND owner_id=?"), kind, owner)
	return err
}
func (s *SQLStore) Purge(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.query("DELETE FROM {table} WHERE expires_at<=?"), now)
	return err
}
