package memory

import (
	"context"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/semantic"
	"strings"
	"time"
)

type embeddingEntry struct {
	vector  []float32
	expires time.Time
}

func (s *Service) SetSemanticClient(client *semantic.Client) { s.semantic = client }
func (s *Service) semanticScores(ctx context.Context, user, query string, items []Memory) map[string]float64 {
	scores := map[string]float64{}
	if !s.semantic.EmbeddingsEnabled() {
		return scores
	}
	vectors, err := s.semantic.Embed(ctx, []string{query})
	if err != nil {
		return scores
	}
	missing := []Memory{}
	texts := []string{}
	now := s.now()
	s.cacheMu.Lock()
	if s.embeddings == nil {
		s.embeddings = map[string]embeddingEntry{}
	}
	for _, item := range items {
		if item.Status != "active" || item.ValidFrom.After(now) || (item.ValidTo != nil && !item.ValidTo.After(now)) {
			continue
		}
		key := user + ":" + item.ID + ":" + item.NormalizedHash + ":" + s.semantic.Version()
		if entry, ok := s.embeddings[key]; ok && entry.expires.After(now) {
			scores[item.ID] = semantic.Cosine(vectors[0], entry.vector)
		} else {
			missing = append(missing, item)
			texts = append(texts, item.Content)
		}
	}
	s.cacheMu.Unlock()
	if len(texts) > 0 {
		embeddings, err := s.semantic.Embed(ctx, texts)
		if err != nil {
			return scores
		}
		s.cacheMu.Lock()
		defer s.cacheMu.Unlock()
		if len(s.embeddings)+len(texts) > 2048 {
			s.embeddings = map[string]embeddingEntry{}
		}
		for i, item := range missing {
			key := user + ":" + item.ID + ":" + item.NormalizedHash + ":" + s.semantic.Version()
			s.embeddings[key] = embeddingEntry{embeddings[i], now.Add(15 * time.Minute)}
			scores[item.ID] = semantic.Cosine(vectors[0], embeddings[i])
		}
	}
	return scores
}

// Correct keeps the old evidence and validity interval; replacement is atomic.
func (s *Service) Correct(ctx context.Context, user, memoryID, content string) (Memory, error) {
	return s.CorrectFromMessage(ctx, user, memoryID, content, "", "")
}
func (s *Service) CorrectFromMessage(ctx context.Context, user, memoryID, content, conversationID, messageID string) (Memory, error) {
	content = strings.TrimSpace(content)
	old, err := s.store.GetMemory(ctx, user, memoryID)
	if err != nil {
		return Memory{}, err
	}
	if len([]rune(content)) < 2 || len([]rune(content)) > 2000 {
		return Memory{}, ErrValidation
	}
	nextID, err := id.New()
	if err != nil {
		return Memory{}, err
	}
	now := s.now().UTC()
	old.SourceConversationID = conversationID
	old.SourceMessageID = messageID
	old.SupersedesID = old.ID
	old.ID = nextID
	old.Content = content
	old.NormalizedHash = hash(normalize(content))
	old.Type = classify(content)
	old.Sensitivity = sensitivity(content)
	old.ValidFrom = now
	old.ValidTo = nil
	old.CreatedAt = now
	old.UpdatedAt = now
	saved, _, err := s.store.UpsertMemory(ctx, old)
	return saved, err
}
