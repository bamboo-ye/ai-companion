package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/twmb/franz-go/pkg/kgo"
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
	envelope, err := json.Marshal(map[string]any{
		"event_id": event.ID, "event_type": event.Type, "event_version": event.Version,
		"aggregate_type": event.AggregateType, "aggregate_id": event.AggregateID,
		"occurred_at": event.OccurredAt.UTC(), "payload": json.RawMessage(event.Payload),
	})
	if err != nil {
		return PublishAck{}, err
	}
	record := &kgo.Record{
		Topic: topic, Key: []byte(event.AggregateID), Value: envelope, Timestamp: event.OccurredAt,
		Headers: []kgo.RecordHeader{
			{Key: "event_id", Value: []byte(event.ID)},
			{Key: "event_type", Value: []byte(event.Type)},
			{Key: "event_version", Value: []byte(strconv.Itoa(event.Version))},
		},
	}
	result, err := p.client.ProduceSync(ctx, record).First()
	if err != nil {
		return PublishAck{}, err
	}
	return PublishAck{Topic: result.Topic, Partition: result.Partition, Offset: result.Offset}, nil
}
