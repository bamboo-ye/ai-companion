package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

type KafkaPublisher struct{ client *kgo.Client }

func NewKafkaPublisher(brokers []string, clientID string) (*KafkaPublisher, error) {
	if len(brokers) == 0 {
		return nil, fmt.Errorf("at least one Kafka broker is required")
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID(clientID),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
	)
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{client: client}, nil
}

func (p *KafkaPublisher) Ping(ctx context.Context) error { return p.client.Ping(ctx) }

func (p *KafkaPublisher) Close() { p.client.Close() }

func (p *KafkaPublisher) Publish(ctx context.Context, event Event) (PublishAck, error) {
	topic, err := TopicFor(event.Type)
	if err != nil {
		return PublishAck{}, err
	}
	producerCtx, err := eventProducerContext(ctx, event)
	if err != nil {
		return PublishAck{}, err
	}
	event.TraceID = tracectx.ID(producerCtx)
	event.TraceParent = tracectx.TraceParent(producerCtx)
	event.TraceState = tracectx.TraceState(producerCtx)
	envelope, err := marshalKafkaEnvelope(event)
	if err != nil {
		return PublishAck{}, err
	}
	record := &kgo.Record{
		Topic: topic, Key: []byte(event.AggregateID), Value: envelope, Timestamp: event.OccurredAt,
		Headers: []kgo.RecordHeader{
			{Key: "event_id", Value: []byte(event.ID)},
			{Key: "event_type", Value: []byte(event.Type)},
			{Key: "event_version", Value: []byte(strconv.Itoa(event.Version))},
			{Key: "trace_id", Value: []byte(event.TraceID)},
			{Key: tracectx.TraceParentHeader, Value: []byte(event.TraceParent)},
		},
	}
	if event.TraceState != "" {
		record.Headers = append(record.Headers, kgo.RecordHeader{Key: tracectx.TraceStateHeader, Value: []byte(event.TraceState)})
	}
	result, err := p.client.ProduceSync(producerCtx, record).First()
	if err != nil {
		return PublishAck{}, err
	}
	return PublishAck{Topic: result.Topic, Partition: result.Partition, Offset: result.Offset}, nil
}

func eventProducerContext(ctx context.Context, event Event) (context.Context, error) {
	if strings.TrimSpace(event.TraceParent) != "" || strings.TrimSpace(event.TraceID) != "" {
		restored, ok := tracectx.FromPropagation(ctx, event.TraceParent, event.TraceState, event.TraceID)
		if !ok {
			return ctx, fmt.Errorf("invalid event trace context")
		}
		return tracectx.Child(restored), nil
	}
	return tracectx.Child(ctx), nil
}

func marshalKafkaEnvelope(event Event) ([]byte, error) {
	return json.Marshal(map[string]any{
		"event_id": event.ID, "event_type": event.Type, "event_version": event.Version,
		"aggregate_type": event.AggregateType, "aggregate_id": event.AggregateID,
		"trace_id": event.TraceID, "traceparent": event.TraceParent, "tracestate": event.TraceState,
		"occurred_at": event.OccurredAt.UTC(), "payload": json.RawMessage(event.Payload),
	})
}
