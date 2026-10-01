package document

import (
	"context"
	"sort"
)

func (s *MemoryStore) ListKnowledgeDocuments(_ context.Context, user, workspace, cursor string, limit int) ([]Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []Document{}
	for _, item := range s.items {
		if item.ID > cursor && item.Status != "deleted" && ((workspace == "" && item.UserID == user) || (workspace != "" && !s.workspaceShares[workspace][item.ID].IsZero())) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
