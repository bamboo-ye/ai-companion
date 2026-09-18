package document

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"strings"
	"time"
)

// The same persistence protocol is used by PostgreSQL and MySQL. Domain IDs
// are stored as text here so both backends share fencing/CAS semantics.
type WikiSQLStore struct {
	db      *sql.DB
	dialect string
}

func NewWikiSQLStore(db *sql.DB, dialect string) *WikiSQLStore { return &WikiSQLStore{db, dialect} }
func (s *WikiSQLStore) table(name string) string {
	if s.dialect == "postgres" {
		return "app." + name
	}
	return name
}
func (s *WikiSQLStore) query(q string) string {
	if s.dialect != "postgres" {
		return q
	}
	var out strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
func (s *WikiSQLStore) ListWikiPages(ctx context.Context, owners []string) ([]WikiPage, error) {
	result := []WikiPage{}
	for _, owner := range owners {
		rows, err := s.db.QueryContext(ctx, s.query("SELECT payload FROM "+s.table("wiki_pages")+" WHERE owner_id=? ORDER BY updated_at DESC"), owner)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var data []byte
			if err = rows.Scan(&data); err != nil {
				rows.Close()
				return nil, err
			}
			var p WikiPage
			if err = json.Unmarshal(data, &p); err != nil {
				rows.Close()
				return nil, err
			}
			p.OwnerID = owner
			result = append(result, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (s *WikiSQLStore) pageTx(ctx context.Context, tx *sql.Tx, pageID string) (WikiPage, error) {
	var data []byte
	var owner string
	err := tx.QueryRowContext(ctx, s.query("SELECT owner_id,payload FROM "+s.table("wiki_pages")+" WHERE id=? FOR UPDATE"), pageID).Scan(&owner, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return WikiPage{}, nil
	}
	if err != nil {
		return WikiPage{}, err
	}
	var p WikiPage
	err = json.Unmarshal(data, &p)
	p.OwnerID = owner
	return p, err
}
func (s *WikiSQLStore) saveTx(ctx context.Context, tx *sql.Tx, p WikiPage, expected int) (WikiPage, error) {
	old, err := s.pageTx(ctx, tx, p.ID)
	if err != nil {
		return WikiPage{}, err
	}
	if old.Version != expected || (old.Version > 0 && old.OwnerID != p.OwnerID) {
		return WikiPage{}, ErrWikiConflict
	}
	if old.Version > 0 && sameWikiContent(old, p) {
		return old, nil
	}
	p.Version = expected + 1
	data, err := json.Marshal(p)
	if err != nil {
		return WikiPage{}, err
	}
	if expected == 0 {
		_, err = tx.ExecContext(ctx, s.query("INSERT INTO "+s.table("wiki_pages")+" (id,owner_id,version,payload,updated_at) VALUES (?,?,?,?,?)"), p.ID, p.OwnerID, p.Version, string(data), p.UpdatedAt)
	} else {
		_, err = tx.ExecContext(ctx, s.query("UPDATE "+s.table("wiki_pages")+" SET version=?,payload=?,updated_at=? WHERE id=? AND version=?"), p.Version, string(data), p.UpdatedAt, p.ID, expected)
	}
	if err != nil {
		return WikiPage{}, err
	}
	_, err = tx.ExecContext(ctx, s.query("INSERT INTO "+s.table("wiki_versions")+" (page_id,owner_id,version,payload,created_at) VALUES (?,?,?,?,?)"), p.ID, p.OwnerID, p.Version, string(data), p.UpdatedAt)
	return p, err
}
func (s *WikiSQLStore) SaveWikiPage(ctx context.Context, p WikiPage, expected int) (WikiPage, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WikiPage{}, err
	}
	defer tx.Rollback()
	if expected > 0 && !p.Edited {
		for _, source := range p.Sources {
			if err = EnqueueWikiSQL(ctx, tx, s.dialect, p.OwnerID, source.DocumentID, p.UpdatedAt); err != nil {
				return WikiPage{}, err
			}
		}
	}
	p, err = s.saveTx(ctx, tx, p, expected)
	if err != nil {
		return WikiPage{}, err
	}
	return p, tx.Commit()
}
func (s *WikiSQLStore) WikiVersions(ctx context.Context, user, page string) ([]WikiPage, error) {
	rows, err := s.db.QueryContext(ctx, s.query("SELECT payload FROM "+s.table("wiki_versions")+" WHERE owner_id=? AND page_id=? ORDER BY version DESC LIMIT 50"), user, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WikiPage{}
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var p WikiPage
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		p.OwnerID = user
		out = append(out, p)
	}
	return out, rows.Err()
}

type wikiSQLExec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func EnqueueWikiSQL(ctx context.Context, exec wikiSQLExec, dialect, user, doc string, now time.Time) error {
	s := &WikiSQLStore{dialect: dialect}
	ownerSQL := "INSERT INTO " + s.table("wiki_owners") + " (owner_id) VALUES (?)"
	if dialect == "postgres" {
		ownerSQL += " ON CONFLICT DO NOTHING"
	} else {
		ownerSQL += " ON DUPLICATE KEY UPDATE owner_id=VALUES(owner_id)"
	}
	if _, err := exec.ExecContext(ctx, s.query(ownerSQL), user); err != nil {
		return err
	}

	q := "INSERT INTO " + s.table("wiki_jobs") + " (document_id,owner_id,status,attempts,next_attempt,lease_token) VALUES (?,?,'queued',0,?,'')"
	if dialect == "postgres" {
		q += " ON CONFLICT (document_id) DO UPDATE SET status='queued',attempts=0,next_attempt=EXCLUDED.next_attempt,lease_token='',lease_until=NULL,last_error=''"
	} else {
		q += " ON DUPLICATE KEY UPDATE status='queued',attempts=0,next_attempt=VALUES(next_attempt),lease_token='',lease_until=NULL,last_error=''"
	}
	_, err := exec.ExecContext(ctx, s.query(q), doc, user, now)
	return err
}
func (s *WikiSQLStore) EnqueueWiki(ctx context.Context, user, doc string, now time.Time) error {
	return EnqueueWikiSQL(ctx, s.db, s.dialect, user, doc, now)
}
func (s *WikiSQLStore) ClaimWiki(ctx context.Context, now time.Time) (WikiJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WikiJob{}, err
	}
	defer tx.Rollback()
	var j WikiJob

	var owner string
	err = tx.QueryRowContext(ctx, s.query("SELECT o.owner_id FROM "+s.table("wiki_owners")+" o WHERE EXISTS (SELECT 1 FROM "+s.table("wiki_jobs")+" j WHERE j.owner_id=o.owner_id AND ((j.status='queued' AND j.next_attempt<=?) OR (j.status='processing' AND j.lease_until<=?))) AND NOT EXISTS (SELECT 1 FROM "+s.table("wiki_jobs")+" j WHERE j.owner_id=o.owner_id AND j.status='processing' AND j.lease_until>?) ORDER BY o.owner_id LIMIT 1 FOR UPDATE SKIP LOCKED"), now, now, now).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return WikiJob{}, ErrNotFound
	}
	if err != nil {
		return WikiJob{}, err
	}
	err = tx.QueryRowContext(ctx, s.query("SELECT document_id,owner_id,attempts FROM "+s.table("wiki_jobs")+" j WHERE j.owner_id=? AND ((j.status='queued' AND j.next_attempt<=?) OR (j.status='processing' AND j.lease_until<=?)) AND NOT EXISTS (SELECT 1 FROM "+s.table("wiki_jobs")+" active WHERE active.owner_id=j.owner_id AND active.document_id<>j.document_id AND active.status='processing' AND active.lease_until>?) ORDER BY next_attempt LIMIT 1 FOR UPDATE SKIP LOCKED"), owner, now, now, now).Scan(&j.DocumentID, &j.UserID, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return WikiJob{}, ErrNotFound
	}
	if err != nil {
		return WikiJob{}, err
	}
	j.Token, err = id.New()
	if err != nil {
		return WikiJob{}, err
	}
	j.Attempts++
	_, err = tx.ExecContext(ctx, s.query("UPDATE "+s.table("wiki_jobs")+" SET status='processing',attempts=?,lease_token=?,lease_until=? WHERE document_id=?"), j.Attempts, j.Token, now.Add(10*time.Minute), j.DocumentID)
	if err != nil {
		return WikiJob{}, err
	}
	return j, tx.Commit()
}
func (s *WikiSQLStore) FinishWiki(ctx context.Context, j WikiJob, pages []WikiPage, code string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerLock string
	if err = tx.QueryRowContext(ctx, s.query("SELECT owner_id FROM "+s.table("wiki_owners")+" WHERE owner_id=? FOR UPDATE"), j.UserID).Scan(&ownerLock); err != nil {
		return err
	}
	var token, status string
	var lease sql.NullTime
	err = tx.QueryRowContext(ctx, s.query("SELECT lease_token,status,lease_until FROM "+s.table("wiki_jobs")+" WHERE document_id=? FOR UPDATE"), j.DocumentID).Scan(&token, &status, &lease)
	if err != nil {
		return err
	}
	if token != j.Token || status != "processing" || !lease.Valid || !lease.Time.After(now) {
		return ErrWikiConflict
	}
	if code != "" {
		status = "queued"
		if j.Attempts >= 5 {
			status = "failed"
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE "+s.table("wiki_jobs")+" SET status=?,last_error=?,lease_until=NULL,next_attempt=? WHERE document_id=?"), status, code, now.Add(time.Duration(min(j.Attempts*j.Attempts, 60))*time.Minute), j.DocumentID)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if pages != nil {
		for _, p := range pages {
			if p.OwnerID != j.UserID {
				return ErrValidation
			}
			old, e := s.pageTx(ctx, tx, p.ID)
			if e != nil {
				return e
			}
			if old.Edited {
				continue
			}
			if _, e = s.saveTx(ctx, tx, p, old.Version); e != nil {
				return e
			}
		}
		// Retire superseded generated pages for this source, retaining revision history.
		rows, err := tx.QueryContext(ctx, s.query("SELECT id,payload FROM "+s.table("wiki_pages")+" WHERE owner_id=? FOR UPDATE"), j.UserID)
		if err != nil {
			return err
		}
		retire := []string{}
		keep := map[string]bool{}
		for _, p := range pages {
			keep[p.ID] = true
		}
		for rows.Next() {
			var key string
			var data []byte
			if err = rows.Scan(&key, &data); err != nil {
				rows.Close()
				return err
			}
			var p WikiPage
			if err = json.Unmarshal(data, &p); err != nil {
				rows.Close()
				return err
			}
			if !p.Edited && (strings.HasSuffix(p.Compiler, ":aggregate") || (len(p.Sources) == 1 && p.Sources[0].DocumentID == j.DocumentID)) && !keep[key] {
				retire = append(retire, key)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, key := range retire {
			if _, err = tx.ExecContext(ctx, s.query("DELETE FROM "+s.table("wiki_pages")+" WHERE id=?"), key); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, s.query("UPDATE "+s.table("wiki_jobs")+" SET status='completed',lease_until=NULL,last_error='' WHERE document_id=?"), j.DocumentID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *WikiSQLStore) SaveWikiFeedback(ctx context.Context, user string, f WikiFeedback) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ownerLock string
	if err = tx.QueryRowContext(ctx, s.query("SELECT owner_id FROM "+s.table("wiki_owners")+" WHERE owner_id=? FOR UPDATE"), user).Scan(&ownerLock); err != nil {
		return err
	}
	p, err := s.pageTx(ctx, tx, f.PageID)
	if err != nil {
		return err
	}
	if p.OwnerID != user {
		return ErrNotFound
	}
	if p.Version != f.Version {
		return ErrWikiConflict
	}
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.query("INSERT INTO "+s.table("wiki_feedback")+" (id,owner_id,page_id,version,payload,created_at) VALUES (?,?,?,?,?,?)"), f.ID, user, f.PageID, f.Version, string(data), f.CreatedAt); err != nil {
		return err
	}
	if f.Rating != "helpful" {
		for _, source := range p.Sources {
			if err = EnqueueWikiSQL(ctx, tx, s.dialect, user, source.DocumentID, f.CreatedAt); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
