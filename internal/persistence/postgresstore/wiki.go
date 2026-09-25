package postgresstore

import (
	"context"
	"github.com/windcry1/ai-companion/internal/document"
	"time"
)

func (s *Store) ListWikiPages(ctx context.Context, owners []string) ([]document.WikiPage, error) {
	return document.NewWikiSQLStore(s.db, "postgres").ListWikiPages(ctx, owners)
}
func (s *Store) SaveWikiPage(ctx context.Context, p document.WikiPage, expected int) (document.WikiPage, error) {
	return document.NewWikiSQLStore(s.db, "postgres").SaveWikiPage(ctx, p, expected)
}
func (s *Store) WikiVersions(ctx context.Context, user, page string) ([]document.WikiPage, error) {
	return document.NewWikiSQLStore(s.db, "postgres").WikiVersions(ctx, user, page)
}
func (s *Store) EnqueueWiki(ctx context.Context, user, doc string, now time.Time) error {
	return document.NewWikiSQLStore(s.db, "postgres").EnqueueWiki(ctx, user, doc, now)
}
func (s *Store) ClaimWiki(ctx context.Context, now time.Time) (document.WikiJob, error) {
	return document.NewWikiSQLStore(s.db, "postgres").ClaimWiki(ctx, now)
}
func (s *Store) FinishWiki(ctx context.Context, job document.WikiJob, pages []document.WikiPage, code string, now time.Time) error {
	return document.NewWikiSQLStore(s.db, "postgres").FinishWiki(ctx, job, pages, code, now)
}
func (s *Store) SaveWikiFeedback(ctx context.Context, user string, f document.WikiFeedback) error {
	return document.NewWikiSQLStore(s.db, "postgres").SaveWikiFeedback(ctx, user, f)
}

func (s *Store) QueueContextBackfill(ctx context.Context, cursor string, limit int, now time.Time) (document.ContextBackfill, error) {
	return document.NewWikiSQLStore(s.db, "postgres").QueueContextBackfill(ctx, cursor, limit, now)
}

func (s *Store) WikiStats(ctx context.Context) (map[string]int64, error) {
	return document.NewWikiSQLStore(s.db, "postgres").WikiStats(ctx)
}

func (s *Store) LoadWikiShard(ctx context.Context, user, doc, key string) ([]byte, error) {
	return document.NewWikiSQLStore(s.db, "postgres").LoadWikiShard(ctx, user, doc, key)
}
func (s *Store) SaveWikiShard(ctx context.Context, user, doc, key string, data []byte) error {
	return document.NewWikiSQLStore(s.db, "postgres").SaveWikiShard(ctx, user, doc, key, data)
}
