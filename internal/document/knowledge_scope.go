package document

import (
	"context"
	"fmt"
)

// DocumentPager enumerates the authoritative scope using a stable ID cursor.
// UI list limits must never define the searchable corpus.
type DocumentPager interface {
	ListKnowledgeDocuments(context.Context, string, string, string, int) ([]Document, error)
}

func (s *Service) KnowledgeDocuments(ctx context.Context, user, workspace string) ([]Document, error) {
	if pager, ok := s.store.(DocumentPager); ok {
		result := []Document{}
		cursor := ""
		for {
			page, err := pager.ListKnowledgeDocuments(ctx, user, workspace, cursor, 256)
			if err != nil {
				return nil, err
			}
			result = append(result, page...)
			if len(page) < 256 {
				return result, nil
			}
			next := page[len(page)-1].ID
			if next <= cursor {
				return nil, fmt.Errorf("invalid document pagination cursor")
			}
			cursor = next
		}
	}
	// Compatibility with third-party stores: grow the requested window until
	// exhaustion instead of silently treating one UI page as the whole corpus.
	for limit := 256; ; limit *= 2 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var items []Document
		var err error
		if workspace == "" {
			items, err = s.store.ListDocuments(ctx, user, limit)
		} else {
			items, err = s.store.ListWorkspaceDocuments(ctx, workspace, limit)
		}
		if err != nil || len(items) < limit {
			return items, err
		}
		if limit > 1<<24 {
			return nil, fmt.Errorf("document store requires cursor pagination")
		}
	}
}

// Workspace membership is checked by the authenticated caller, as for QueryWorkspace.
func (s *Service) KnowledgeDocument(ctx context.Context, user, workspace, id string) (Document, error) {
	if workspace == "" {
		return s.Get(ctx, user, id)
	}
	items, err := s.KnowledgeDocuments(ctx, user, workspace)
	if err != nil {
		return Document{}, err
	}
	for _, d := range items {
		if d.ID == id {
			return d, nil
		}
	}
	return Document{}, ErrNotFound
}

func (s *Service) KnowledgeChunks(ctx context.Context, user, workspace, id string) (Document, []Chunk, error) {
	d, err := s.KnowledgeDocument(ctx, user, workspace, id)
	if err != nil {
		return d, nil, err
	}
	if d.Status != "ready" {
		return d, nil, ErrParsedContentUnavailable
	}
	store, ok := s.store.(ParsedStore)
	if !ok {
		return d, nil, ErrParsedContentUnavailable
	}
	chunks, err := store.ListDocumentChunks(ctx, d.UserID, id, max(d.ChunkCount, 1))
	if err == nil && len(chunks) != d.ChunkCount {
		err = ErrParsedContentUnavailable
	}
	return d, chunks, err
}
