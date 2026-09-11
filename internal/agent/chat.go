package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

type ChatStore interface {
	AcceptAgentMessage(context.Context, conversation.Message, Run) (conversation.Message, Run, error)
}

type AcceptChatInput struct {
	UserID         string
	ConversationID string
	CharacterID    string
	Module         string
	Content        string
	Context        map[string]any
}

func (s *Service) AcceptChat(ctx context.Context, input AcceptChatInput) (conversation.Message, Run, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.ConversationID = strings.TrimSpace(input.ConversationID)
	input.CharacterID = strings.TrimSpace(input.CharacterID)
	input.Module = strings.TrimSpace(input.Module)
	input.Content = strings.TrimSpace(input.Content)
	if input.UserID == "" || input.ConversationID == "" || input.CharacterID == "" ||
		input.Content == "" || len([]rune(input.Content)) > 8000 {
		return conversation.Message{}, Run{}, fmt.Errorf("%w: invalid Agent chat input", ErrValidation)
	}
	if input.Module != "companion" && input.Module != "life" && input.Module != "work" {
		return conversation.Message{}, Run{}, fmt.Errorf("%w: unsupported module", ErrValidation)
	}
	store, ok := s.store.(ChatStore)
	if !ok {
		return conversation.Message{}, Run{}, ErrConflict
	}
	messageID, err := id.New()
	if err != nil {
		return conversation.Message{}, Run{}, err
	}
	runID, err := id.New()
	if err != nil {
		return conversation.Message{}, Run{}, err
	}
	now := s.now().UTC()
	message := conversation.Message{
		ID: messageID, ConversationID: input.ConversationID, UserID: input.UserID,
		Role: "user", Bubble: 1, Content: input.Content, Status: "completed",
		CreatedAt: now, CompletedAt: &now,
	}
	payload := map[string]any{
		"message_id": messageID,
		"text":       input.Content,
		"context":    input.Context,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return conversation.Message{}, Run{}, ErrValidation
	}
	run := Run{
		ID: runID, ThreadID: runID,
		UserID: input.UserID, ConversationID: input.ConversationID,
		CharacterID: input.CharacterID, Module: input.Module,
		GraphName: GraphName, GraphVersion: GraphVersion,
		Status: "accepted", IdempotencyKey: messageID, Input: encoded,
		AvailableAt: now, DeadlineAt: now.Add(s.runTimeout),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err = s.captureAgentDefinition(ctx, &run); err != nil {
		return conversation.Message{}, Run{}, err
	}
	if err = s.captureModelProfile(ctx, &run); err != nil {
		return conversation.Message{}, Run{}, err
	}
	return store.AcceptAgentMessage(ctx, message, run)
}
