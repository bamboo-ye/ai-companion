package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNoEvents = errors.New("no outbox events available")
	ErrConflict = errors.New("event state conflict")
)

type Event struct {
	ID            string
	TraceID       string
	TraceParent   string
	TraceState    string
	AggregateType string
	AggregateID   string
	Type          string
	Version       int
	Payload       json.RawMessage
	OccurredAt    time.Time
	Attempts      int
}

type PublishAck struct {
	Topic     string
	Partition int32
	Offset    int64
}

type Publisher interface {
	Publish(context.Context, Event) (PublishAck, error)
}

type Store interface {
	ClaimOutboxEvents(context.Context, string, time.Time, time.Duration, int) ([]Event, error)
	MarkOutboxPublished(context.Context, string, string, PublishAck, time.Time) error
	FailOutboxEvent(context.Context, string, string, string, time.Time, bool) error
	RecordInboxEvent(context.Context, string, string, time.Time) (bool, error)
	InboxEventExists(context.Context, string, string) (bool, error)
	RecordPoisonMessage(context.Context, PoisonMessageInput) error
	ReplayOutboxEvent(context.Context, string, time.Time) error
}

func TopicFor(eventType string) (string, error) {
	if _, allowed := knownTopics[eventType]; !allowed {
		return "", fmt.Errorf("event topic is not allowlisted")
	}
	return eventType, nil
}

var knownTopics = map[string]struct{}{
	"chat.command.v1":               {},
	"agent.run.requested.v1":        {},
	"agent.run.resume.requested.v1": {},
	"memory.extract.v1":             {},
	"document.ingest.v1":            {},
	"document.cleanup.v1":           {},
	"skill.execute.v1":              {},
	"skill.run.succeeded.v1":        {},
	"skill.run.failed.v1":           {},
	"skill.run.cancelled.v1":        {},
	"ledger.entry.created.v1":       {},
	"ledger.export.v1":              {},
	"reminder.confirmed.v1":         {},
	"reminder.rescheduled.v1":       {},
	"notification.deliver.v1":       {},
	"email.deliver.v1":              {},
}
