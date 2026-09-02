package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/conversation"
)

type serviceStore struct {
	run Run
}

func (s *serviceStore) CreateAgentRun(_ context.Context, item Run) (Run, bool, error) {
	s.run = item
	return item, true, nil
}
func (s *serviceStore) GetAgentRun(context.Context, string) (Run, error) {
	return s.run, nil
}
func (s *serviceStore) GetActiveAgentRun(context.Context, string, string) (Run, error) {
	return s.run, nil
}
func (s *serviceStore) ClaimAgentRun(context.Context, string, string, time.Time, time.Duration) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) ClaimNextAgentRun(context.Context, string, time.Time, time.Duration) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) DeferAgentRun(context.Context, string, string, int, time.Time, string, time.Time) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) PauseAgentRun(context.Context, string, string, int, json.RawMessage, time.Time) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) SuspendAgentRunForTool(context.Context, string, string, int, json.RawMessage, time.Time, time.Time) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) WakeAgentRunsForTool(context.Context, string, string, time.Time, int) ([]Run, error) {
	return []Run{s.run}, nil
}
func (s *serviceStore) ResolveAgentRun(context.Context, string, string, bool, string, time.Time) (Run, bool, error) {
	return s.run, true, nil
}
func (s *serviceStore) CancelAgentRun(context.Context, string, string, time.Time) (Run, bool, error) {
	s.run.Status = "cancelled"
	s.run.Revision++
	return s.run, true, nil
}
func (s *serviceStore) FinalizeAgentRunCancellation(context.Context, string, string, int, time.Time) (Run, error) {
	s.run.Status = "cancelled"
	return s.run, nil
}
func (s *serviceStore) TimeoutAgentRun(context.Context, string, string, int, string, string, time.Time) (Run, error) {
	s.run.Status = "timed_out"
	return s.run, nil
}
func (s *serviceStore) ExpireAgentRuns(context.Context, time.Time, int) (int, error) {
	return 0, nil
}
func (s *serviceStore) CompleteAgentRun(context.Context, string, string, int, json.RawMessage, time.Time) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) FailAgentRun(context.Context, string, string, int, string, string, time.Time) (Run, error) {
	return Run{}, nil
}
func (s *serviceStore) ListAgentRunEvents(context.Context, string, int64, int) ([]Event, error) {
	return nil, nil
}
func (s *serviceStore) AcceptAgentMessage(_ context.Context, message conversation.Message, run Run) (conversation.Message, Run, error) {
	message.Sequence = 7
	s.run = run
	return message, run, nil
}

func TestCreateUsesRunIDAsThreadID(t *testing.T) {
	store := &serviceStore{}
	now := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	service := NewServiceWithClock(store, func() time.Time { return now })
	item, created, err := service.Create(context.Background(), CreateInput{
		UserID: "user-1", ConversationID: "conversation-1", CharacterID: "character-1",
		Module: "life", IdempotencyKey: "message-1",
		Payload: map[string]any{"message_id": "message-1"},
	})
	if err != nil || !created {
		t.Fatalf("Create() = %#v, %v, %v", item, created, err)
	}
	if item.ID == "" || item.ThreadID != item.ID {
		t.Fatalf("id/thread_id = %q/%q", item.ID, item.ThreadID)
	}
	if item.Status != "accepted" || item.GraphName != GraphName || item.GraphVersion != GraphVersion {
		t.Fatalf("run = %#v", item)
	}
	if !item.DeadlineAt.Equal(now.Add(DefaultRunTimeout)) {
		t.Fatalf("deadline_at = %s", item.DeadlineAt)
	}
}

func TestCreateRejectsUnsupportedModule(t *testing.T) {
	service := NewService(&serviceStore{})
	if _, _, err := service.Create(context.Background(), CreateInput{
		UserID: "user-1", ConversationID: "conversation-1", CharacterID: "character-1",
		Module: "finance",
	}); err == nil {
		t.Fatal("Create() expected validation error")
	}
}

func TestAcceptChatCreatesMessageAndRunAsOneStoreOperation(t *testing.T) {
	store := &serviceStore{}
	now := time.Date(2026, time.July, 24, 11, 0, 0, 0, time.UTC)
	service := NewServiceWithClock(store, func() time.Time { return now })
	message, run, err := service.AcceptChat(context.Background(), AcceptChatInput{
		UserID: "user-1", ConversationID: "conversation-1",
		CharacterID: "character-1", Module: "life",
		Content: "我今天的计划是什么",
		Context: map[string]any{"timezone": "Asia/Shanghai"},
	})
	if err != nil {
		t.Fatalf("AcceptChat() error = %v", err)
	}
	if message.ID == "" || message.Sequence != 7 || run.ID == "" ||
		run.ThreadID != run.ID || run.IdempotencyKey != message.ID {
		t.Fatalf("message/run = %#v / %#v", message, run)
	}
	if !run.DeadlineAt.Equal(now.Add(DefaultRunTimeout)) {
		t.Fatalf("deadline_at = %s", run.DeadlineAt)
	}
	var payload map[string]any
	if err = json.Unmarshal(run.Input, &payload); err != nil ||
		payload["message_id"] != message.ID ||
		payload["text"] != "我今天的计划是什么" {
		t.Fatalf("run input = %#v, %v", payload, err)
	}
}

func TestCancelRequiresRunAndUser(t *testing.T) {
	service := NewService(&serviceStore{})
	if _, _, err := service.Cancel(context.Background(), "", "run-1"); err == nil {
		t.Fatal("Cancel() expected validation error")
	}
}

func TestRetryChatKeepsOriginalMessageAndCreatesIdempotentRunInput(t *testing.T) {
	priorInput, _ := json.Marshal(map[string]any{
		"message_id": "message-1", "text": "查看今日计划",
		"context": map[string]any{"timezone": "Asia/Shanghai"},
	})
	store := &serviceStore{run: Run{
		ID: "failed-run", UserID: "user-1", ConversationID: "conversation-1",
		CharacterID: "character-1", Module: "life", Status: "failed", Input: priorInput,
	}}
	service := NewService(store)
	retried, created, err := service.RetryChat(context.Background(), "user-1", "failed-run", "failed-run:retry")
	if err != nil || !created || retried.ID == "" || retried.ID == "failed-run" ||
		retried.IdempotencyKey != "chat-retry:failed-run:retry" {
		t.Fatalf("RetryChat() = %#v, %v, %v", retried, created, err)
	}
	var payload map[string]any
	if err = json.Unmarshal(retried.Input, &payload); err != nil ||
		payload["message_id"] != "message-1" || payload["text"] != "查看今日计划" {
		t.Fatalf("retry payload = %#v, %v", payload, err)
	}
}

func TestRetryChatWithPayloadRepairsLegacyAttachmentMetadata(t *testing.T) {
	priorInput, _ := json.Marshal(map[string]any{
		"message_id": "message-1", "text": "生成 PPT\n📎 courses.pdf",
	})
	store := &serviceStore{run: Run{
		ID: "failed-run", UserID: "user-1", ConversationID: "conversation-1",
		CharacterID: "character-1", Module: "work", Status: "failed", Input: priorInput,
	}}
	service := NewService(store)
	repaired := map[string]any{
		"message_id": "message-1",
		"text":       "生成 PPT\n<!--ai-document:11111111-1111-1111-1111-111111111111|courses.pdf-->",
	}
	retried, created, err := service.RetryChatWithPayload(
		context.Background(), "user-1", "failed-run", "attachment-repair-v1", repaired,
	)
	if err != nil || !created {
		t.Fatalf("RetryChatWithPayload() = %#v, %v, %v", retried, created, err)
	}
	var payload map[string]any
	if err = json.Unmarshal(retried.Input, &payload); err != nil ||
		!strings.Contains(payload["text"].(string), "<!--ai-document:") {
		t.Fatalf("retry payload = %#v, %v", payload, err)
	}
}

func TestResolveApprovalRequiresIdempotencyKey(t *testing.T) {
	service := NewService(&serviceStore{})
	if _, _, err := service.ResolveApproval(
		context.Background(), "user-1", "run-1", true, "",
	); err == nil {
		t.Fatal("ResolveApproval() expected validation error")
	}
}

func TestWakeForToolValidatesAndUsesServiceClock(t *testing.T) {
	store := &serviceStore{run: Run{ID: "run-1"}}
	now := time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)
	service := NewServiceWithClock(store, func() time.Time { return now })
	items, err := service.WakeForTool(context.Background(), "user-1", "task-1", 1)
	if err != nil || len(items) != 1 || items[0].ID != "run-1" {
		t.Fatalf("WakeForTool() = %#v, %v", items, err)
	}
	if _, err = service.WakeForTool(context.Background(), "", "task-1", 1); err == nil {
		t.Fatal("WakeForTool() expected validation error")
	}
}
