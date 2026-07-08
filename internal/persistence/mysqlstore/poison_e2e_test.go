package mysqlstore

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
)

func TestKafkaPoisonMessageStoreE2E(t *testing.T) {
	if os.Getenv("AI_COMPANION_E2E") != "1" {
		t.Skip("set AI_COMPANION_E2E=1 with local Docker MySQL to run")
	}
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		t.Fatal("MYSQL_DSN is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	consumer := "ai-companion-e2e-poison-" + e2eUUID(t)
	input := eventbus.PoisonMessageInput{
		ConsumerName: consumer,
		Topic:        "ledger.export.v1",
		Partition:    2,
		Offset:       time.Now().UnixNano(),
		EventID:      "event-fail",
		EventType:    "ledger.export.v1",
		AggregateID:  "export-1",
		Reason:       "poison Kafka message: invalid Kafka event id",
		Envelope:     json.RawMessage(`{"event_id":"event-fail","event_type":"ledger.export.v1"}`),
		ObservedAt:   time.Now().UTC(),
	}
	if err = store.RecordPoisonMessage(ctx, input); err != nil {
		t.Fatalf("record poison message: %v", err)
	}
	if err = store.RecordPoisonMessage(ctx, input); err != nil {
		t.Fatalf("record duplicate poison message: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM kafka_poison_messages WHERE consumer_name=?`, consumer)
	})

	records, err := store.ListPoisonMessages(ctx, 500)
	if err != nil {
		t.Fatalf("list poison messages: %v", err)
	}
	found := 0
	for _, record := range records {
		if record.ConsumerName != consumer {
			continue
		}
		found++
		if record.Topic != input.Topic || record.Partition != input.Partition || record.Offset != input.Offset || record.EventID != input.EventID || !strings.Contains(record.Reason, "invalid Kafka event id") {
			t.Fatalf("unexpected poison record: %#v", record)
		}
	}
	if found != 1 {
		t.Fatalf("found records=%d, want exactly one deduplicated poison message", found)
	}
}
