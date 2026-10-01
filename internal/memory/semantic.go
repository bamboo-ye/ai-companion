package memory

import (
	"context"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/semantic"
	"strings"
	"time"
)

func (s *Service) SetSemanticClient(client *semantic.Client) { s.semantic = client }
func (s *Service) semanticScores(ctx context.Context, user, query string, items []Memory) (map[string]float64, bool) {
	projections := []semantic.Projection{}
	for _, item := range items {
		if item.Status == "active" && !item.ValidFrom.After(s.now()) && (item.ValidTo == nil || item.ValidTo.After(s.now())) {
			projections = append(projections, s.semantic.SourceProjections(user, "memory", item.ID, KnowledgeRevision(item), item.Content)...)
		}
	}
	scores, pending, err := s.semantic.StoredScores(ctx, query, projections)
	return scores, pending || err != nil
}
func KnowledgeRevision(item Memory) string {
	return hash(item.NormalizedHash + ":" + item.UpdatedAt.UTC().Format(time.RFC3339Nano))
}
func (s *Service) IndexKnowledge(ctx context.Context, user string) error {
	if !s.semantic.EmbeddingsEnabled() {
		return nil
	}
	items, err := s.KnowledgeMemories(ctx, user)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ValidFrom.After(s.now()) || (item.ValidTo != nil && !item.ValidTo.After(s.now())) {
			continue
		}
		if err = s.semantic.QueueSource(ctx, user, "memory", item.ID, KnowledgeRevision(item), item.Content); err != nil {
			return err
		}
	}
	return nil
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
	old.Arbitration = nil
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
