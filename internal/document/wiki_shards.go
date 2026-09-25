package document

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *WikiSQLStore) LoadWikiShard(ctx context.Context, user, doc, key string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, s.query("SELECT payload FROM "+s.table("wiki_shards")+" WHERE owner_id=? AND document_id=? AND cache_key=? AND expires_at>?"), user, doc, key, time.Now().UTC()).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return data, err
}
func (s *WikiSQLStore) SaveWikiShard(ctx context.Context, user, doc, key string, data []byte) error {
	if len(data) > 256000 {
		return ErrValidation
	}
	// A bounded retention window permits crash recovery without retaining old
	// source/model revisions indefinitely. No shard bypasses source ACL checks.
	if _, err := s.db.ExecContext(ctx, s.query("DELETE FROM "+s.table("wiki_shards")+" WHERE expires_at<=?"), time.Now().UTC()); err != nil {
		return err
	}
	q := "INSERT INTO " + s.table("wiki_shards") + " (owner_id,document_id,cache_key,payload,expires_at) VALUES (?,?,?,?,?)"
	if s.dialect == "postgres" {
		q += " ON CONFLICT (owner_id,document_id,cache_key) DO UPDATE SET payload=EXCLUDED.payload,expires_at=EXCLUDED.expires_at"
	} else {
		q += " ON DUPLICATE KEY UPDATE payload=VALUES(payload),expires_at=VALUES(expires_at)"
	}
	_, err := s.db.ExecContext(ctx, s.query(q), user, doc, key, string(data), time.Now().UTC().Add(24*time.Hour))
	return err
}

type wikiShard struct {
	data    []byte
	expires time.Time
}

func (s *WikiMemoryStore) LoadWikiShard(_ context.Context, user, doc, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.shards[user+":"+doc+":"+key]
	if !ok || time.Now().After(entry.expires) {
		return nil, ErrNotFound
	}
	return append([]byte(nil), entry.data...), nil
}
func (s *WikiMemoryStore) SaveWikiShard(_ context.Context, user, doc, key string, data []byte) error {
	if len(data) > 256000 {
		return ErrValidation
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shards == nil {
		s.shards = map[string]wikiShard{}
	}
	for k, v := range s.shards {
		if time.Now().After(v.expires) {
			delete(s.shards, k)
		}
	}
	s.shards[user+":"+doc+":"+key] = wikiShard{append([]byte(nil), data...), time.Now().Add(24 * time.Hour)}
	return nil
}
