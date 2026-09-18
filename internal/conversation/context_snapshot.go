package conversation

import (
	"context"
	"strconv"

	"github.com/windcry1/ai-companion/internal/contextengine"
	"github.com/windcry1/ai-companion/internal/productknowledge"
)

type sourcedMemoryContext interface {
	RecallContext(context.Context, string, string, int) ([]contextengine.Item, error)
}

// BuildContext is the common, user-scoped entry point for regular chat and
// Agent intake. The current Agent message is not yet persisted at intake and
// is supplied separately; it must never be duplicated in History.
func (s *Service) BuildContext(ctx context.Context, userID, conversationID, query string) (contextengine.Snapshot, error) {
	if _, err := s.store.GetConversation(ctx, userID, conversationID); err != nil {
		return contextengine.Snapshot{}, err
	}
	result, err := s.contextBuilder.Build(ctx, userID, conversationID)
	if err != nil {
		return contextengine.Snapshot{}, err
	}
	return s.contextSnapshot(ctx, userID, conversationID, query, result, s.policy().UseFullRAG), nil
}

func (s *Service) contextSnapshot(ctx context.Context, userID, conversationID, query string, result ContextResult, recall bool) contextengine.Snapshot {
	snapshot := contextengine.Snapshot{
		Version: contextengine.Version, History: []contextengine.Message{}, Memories: []contextengine.Item{},
		Manifest: contextengine.Manifest{Version: contextengine.Version, Sources: []contextengine.Source{}, RecentTokens: result.RecentTokens},
	}
	for _, item := range result.Messages {
		if item.Role != "user" && item.Role != "assistant" {
			continue
		}
		snapshot.History = append(snapshot.History, contextengine.Message{ID: item.ID, Role: item.Role, Content: item.Content, Sequence: item.Sequence, Bubble: item.Bubble})
		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, contextengine.Source{Kind: "message", ID: item.ID, ConversationID: conversationID, StartSequence: item.Sequence, EndSequence: item.Sequence})
	}
	if summary := result.Summary; summary != nil {
		snapshot.Summary = &contextengine.Item{Content: summary.Content, EstimatedTokens: summary.TokenCount, Source: contextengine.Source{
			Kind: "summary", ID: summary.ID, ConversationID: conversationID, Version: strconv.Itoa(summary.Version),
			StartSequence: summary.StartSequence, EndSequence: summary.EndSequence, UpdatedAt: &summary.CreatedAt,
		}}
		snapshot.Manifest.SummaryTokens = summary.TokenCount
		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, snapshot.Summary.Source)
	}
	// Local product help has no remote RAG dependency and remains available in
	// degraded mode. It never includes a user's private uploaded documents.
	snapshot.Knowledge, snapshot.Manifest.KnowledgeTokens, snapshot.Manifest.KnowledgeOmitted = productknowledge.Context(query, snapshot.History)
	for _, item := range snapshot.Knowledge {
		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, item.Source)
	}
	if !recall {
		snapshot.Manifest.Degraded = append(snapshot.Manifest.Degraded, "memory_disabled_by_policy")
		return snapshot
	}
	var candidates []contextengine.Item
	var err error
	if memories, ok := s.memories.(sourcedMemoryContext); ok {
		candidates, err = memories.RecallContext(ctx, userID, query, 8)
	} else {
		var texts []string
		texts, err = s.memories.Recall(ctx, userID, query, 8)
		for _, content := range texts {
			candidates = append(candidates, contextengine.Item{Content: content, Source: contextengine.Source{Kind: "memory"}})
		}
	}
	if err != nil {
		snapshot.Manifest.Degraded = append(snapshot.Manifest.Degraded, "memory_unavailable")
		return snapshot
	}
	snapshot.Memories, snapshot.Manifest.MemoryTokens, snapshot.Manifest.MemoryOmitted = contextengine.BoundMemories(candidates, contextengine.DefaultMemoryTokens)
	for _, item := range snapshot.Memories {
		snapshot.Manifest.Sources = append(snapshot.Manifest.Sources, item.Source)
	}
	return snapshot
}

func snapshotMessages(snapshot contextengine.Snapshot) []Message {
	messages := make([]Message, 0, len(snapshot.History)+2)
	if reference := snapshot.ReferenceText(); reference != "" {
		messages = append(messages, Message{Role: "system", Content: contextengine.ReferenceInstruction + "\n" + productknowledge.Instruction}, Message{Role: "user", Content: reference})
	}
	for _, item := range snapshot.History {
		messages = append(messages, Message{ID: item.ID, Role: item.Role, Content: item.Content, Sequence: item.Sequence, Bubble: item.Bubble})
	}
	return messages
}
