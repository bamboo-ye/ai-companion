package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

var ErrConsumerUnavailable = errors.New("Kafka consumer unavailable")

type PoisonMessageError struct {
	Message KafkaMessage
	Cause   error
}

func (e PoisonMessageError) Error() string {
	if e.Cause == nil {
		return "poison Kafka message"
	}
	return "poison Kafka message: " + e.Cause.Error()
}

func (e PoisonMessageError) Unwrap() error { return e.Cause }

type KafkaMessage struct {
	Event  Event
	record *kgo.Record
}

type KafkaConsumer struct{ client *kgo.Client }

type Consumer interface {
	Poll(context.Context) (KafkaMessage, error)
	Commit(context.Context, KafkaMessage) error
}

func NewKafkaConsumer(brokers []string, clientID, group string, topics []string) (*KafkaConsumer, error) {
	if len(brokers) == 0 || group == "" || len(topics) == 0 {
		return nil, fmt.Errorf("Kafka brokers, group and topics are required")
	}
	for _, topic := range topics {
		if _, err := TopicFor(topic); err != nil {
			return nil, err
		}
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...), kgo.ClientID(clientID), kgo.ConsumerGroup(group), kgo.ConsumeTopics(topics...),
		kgo.DisableAutoCommit(), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, err
	}
	return &KafkaConsumer{client: client}, nil
}

func (c *KafkaConsumer) Close() { c.client.Close() }

func (c *KafkaConsumer) Poll(ctx context.Context) (KafkaMessage, error) {
	var record *kgo.Record
	for record == nil {
		fetches := c.client.PollRecords(ctx, 1)
		if err := fetches.Err(); err != nil {
			return KafkaMessage{}, fmt.Errorf("%w: %v", ErrConsumerUnavailable, err)
		}
		records := fetches.Records()
		if len(records) > 0 {
			record = records[0]
		}
	}
	var envelope struct {
		EventID       string          `json:"event_id"`
		EventType     string          `json:"event_type"`
		EventVersion  int             `json:"event_version"`
		AggregateType string          `json:"aggregate_type"`
		AggregateID   string          `json:"aggregate_id"`
		OccurredAt    time.Time       `json:"occurred_at"`
		Payload       json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		return KafkaMessage{}, PoisonMessageError{Message: KafkaMessage{record: record}, Cause: fmt.Errorf("decode Kafka envelope: %w", err)}
	}
	if envelope.EventID == "" || envelope.EventType != record.Topic || envelope.EventVersion < 1 || envelope.AggregateID == "" {
		return KafkaMessage{}, PoisonMessageError{Message: KafkaMessage{record: record}, Cause: errors.New("invalid Kafka event envelope")}
	}
	if !isUUID(envelope.EventID) {
		return KafkaMessage{}, PoisonMessageError{Message: KafkaMessage{record: record}, Cause: fmt.Errorf("invalid Kafka event id %q", envelope.EventID)}
	}
	return KafkaMessage{Event: Event{ID: envelope.EventID, AggregateType: envelope.AggregateType, AggregateID: envelope.AggregateID, Type: envelope.EventType, Version: envelope.EventVersion, Payload: envelope.Payload, OccurredAt: envelope.OccurredAt}, record: record}, nil
}

func (c *KafkaConsumer) Commit(ctx context.Context, message KafkaMessage) error {
	if message.record == nil {
		return errors.New("Kafka record is missing")
	}
	if err := c.client.CommitRecords(ctx, message.record); err != nil {
		return fmt.Errorf("%w: %v", ErrConsumerUnavailable, err)
	}
	return nil
}

type Processor interface {
	Process(context.Context, Event) error
}

type ProcessorFunc func(context.Context, Event) error

func (f ProcessorFunc) Process(ctx context.Context, event Event) error { return f(ctx, event) }

type ConsumerRunner struct {
	store     Store
	consumer  Consumer
	processor Processor
	name      string
	now       func() time.Time
	backoff   time.Duration
	onError   func(error)
}

func NewConsumerRunner(store Store, consumer Consumer, processor Processor, name string) *ConsumerRunner {
	return &ConsumerRunner{store: store, consumer: consumer, processor: processor, name: name, now: time.Now, backoff: time.Second}
}

func (r *ConsumerRunner) SetErrorHandler(handler func(error)) { r.onError = handler }

func (r *ConsumerRunner) Run(ctx context.Context) error {
	for {
		if err := r.RunOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ErrConsumerUnavailable) {
				r.report(err)
				if !sleepWithContext(ctx, r.backoff) {
					return nil
				}
				continue
			}
			return err
		}
	}
}

func (r *ConsumerRunner) RunOnce(ctx context.Context) error {
	message, err := r.consumer.Poll(ctx)
	if err != nil {
		var poison PoisonMessageError
		if errors.As(err, &poison) {
			r.report(err)
			return r.consumer.Commit(ctx, poison.Message)
		}
		return err
	}
	if !isUUID(message.Event.ID) {
		r.report(PoisonMessageError{Message: message, Cause: fmt.Errorf("invalid Kafka event id %q", message.Event.ID)})
		return r.consumer.Commit(ctx, message)
	}
	seen, err := r.store.InboxEventExists(ctx, r.name, message.Event.ID)
	if err != nil {
		return err
	}
	if !seen {
		if err = r.processor.Process(ctx, message.Event); err != nil {
			return err
		}
		if _, err = r.store.RecordInboxEvent(ctx, r.name, message.Event.ID, r.now().UTC()); err != nil {
			return err
		}
	}
	return r.consumer.Commit(ctx, message)
}

func (r *ConsumerRunner) report(err error) {
	if r.onError != nil {
		r.onError(err)
	}
}

func sleepWithContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		duration = time.Second
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func isUUID(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 {
		return false
	}
	for index, current := range value {
		switch index {
		case 8, 13, 18, 23:
			if current != '-' {
				return false
			}
		default:
			if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
				return false
			}
		}
	}
	return true
}
