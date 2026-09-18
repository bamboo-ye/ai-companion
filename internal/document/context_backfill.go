package document

import (
	"context"
	"time"
)

type ContextBackfill struct {
	Queued     int    `json:"queued"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}
type ContextBackfillStore interface {
	QueueContextBackfill(context.Context, string, int, time.Time) (ContextBackfill, error)
}

func (s *Service) BackfillContext(ctx context.Context, cursor string) (ContextBackfill, error) {
	store, ok := s.store.(ContextBackfillStore)
	if !ok {
		return ContextBackfill{}, ErrValidation
	}
	return store.QueueContextBackfill(ctx, cursor, 200, s.now().UTC())
}
func (s *WikiSQLStore) QueueContextBackfill(ctx context.Context, cursor string, limit int, now time.Time) (ContextBackfill, error) {
	limit = min(max(limit, 1), 200)
	docID, userID := "id::text", "user_id::text"
	if s.dialect != "postgres" {
		docID, userID = "BIN_TO_UUID(id)", "BIN_TO_UUID(user_id)"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContextBackfill{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.query("SELECT "+docID+","+userID+" FROM "+s.table("documents")+" WHERE ingest_status='ready' AND deleted_at IS NULL AND "+docID+">? ORDER BY "+docID+" LIMIT ?"), cursor, limit+1)
	if err != nil {
		return ContextBackfill{}, err
	}
	jobs := []WikiJob{}
	for rows.Next() {
		var j WikiJob
		if err = rows.Scan(&j.DocumentID, &j.UserID); err != nil {
			rows.Close()
			return ContextBackfill{}, err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ContextBackfill{}, err
	}
	result := ContextBackfill{HasMore: len(jobs) > limit}
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	for _, j := range jobs {
		if err = EnqueueWikiSQL(ctx, tx, s.dialect, j.UserID, j.DocumentID, now); err != nil {
			return ContextBackfill{}, err
		}
		result.Queued++
		result.NextCursor = j.DocumentID
	}
	return result, tx.Commit()
}
