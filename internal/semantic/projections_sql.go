package semantic

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"strings"
	"time"
)

type SQLProjectionStore struct {
	db      *sql.DB
	dialect string
}

func NewSQLProjectionStore(db *sql.DB, dialect string) *SQLProjectionStore {
	return &SQLProjectionStore{db: db, dialect: dialect}
}
func (s *SQLProjectionStore) table() string {
	if s.dialect == "postgres" {
		return "app.knowledge_projections"
	}
	return "knowledge_projections"
}
func (s *SQLProjectionStore) q(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	i := 0
	return replacePlaceholders(q, &i)
}
func replacePlaceholders(q string, i *int) string {
	var b strings.Builder
	for _, r := range q {
		if r == '?' {
			*i++
			fmt.Fprintf(&b, "$%d", *i)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func (s *SQLProjectionStore) Queue(ctx context.Context, items []Projection, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range items {
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		q := "INSERT INTO " + s.table() + " (id,owner_id,kind,source_id,spec_id,payload,status,attempts,available_at,last_seen) VALUES (?,?,?,?,?,?,'queued',0,?,?)"
		if s.dialect == "postgres" {
			q += " ON CONFLICT(id) DO UPDATE SET status=CASE WHEN " + s.table() + ".last_seen<EXCLUDED.last_seen-interval '24 hours' THEN 'queued' ELSE " + s.table() + ".status END, attempts=CASE WHEN " + s.table() + ".last_seen<EXCLUDED.last_seen-interval '24 hours' THEN 0 ELSE " + s.table() + ".attempts END, last_seen=EXCLUDED.last_seen"
		} else {
			q += " ON DUPLICATE KEY UPDATE status=IF(last_seen<VALUES(last_seen)-INTERVAL 24 HOUR,'queued',status),attempts=IF(last_seen<VALUES(last_seen)-INTERVAL 24 HOUR,0,attempts),last_seen=VALUES(last_seen)"
		}
		if _, err = tx.ExecContext(ctx, s.q(q), p.Key, p.Owner, p.Kind, p.SourceID, p.Spec, string(payload), now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *SQLProjectionStore) Lookup(ctx context.Context, keys []string) (map[string][]float32, error) {
	out := map[string][]float32{}
	for start := 0; start < len(keys); start += 256 {
		batch := keys[start:min(start+256, len(keys))]
		args := []any{}
		for _, k := range batch {
			args = append(args, k)
		}
		rows, err := s.db.QueryContext(ctx, s.q("SELECT id,vector FROM "+s.table()+" WHERE status='ready' AND id IN ("+strings.TrimRight(strings.Repeat("?,", len(batch)), ",")+")"), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			var b []byte
			if err = rows.Scan(&key, &b); err != nil {
				rows.Close()
				return nil, err
			}
			var v []float32
			if err = json.Unmarshal(b, &v); err != nil {
				rows.Close()
				return nil, err
			}
			out[key] = v
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *SQLProjectionStore) ReadyKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	for start := 0; start < len(keys); start += 256 {
		batch := keys[start:min(start+256, len(keys))]
		args := []any{}
		for _, key := range batch {
			args = append(args, key)
		}
		rows, err := s.db.QueryContext(ctx, s.q("SELECT id FROM "+s.table()+" WHERE status='ready' AND id IN ("+strings.TrimRight(strings.Repeat("?,", len(batch)), ",")+")"), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key string
			if err = rows.Scan(&key); err != nil {
				rows.Close()
				return nil, err
			}
			out[key] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *SQLProjectionStore) States(ctx context.Context, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for start := 0; start < len(keys); start += 256 {
		batch := keys[start:min(start+256, len(keys))]
		args := []any{}
		for _, key := range batch {
			args = append(args, key)
		}
		rows, err := s.db.QueryContext(ctx, s.q("SELECT id,status FROM "+s.table()+" WHERE id IN ("+strings.TrimRight(strings.Repeat("?,", len(batch)), ",")+")"), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key, state string
			if err = rows.Scan(&key, &state); err != nil {
				rows.Close()
				return nil, err
			}
			out[key] = state
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *SQLProjectionStore) Claim(ctx context.Context, spec string, now time.Time) (Projection, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Projection{}, false, err
	}
	defer tx.Rollback()
	var data []byte
	var attempts int
	err = tx.QueryRowContext(ctx, s.q("SELECT payload,attempts FROM "+s.table()+" WHERE spec_id=? AND available_at<=? AND (status='queued' OR (status='processing' AND lease_expires<=?)) ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED"), spec, now, now).Scan(&data, &attempts)
	if err == sql.ErrNoRows {
		return Projection{}, false, nil
	}
	if err != nil {
		return Projection{}, false, err
	}
	var p Projection
	if err = json.Unmarshal(data, &p); err != nil {
		return p, false, err
	}
	p.Lease, err = id.New()
	if err != nil {
		return p, false, err
	}
	p.Attempts = attempts + 1
	_, err = tx.ExecContext(ctx, s.q("UPDATE "+s.table()+" SET status='processing',attempts=?,lease_token=?,lease_expires=? WHERE id=?"), p.Attempts, p.Lease, now.Add(2*time.Minute), p.Key)
	if err != nil {
		return p, false, err
	}
	return p, true, tx.Commit()
}
func (s *SQLProjectionStore) Finish(ctx context.Context, p Projection, vector []float32, code string, now time.Time) error {
	var result sql.Result
	var err error
	if code == "obsolete" {
		result, err = s.db.ExecContext(ctx, s.q("DELETE FROM "+s.table()+" WHERE id=? AND status='processing' AND lease_token=? AND lease_expires>?"), p.Key, p.Lease, now)
	} else {
		state := "ready"
		available := now
		if code != "" {
			state = "queued"
			available = now.Add(time.Duration(1<<min(p.Attempts, 8)) * time.Second)
			if p.Attempts >= 5 {
				state = "failed"
			}
		}
		b, e := json.Marshal(vector)
		if e != nil {
			return e
		}
		result, err = s.db.ExecContext(ctx, s.q("UPDATE "+s.table()+" SET status=?,vector=?,failure_code=?,available_at=?,lease_token=NULL,lease_expires=NULL WHERE id=? AND status='processing' AND lease_token=? AND lease_expires>?"), state, string(b), code, available, p.Key, p.Lease, now)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrProjectionLease
	}
	return nil
}
func (s *SQLProjectionStore) Renew(ctx context.Context, p Projection, now time.Time) error {
	r, err := s.db.ExecContext(ctx, s.q("UPDATE "+s.table()+" SET lease_expires=? WHERE id=? AND status='processing' AND lease_token=? AND lease_expires>?"), now.Add(2*time.Minute), p.Key, p.Lease, now)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrProjectionLease
	}
	return nil
}
func (s *SQLProjectionStore) DeleteSource(ctx context.Context, kind, owner, source string) error {
	_, err := s.db.ExecContext(ctx, s.q("DELETE FROM "+s.table()+" WHERE kind=? AND owner_id=? AND (?='' OR source_id=?)"), kind, owner, source, source)
	return err
}
func (s *SQLProjectionStore) Prune(ctx context.Context, before time.Time, cleanup func(context.Context, []Projection) error) (int64, error) {
	var total int64
	for {
		n, err := s.pruneBatch(ctx, before, cleanup)
		total += n
		if err != nil || n == 0 {
			return total, err
		}
	}
}
func (s *SQLProjectionStore) pruneBatch(ctx context.Context, before time.Time, cleanup func(context.Context, []Projection) error) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.q("SELECT payload FROM "+s.table()+" WHERE last_seen<? AND (status<>'processing' OR lease_expires<?) ORDER BY id LIMIT 100 FOR UPDATE SKIP LOCKED"), before, before)
	if err != nil {
		return 0, err
	}
	items := []Projection{}
	for rows.Next() {
		var b []byte
		var p Projection
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return 0, err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, nil
	}
	if cleanup != nil {
		if err = cleanup(ctx, items); err != nil {
			return 0, err
		}
	}
	for _, p := range items {
		if _, err = tx.ExecContext(ctx, s.q("DELETE FROM "+s.table()+" WHERE id=?"), p.Key); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(items)), nil
}

func (s *SQLProjectionStore) Stats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT status,COUNT(*) FROM "+s.table()+" GROUP BY status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var state string
		var n int64
		if err = rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}
func (s *SQLProjectionStore) Retry(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, s.q("UPDATE "+s.table()+" SET status='queued',attempts=0,available_at=? WHERE status='failed'"), time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
func (s *SQLProjectionStore) Rebuild(ctx context.Context, specs []string) (int64, error) {
	if len(specs) == 0 {
		return 0, nil
	}
	args := []any{time.Now().UTC()}
	for _, spec := range specs {
		args = append(args, spec)
	}
	r, err := s.db.ExecContext(ctx, s.q("UPDATE "+s.table()+" SET status='queued',attempts=0,available_at=?,vector=NULL WHERE status<>'processing' AND spec_id IN ("+strings.TrimRight(strings.Repeat("?,", len(specs)), ",")+")"), args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}
