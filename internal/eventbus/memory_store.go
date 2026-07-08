package eventbus

import (
	"context"
	"sort"
	"sync"
	"time"
)

type memoryEvent struct {
	event        Event
	status       string
	availableAt  time.Time
	workerID     string
	leaseExpires time.Time
	ack          PublishAck
	lastError    string
}

type MemoryStore struct {
	mu            sync.Mutex
	events        map[string]memoryEvent
	inbox         map[string]time.Time
	poison        []PoisonMessageRecord
	compensations []CompensationRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{events: make(map[string]memoryEvent), inbox: make(map[string]time.Time)}
}

func (s *MemoryStore) Add(event Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[event.ID] = memoryEvent{event: event, status: "pending", availableAt: event.OccurredAt}
}

func (s *MemoryStore) ClaimOutboxEvents(_ context.Context, workerID string, now time.Time, lease time.Duration, limit int) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0)
	for id, item := range s.events {
		if !item.availableAt.After(now) && (item.status == "pending" || (item.status == "publishing" && !item.leaseExpires.After(now))) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.events[ids[i]].event.OccurredAt.Before(s.events[ids[j]].event.OccurredAt)
	})
	if len(ids) == 0 {
		return nil, ErrNoEvents
	}
	if len(ids) > limit {
		ids = ids[:limit]
	}
	result := make([]Event, 0, len(ids))
	for _, id := range ids {
		item := s.events[id]
		item.status, item.workerID, item.leaseExpires = "publishing", workerID, now.Add(lease)
		item.event.Attempts++
		s.events[id] = item
		result = append(result, item.event)
	}
	return result, nil
}

func (s *MemoryStore) MarkOutboxPublished(_ context.Context, eventID, workerID string, ack PublishAck, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.events[eventID]
	if !ok || item.status != "publishing" || item.workerID != workerID {
		return ErrConflict
	}
	item.status, item.workerID, item.ack = "published", "", ack
	s.events[eventID] = item
	return nil
}

func (s *MemoryStore) FailOutboxEvent(_ context.Context, eventID, workerID, message string, availableAt time.Time, dead bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.events[eventID]
	if !ok || item.status != "publishing" || item.workerID != workerID {
		return ErrConflict
	}
	item.status, item.workerID, item.lastError, item.availableAt = "pending", "", message, availableAt
	if dead {
		item.status = "dead_letter"
	}
	s.events[eventID] = item
	return nil
}

func (s *MemoryStore) RecordInboxEvent(_ context.Context, consumer, eventID string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := consumer + ":" + eventID
	if _, exists := s.inbox[key]; exists {
		return false, nil
	}
	s.inbox[key] = now
	return true, nil
}

func (s *MemoryStore) InboxEventExists(_ context.Context, consumer, eventID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.inbox[consumer+":"+eventID]
	return exists, nil
}

func (s *MemoryStore) RecordPoisonMessage(_ context.Context, input PoisonMessageInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := PoisonMessageRecord{
		ID: uint64(len(s.poison) + 1), ConsumerName: input.ConsumerName, Topic: input.Topic, Partition: input.Partition,
		Offset: input.Offset, EventID: input.EventID, EventType: input.EventType, AggregateID: input.AggregateID,
		Reason: input.Reason, Envelope: append([]byte(nil), input.Envelope...), ObservedAt: input.ObservedAt,
	}
	s.poison = append(s.poison, record)
	return nil
}

func (s *MemoryStore) ReplayOutboxEvent(_ context.Context, eventID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.events[eventID]
	if !ok || item.status != "dead_letter" {
		return ErrConflict
	}
	item.status, item.availableAt, item.lastError, item.workerID = "pending", now, "", ""
	item.event.Attempts = 0
	s.events[eventID] = item
	return nil
}

func (s *MemoryStore) ListDeadLetterOutboxEvents(_ context.Context, limit int) ([]OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	records := make([]OutboxRecord, 0)
	for _, item := range s.events {
		if item.status != "dead_letter" {
			continue
		}
		record := memoryOutboxRecord(item)
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].AvailableAt.Before(records[j].AvailableAt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *MemoryStore) GetOutboxEvent(_ context.Context, eventID string) (OutboxRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.events[eventID]
	if !ok {
		return OutboxRecord{}, ErrConflict
	}
	return memoryOutboxRecord(item), nil
}

func (s *MemoryStore) ListPoisonMessages(_ context.Context, limit int) ([]PoisonMessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	records := append([]PoisonMessageRecord(nil), s.poison...)
	sort.Slice(records, func(i, j int) bool { return records[i].ObservedAt.After(records[j].ObservedAt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *MemoryStore) CreateCompensationRecord(_ context.Context, input CompensationInput) (CompensationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := CompensationRecord{
		ID: input.ID, SourceType: input.SourceType, SourceID: input.SourceID, Action: input.Action, Reason: input.Reason,
		Actor: input.Actor, Status: input.Status, Metadata: append([]byte(nil), input.Metadata...), CreatedAt: input.CreatedAt,
	}
	s.compensations = append(s.compensations, record)
	return record, nil
}

func (s *MemoryStore) ListCompensationRecords(_ context.Context, limit int) ([]CompensationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	records := append([]CompensationRecord(nil), s.compensations...)
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func memoryOutboxRecord(item memoryEvent) OutboxRecord {
	event := item.event
	record := OutboxRecord{
		ID: event.ID, AggregateType: event.AggregateType, AggregateID: event.AggregateID, Type: event.Type, Version: event.Version,
		Payload: append([]byte(nil), event.Payload...), OccurredAt: event.OccurredAt, Status: item.status, AvailableAt: item.availableAt,
		Attempts: event.Attempts, LastError: item.lastError, WorkerID: item.workerID, PublishedTopic: item.ack.Topic,
	}
	if !item.leaseExpires.IsZero() {
		value := item.leaseExpires
		record.LeaseExpiresAt = &value
	}
	if item.ack.Topic != "" {
		partition, offset := int(item.ack.Partition), item.ack.Offset
		record.PublishedPartition, record.PublishedOffset = &partition, &offset
	}
	return record
}
