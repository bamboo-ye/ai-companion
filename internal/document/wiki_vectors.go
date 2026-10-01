package document

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
)

func WikiRevision(p WikiPage) string {
	data, _ := json.Marshal(struct {
		Version     int
		Title, Body string
		Sources     []WikiSource
	}{p.Version, p.Title, p.Body, p.Sources})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func (s *Service) wikiDenseScores(ctx context.Context, query string, pages []WikiPage) (map[string]float64, error) {
	items := []semantic.Projection{}
	for _, p := range pages {
		items = append(items, s.semantic.SourceProjections(p.OwnerID, "wiki", p.ID, WikiRevision(p), p.Title+"\n"+p.Body)...)
	}
	scores, missing, err := s.semantic.StoredScores(ctx, query, items)
	if err == nil && missing {
		err = fmt.Errorf("wiki semantic index pending")
	}
	// Partial ready projections remain usable, with the degraded flag reported.
	return scores, err
}
func (s *Service) IndexKnowledge(ctx context.Context, user string) error {
	if !s.semantic.EmbeddingsEnabled() || !s.wikiEnabled {
		return nil
	}
	pages, err := s.ListWiki(ctx, user, "")
	if err != nil {
		return err
	}
	for _, p := range pages {
		if p.Stale {
			continue
		}
		if err = s.semantic.QueueSource(ctx, p.OwnerID, "wiki", p.ID, WikiRevision(p), p.Title+"\n"+p.Body); err != nil {
			return err
		}
	}
	return nil
}
