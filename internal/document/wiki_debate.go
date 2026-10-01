package document

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/windcry1/ai-companion/internal/arbitration"
	"github.com/windcry1/ai-companion/internal/semantic"
)

// Runs in the already-leased Wiki compile job. Supplemental reads deliberately
// stay inside the page's complete source intersection, including source versions.
// No external source can silently widen visibility of the published page.
func (s *Service) debateWiki(ctx context.Context, pages []WikiPage, input arbitration.Input, client *semantic.Client) arbitration.Record {
	sources := map[string]string{}
	user := pages[0].OwnerID
	for _, p := range pages {
		for _, ref := range p.Sources {
			sources[ref.DocumentID] = ref.Version
		}
	}
	verify := func(ctx context.Context) error {
		for id, version := range sources {
			d, err := s.store.GetDocument(ctx, user, id)
			if err != nil || d.Status != "ready" || SourceVersion(d) != version {
				return ErrNotFound
			}
		}
		return nil
	}
	read := func(ctx context.Context, request arbitration.EvidenceRequest) ([]arbitration.Supplement, error) {
		if err := verify(ctx); err != nil {
			return nil, err
		}
		store, ok := s.store.(ParsedStore)
		if !ok {
			return nil, ErrParsedContentUnavailable
		}
		query, _ := request.Arguments["query"].(string)
		issueIDs := []string{}
		for _, i := range input.Issues {
			if strings.Contains(query, i.Label) || strings.Contains(i.Label, query) {
				issueIDs = append(issueIDs, i.ID)
			}
		}
		if len(issueIDs) == 0 {
			return nil, nil
		}
		known := map[string]bool{}
		for _, e := range input.Evidence {
			known[e.ID] = true
		}
		out := []arbitration.Supplement{}
		// Deterministic page source order, not map iteration order.
		for _, source := range pages[0].Sources {
			chunks, err := store.ListDocumentChunks(ctx, user, source.DocumentID, 10000)
			if err != nil {
				return nil, err
			}
			for _, chunk := range chunks {
				if known[chunk.ID] {
					continue
				}
				relevant := false
				for _, i := range input.Issues {
					relevant = relevant || strings.Contains(chunk.Content, i.Label)
				}
				if !relevant {
					continue
				}
				// Never truncate a source into an apparent complete excerpt.
				if len(chunk.Content) > 12000 {
					continue
				}
				out = append(out, arbitration.Supplement{Evidence: arbitration.Evidence{ID: chunk.ID, Text: chunk.Content, Version: sources[source.DocumentID]}, IssueIDs: issueIDs})
				if len(out) == 4 {
					break
				}
			}
			if len(out) == 4 {
				break
			}
		}
		if err := verify(ctx); err != nil {
			return nil, fmt.Errorf("source changed: %w", err)
		}
		return out, nil
	}
	if cache, ok := s.store.(WikiShardStore); ok {
		store := arbitration.DebatePersistence{
			Load: func(ctx context.Context, key string) ([]byte, error) {
				data, err := cache.LoadWikiShard(ctx, user, pages[0].Sources[0].DocumentID, key)
				if errors.Is(err, ErrNotFound) {
					return nil, nil
				}
				return data, err
			},
			Save: func(ctx context.Context, key string, data []byte) error {
				return cache.SaveWikiShard(ctx, user, pages[0].Sources[0].DocumentID, key, data)
			},
		}
		return arbitration.Debate(ctx, client, input, read, verify, store)
	}
	return arbitration.Debate(ctx, client, input, read, verify)
}
