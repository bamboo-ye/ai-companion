package mysqlstore

import (
	"context"
	"github.com/windcry1/ai-companion/internal/document"
	"github.com/windcry1/ai-companion/internal/memory"
)

func (s *Store) ListKnowledgeDocuments(ctx context.Context, user, workspace, cursor string, limit int) ([]document.Document, error) {
	rows, err := s.db.QueryContext(ctx, documentSelect+` WHERE d.ingest_status<>'deleted' AND f.status='active' AND d.id>UUID_TO_BIN(COALESCE(NULLIF(?,''),'00000000-0000-0000-0000-000000000000')) AND ((?='' AND d.user_id=UUID_TO_BIN(?)) OR (?<>'' AND EXISTS (SELECT 1 FROM workspace_document_shares ks WHERE ks.document_id=d.id AND ks.workspace_id=UUID_TO_BIN(NULLIF(?,''))))) ORDER BY d.id LIMIT ?`, cursor, workspace, user, workspace, workspace, limit)
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
	rows, err := s.db.QueryContext(ctx, memorySelect+` WHERE user_id=UUID_TO_BIN(?) AND status='active' AND id>UUID_TO_BIN(COALESCE(NULLIF(?,''),'00000000-0000-0000-0000-000000000000')) ORDER BY id LIMIT ?`, user, cursor, limit)
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
