package eventbus

import (
	"context"
	"encoding/json"
	"time"
)

type OutboxRecord struct {
	ID                 string          `json:"id"`
	AggregateType      string          `json:"aggregate_type"`
	AggregateID        string          `json:"aggregate_id"`
	Type               string          `json:"event_type"`
	Version            int             `json:"event_version"`
	Payload            json.RawMessage `json:"payload,omitempty"`
	OccurredAt         time.Time       `json:"occurred_at"`
	Status             string          `json:"status"`
	AvailableAt        time.Time       `json:"available_at"`
	Attempts           int             `json:"attempts"`
	LastError          string          `json:"last_error,omitempty"`
	WorkerID           string          `json:"worker_id,omitempty"`
	LeaseExpiresAt     *time.Time      `json:"lease_expires_at,omitempty"`
	PublishedAt        *time.Time      `json:"published_at,omitempty"`
	PublishedTopic     string          `json:"published_topic,omitempty"`
	PublishedPartition *int            `json:"published_partition,omitempty"`
	PublishedOffset    *int64          `json:"published_offset,omitempty"`
}

type CompensationRecord struct {
	ID          string          `json:"id"`
	SourceType  string          `json:"source_type"`
	SourceID    string          `json:"source_id"`
	Action      string          `json:"action"`
	Reason      string          `json:"reason"`
	Actor       string          `json:"actor"`
	Status      string          `json:"status"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

type PoisonMessageInput struct {
	ConsumerName string
	Topic        string
	Partition    int
	Offset       int64
	EventID      string
	EventType    string
	AggregateID  string
	Reason       string
	Envelope     json.RawMessage
	ObservedAt   time.Time
}

type PoisonMessageRecord struct {
	ID           uint64          `json:"id"`
	ConsumerName string          `json:"consumer_name"`
	Topic        string          `json:"topic"`
	Partition    int             `json:"partition"`
	Offset       int64           `json:"offset"`
	EventID      string          `json:"event_id,omitempty"`
	EventType    string          `json:"event_type,omitempty"`
	AggregateID  string          `json:"aggregate_id,omitempty"`
	Reason       string          `json:"reason"`
	Envelope     json.RawMessage `json:"envelope,omitempty"`
	ObservedAt   time.Time       `json:"observed_at"`
}

type CompensationInput struct {
	ID         string
	SourceType string
	SourceID   string
	Action     string
	Reason     string
	Actor      string
	Status     string
	Metadata   json.RawMessage
	CreatedAt  time.Time
}

type OperationsStore interface {
	ListDeadLetterOutboxEvents(context.Context, int) ([]OutboxRecord, error)
	GetOutboxEvent(context.Context, string) (OutboxRecord, error)
	ReplayOutboxEvent(context.Context, string, time.Time) error
	ListPoisonMessages(context.Context, int) ([]PoisonMessageRecord, error)
	CreateCompensationRecord(context.Context, CompensationInput) (CompensationRecord, error)
	ListCompensationRecords(context.Context, int) ([]CompensationRecord, error)
}
