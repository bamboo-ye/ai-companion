package document

import (
	"context"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"time"
)

type wikiVector struct {
	value   []float32
	expires time.Time
}

func (s *Service) wikiDenseScores(ctx context.Context, query string, pages []WikiPage) (map[string]float64, error) {
	vectors, err := s.semantic.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	queryVector := vectors[0]
	result := map[string]float64{}
	missing := []WikiPage{}
	texts := []string{}
	w := s.wikiRuntime
	now := s.now()
	w.mu.Lock()
	if w.vectors == nil {
		w.vectors = map[string]wikiVector{}
	}
	for _, p := range pages {
		key := wikiVectorKey(p, s.semantic.Version())
		if v, ok := w.vectors[key]; ok && v.expires.After(now) {
			result[p.ID] = semantic.Cosine(queryVector, v.value)
		} else {
			missing = append(missing, p)
			texts = append(texts, p.Title+"\n"+truncateRunes(p.Body, 1600))
		}
	}
	w.mu.Unlock()
	if len(missing) == 0 {
		return result, nil
	}
	embedded, err := s.semantic.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.vectors)+len(missing) > 2048 {
		w.vectors = map[string]wikiVector{}
	}
	for i, p := range missing {
		w.vectors[wikiVectorKey(p, s.semantic.Version())] = wikiVector{embedded[i], now.Add(15 * time.Minute)}
		result[p.ID] = semantic.Cosine(queryVector, embedded[i])
	}
	return result, nil
}
func wikiVectorKey(p WikiPage, model string) string {
	return fmt.Sprintf("%s:%s:%d:%s:%s", p.OwnerID, p.ID, p.Version, p.UpdatedAt.UTC().Format(time.RFC3339Nano), model)
}
