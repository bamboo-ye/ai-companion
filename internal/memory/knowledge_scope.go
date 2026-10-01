package memory

import (
	"context"
	"fmt"
)

type MemoryPager interface {
	ListKnowledgeMemories(context.Context, string, string, int) ([]Memory, error)
}

func (s *Service) KnowledgeMemories(ctx context.Context, user string) ([]Memory, error) {
	if pager, ok := s.store.(MemoryPager); ok {
		items := []Memory{}
		cursor := ""
		for {
			page, err := pager.ListKnowledgeMemories(ctx, user, cursor, 256)
			if err != nil {
				return nil, err
			}
			items = append(items, page...)
			if len(page) < 256 {
				return items, nil
			}
			next := page[len(page)-1].ID
			if next <= cursor {
				return nil, fmt.Errorf("invalid memory pagination cursor")
			}
			cursor = next
		}
	}
	for limit := 512; ; limit *= 2 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := s.store.ListMemories(ctx, user, limit)
		if err != nil || len(items) < limit {
			return items, err
		}
		if limit > 1<<24 {
			return nil, fmt.Errorf("memory store requires cursor pagination")
		}
	}
}

func (s *Service) KnowledgeMemory(ctx context.Context, user, id string) (Memory, error) {
	item, err := s.store.GetMemory(ctx, user, id)
	if err != nil {
		return Memory{}, err
	}
	now := s.now()
	if item.Status != "active" || item.ValidFrom.After(now) || (item.ValidTo != nil && !item.ValidTo.After(now)) {
		return Memory{}, ErrNotFound
	}
	item.Arbitration = s.visibleArbitration(ctx, item)
	return item, nil
}
