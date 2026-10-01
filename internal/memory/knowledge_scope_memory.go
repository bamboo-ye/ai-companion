package memory

import (
	"context"
	"sort"
)

func (s *MemoryStore) ListKnowledgeMemories(_ context.Context, user, cursor string, limit int) ([]Memory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := []Memory{}
	for _, item := range s.items {
		if item.ID > cursor && item.Status == "active" && item.UserID == user {
			items = append(items, cloneMemory(item))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
