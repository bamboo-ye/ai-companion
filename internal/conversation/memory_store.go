package conversation

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

type MemoryStore struct {
	mu            sync.RWMutex
	conversations map[string]Conversation
	messages      map[string][]Message
	jobs          map[string]Job
	events        map[string][]Event
	summaries     map[string][]ConversationSummary
	nextEvent     uint64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{conversations: map[string]Conversation{}, messages: map[string][]Message{}, jobs: map[string]Job{}, events: map[string][]Event{}, summaries: map[string][]ConversationSummary{}}
}
func (s *MemoryStore) CreateConversation(_ context.Context, item Conversation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conversations[item.ID] = item
	return nil
}
func (s *MemoryStore) ListConversations(_ context.Context, userID string) ([]Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := []Conversation{}
	for _, item := range s.conversations {
		if item.UserID == userID {
			result = append(result, item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}
func (s *MemoryStore) GetConversation(_ context.Context, userID, id string) (Conversation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.conversations[id]
	if !ok || item.UserID != userID {
		return Conversation{}, ErrNotFound
	}
	return item, nil
}
func (s *MemoryStore) AcceptMessage(_ context.Context, userID, conversationID string, message Message, job Job) (Message, Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.conversations[conversationID]
	if !ok || conv.UserID != userID {
		return Message{}, Job{}, ErrNotFound
	}
	message.Sequence = conv.NextSequence
	conv.NextSequence += 2
	conv.LastMessageAt = &message.CreatedAt
	conv.UpdatedAt = message.CreatedAt
	s.conversations[conv.ID] = conv
	s.messages[conv.ID] = append(s.messages[conv.ID], message)
	s.jobs[job.ID] = job
	return message, job, nil
}
func (s *MemoryStore) ListMessages(_ context.Context, userID, conversationID string, after uint64, afterBubble, limit int) ([]Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conv, ok := s.conversations[conversationID]
	if !ok || conv.UserID != userID {
		return nil, ErrNotFound
	}
	result := []Message{}
	for _, item := range s.messages[conversationID] {
		if item.Sequence > after || (item.Sequence == after && item.Bubble > afterBubble) {
			result = append(result, item)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *MemoryStore) GetLatestSummary(_ context.Context, userID, conversationID string) (ConversationSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	conv, ok := s.conversations[conversationID]
	if !ok || conv.UserID != userID {
		return ConversationSummary{}, ErrNotFound
	}
	items := s.summaries[conversationID]
	if len(items) == 0 {
		return ConversationSummary{}, ErrNotFound
	}
	return items[len(items)-1], nil
}

func (s *MemoryStore) SaveSummary(_ context.Context, item ConversationSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.conversations[item.ConversationID]
	if !ok || conv.UserID != item.UserID {
		return ErrNotFound
	}
	items := s.summaries[item.ConversationID]
	if len(items) > 0 {
		latest := items[len(items)-1]
		if latest.EndSequence >= item.EndSequence {
			return nil
		}
		item.Version = latest.Version + 1
	}
	s.summaries[item.ConversationID] = append(items, item)
	return nil
}
func (s *MemoryStore) GetJob(_ context.Context, userID, jobID string) (Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return Job{}, ErrNotFound
	}
	conv := s.conversations[job.ConversationID]
	if conv.UserID != userID {
		return Job{}, ErrNotFound
	}
	return job, nil
}
func (s *MemoryStore) MarkJobRunning(_ context.Context, userID, jobID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || s.conversations[job.ConversationID].UserID != userID {
		return ErrNotFound
	}
	if job.Status != "accepted" {
		return ErrConflict
	}
	job.Status = "running"
	job.StartedAt = &now
	job.Provider = "development"
	job.Model = "deterministic-persona-v1"
	s.jobs[jobID] = job
	return nil
}
func (s *MemoryStore) AppendAssistantBubble(_ context.Context, userID, jobID string, bubble int, content string, now time.Time) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || s.conversations[job.ConversationID].UserID != userID {
		return Message{}, ErrNotFound
	}
	if job.Status != "running" {
		return Message{}, ErrConflict
	}
	var source Message
	for _, item := range s.messages[job.ConversationID] {
		if item.ID == job.UserMessageID {
			source = item
			break
		}
	}
	messageID, err := id.New()
	if err != nil {
		return Message{}, err
	}
	message := Message{ID: messageID, ConversationID: job.ConversationID, UserID: userID, Role: "assistant", Sequence: source.Sequence + 1, Bubble: bubble, Content: content, Status: "completed", ReplyToID: source.ID, CreatedAt: now, CompletedAt: &now}
	s.messages[job.ConversationID] = append(s.messages[job.ConversationID], message)
	return message, nil
}
func (s *MemoryStore) FinishJob(_ context.Context, userID, jobID, status, code, message string, usage Usage, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || s.conversations[job.ConversationID].UserID != userID {
		return ErrNotFound
	}
	if job.Status == "cancel_requested" {
		status, code, message = "cancelled", "cancelled", "generation was cancelled"
	}
	job.Status = status
	job.ErrorCode = code
	job.ErrorMessage = message
	job.CompletedAt = &now
	s.jobs[jobID] = job
	return nil
}

func (s *MemoryStore) DeferGenerationJob(_ context.Context, userID, jobID, code, message string, _ time.Time, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || s.conversations[job.ConversationID].UserID != userID {
		return ErrNotFound
	}
	if job.Status != "accepted" && job.Status != "running" {
		return ErrConflict
	}
	job.Status = "accepted"
	job.ErrorCode = code
	job.ErrorMessage = message
	job.CompletedAt = nil
	s.jobs[jobID] = job
	return nil
}

func (s *MemoryStore) RequestCancel(_ context.Context, userID, jobID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || s.conversations[job.ConversationID].UserID != userID {
		return ErrNotFound
	}
	if job.Status != "accepted" && job.Status != "running" {
		return ErrConflict
	}
	job.Status = "cancelled"
	job.ErrorCode = "cancelled"
	job.ErrorMessage = "generation was cancelled"
	job.CompletedAt = &now
	s.jobs[jobID] = job
	return nil
}
func (s *MemoryStore) CreateRetry(_ context.Context, userID, oldID string, next Job) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.jobs[oldID]
	if !ok || s.conversations[old.ConversationID].UserID != userID {
		return Job{}, ErrNotFound
	}
	if !IsTerminal(old.Status) || old.Status == "completed" {
		return Job{}, ErrConflict
	}
	next.ConversationID = old.ConversationID
	next.UserMessageID = old.UserMessageID
	next.Attempt = old.Attempt + 1
	s.jobs[next.ID] = next
	return next, nil
}
func (s *MemoryStore) AppendEvent(_ context.Context, jobID, eventType string, data any, now time.Time) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[jobID]; !ok {
		return Event{}, ErrNotFound
	}
	s.nextEvent++
	event := Event{ID: s.nextEvent, JobID: jobID, Type: eventType, Data: data, CreatedAt: now}
	s.events[jobID] = append(s.events[jobID], event)
	return event, nil
}
func (s *MemoryStore) ListEvents(_ context.Context, userID, jobID string, after uint64, limit int) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[jobID]
	if !ok || s.conversations[job.ConversationID].UserID != userID {
		return nil, ErrNotFound
	}
	result := []Event{}
	for _, event := range s.events[jobID] {
		if event.ID > after {
			result = append(result, event)
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}
func (s *MemoryStore) RecoverInterrupted(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if job.Status != "accepted" && job.Status != "running" && job.Status != "cancel_requested" {
			continue
		}
		eventType := "failed"
		job.ErrorCode = "process_interrupted"
		job.ErrorMessage = "generation was interrupted and can be retried"
		if job.Status == "cancel_requested" {
			eventType = "cancelled"
			job.ErrorCode = "cancelled"
		}
		job.Status = eventType
		job.CompletedAt = &now
		s.jobs[id] = job
		s.nextEvent++
		s.events[id] = append(s.events[id], Event{ID: s.nextEvent, JobID: id, Type: eventType, Data: map[string]string{"status": eventType, "error_code": job.ErrorCode}, CreatedAt: now})
	}
	return nil
}
