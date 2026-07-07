package mysqlstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
)

func TestKafkaOutboxRelayE2E(t *testing.T) {
	if os.Getenv("AI_COMPANION_E2E") != "1" {
		t.Skip("set AI_COMPANION_E2E=1 with local Docker MySQL/Kafka to run")
	}
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		t.Fatal("MYSQL_DSN is required")
	}
	brokers := splitE2EBrokers(os.Getenv("KAFKA_BROKERS"))
	if len(brokers) == 0 {
		brokers = []string{"127.0.0.1:9092"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	publisher, err := eventbus.NewKafkaPublisher(brokers, "ai-companion-e2e-publisher")
	if err != nil {
		t.Fatalf("new kafka publisher: %v", err)
	}
	defer publisher.Close()
	if err = publisher.Ping(ctx); err != nil {
		t.Fatalf("ping kafka: %v", err)
	}

	eventID := e2eUUID(t)
	aggregateID := e2eUUID(t)
	payload, err := json.Marshal(map[string]string{"job_id": aggregateID})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	now := time.Now().UTC()
	if _, err = store.db.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at,status,available_at) VALUES (UUID_TO_BIN(?),'ledger_export',UUID_TO_BIN(?),'ledger.export.v1',1,?,?,'pending',?)`, eventID, aggregateID, payload, now, now); err != nil {
		t.Fatalf("insert outbox event: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM inbox_events WHERE event_id=UUID_TO_BIN(?)`, eventID)
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM outbox_events WHERE id=UUID_TO_BIN(?)`, eventID)
	})

	relay := eventbus.NewRelay(store, publisher, "ai-companion-e2e-worker", 30*time.Second, 500, 3)
	processed, err := relay.RunOnce(ctx)
	if err != nil {
		t.Fatalf("run relay: %v", err)
	}
	if processed < 1 {
		t.Fatalf("relay processed %d events, want at least 1", processed)
	}

	var status, topic string
	var partition int
	var offset int64
	if err = store.db.QueryRowContext(ctx, `SELECT status,COALESCE(published_topic,''),COALESCE(published_partition,-1),COALESCE(published_offset,-1) FROM outbox_events WHERE id=UUID_TO_BIN(?)`, eventID).Scan(&status, &topic, &partition, &offset); err != nil {
		t.Fatalf("query outbox publish ack: %v", err)
	}
	if status != "published" || topic != "ledger.export.v1" || partition < 0 || offset < 0 {
		t.Fatalf("unexpected outbox ack status=%s topic=%s partition=%d offset=%d", status, topic, partition, offset)
	}

	consumer, err := eventbus.NewKafkaConsumer(brokers, "ai-companion-e2e-consumer", "ai-companion-e2e-"+eventID, []string{"ledger.export.v1"})
	if err != nil {
		t.Fatalf("new kafka consumer: %v", err)
	}
	defer consumer.Close()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pollCtx, pollCancel := context.WithTimeout(ctx, 5*time.Second)
		message, pollErr := consumer.Poll(pollCtx)
		pollCancel()
		if pollErr != nil {
			if ctx.Err() != nil {
				t.Fatalf("poll kafka: %v", pollErr)
			}
			continue
		}
		if commitErr := consumer.Commit(ctx, message); commitErr != nil {
			t.Fatalf("commit kafka message: %v", commitErr)
		}
		if message.Event.ID != eventID {
			continue
		}
		if message.Event.Type != "ledger.export.v1" || message.Event.AggregateID != aggregateID {
			t.Fatalf("unexpected kafka event type=%s aggregate=%s", message.Event.Type, message.Event.AggregateID)
		}
		return
	}
	t.Fatalf("published Kafka event %s was not consumed before deadline", eventID)
}

func splitE2EBrokers(value string) []string {
	parts := strings.Split(value, ",")
	brokers := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			brokers = append(brokers, part)
		}
	}
	return brokers
}

func e2eUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generate uuid: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}
