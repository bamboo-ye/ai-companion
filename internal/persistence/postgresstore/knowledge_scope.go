package postgresstore

import (
	"context"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/memory"
)

func (s *Store) ListKnowledgeDocuments(ctx context.Context, user, workspace, cursor string, limit int) ([]document.Document, error) {
	rows, err := s.db.QueryContext(ctx, documentSelect+` WHERE d.ingest_status<>'deleted' AND f.status='active' AND d.id>COALESCE(NULLIF($3,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) AND (($2='' AND d.user_id=NULLIF($1,'')::uuid) OR ($2<>'' AND EXISTS (SELECT 1 FROM app.workspace_document_shares ks WHERE ks.document_id=d.id AND ks.workspace_id=NULLIF($2,'')::uuid))) ORDER BY d.id LIMIT $4`, user, workspace, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []document.Document{}
	for rows.Next() {
		item, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *Store) ListKnowledgeMemories(ctx context.Context, user, cursor string, limit int) ([]memory.Memory, error) {
	rows, err := s.db.QueryContext(ctx, memorySelect+` WHERE user_id=$1 AND status='active' AND id>COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $3`, user, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []memory.Memory{}
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
