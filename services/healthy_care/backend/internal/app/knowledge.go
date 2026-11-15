package app

import (
	"context"
	"encoding/json"
	"time"
)

func (a *App) referenceKnowledge(ctx context.Context) ([]map[string]any, error) {
	const key = "knowledge:reference-2026-10-19:v1"
	var items []map[string]any
	if cached, err := a.Cache.Get(ctx, key).Bytes(); err == nil && json.Unmarshal(cached, &items) == nil {
		return items, nil
	}
	items, err := rows(ctx, a.DB, "SELECT id,topic,title,content,source_url,version,review_status FROM knowledge_items WHERE review_status<>'retired' ORDER BY id")
	if err != nil {
		return nil, err
	}
	_ = a.Cache.Set(ctx, key, encode(items), 24*time.Hour).Err()
	return items, nil
}
