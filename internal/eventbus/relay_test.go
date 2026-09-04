package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakePublisher struct {
	failures int
	calls    int
}

func (p *fakePublisher) Publish(_ context.Context, event Event) (PublishAck, error) {
	p.calls++
	if p.calls <= p.failures {
		return PublishAck{}, errors.New("broker unavailable")
	}
	return PublishAck{Topic: event.Type, Partition: 1, Offset: int64(p.calls)}, nil
}

func TestRelayPublishesAndInboxDeduplicates(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 6, 8, 0, 0, 0, time.UTC)
	store.Add(Event{ID: "event-1", AggregateType: "document", AggregateID: "document-1", Type: "document.ingest.v1", Version: 1, Payload: json.RawMessage(`{"document_id":"document-1"}`), OccurredAt: now})
	publisher := &fakePublisher{}
	relay := NewRelay(store, publisher, "relay-1", time.Minute, 10, 3)
	relay.now = func() time.Time { return now }
	count, err := relay.RunOnce(context.Background())
	if err != nil || count != 1 || publisher.calls != 1 || store.events["event-1"].status != "published" {
		t.Fatalf("relay count=%d calls=%d state=%#v err=%v", count, publisher.calls, store.events["event-1"], err)
	}
	first, err := store.RecordInboxEvent(context.Background(), "document-worker", "event-1", now)
	duplicate, err2 := store.RecordInboxEvent(context.Background(), "document-worker", "event-1", now.Add(time.Second))
	if err != nil || err2 != nil || !first || duplicate {
		t.Fatalf("inbox first=%v duplicate=%v err=%v/%v", first, duplicate, err, err2)
	}
}

func TestDatabaseReconciledPublisherSettlesOutboxWithoutKafka(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	store.Add(Event{ID: "event-database", AggregateType: "agent_run", AggregateID: "run-1", Type: "agent.run.requested.v1", Version: 1, Payload: json.RawMessage(`{"run_id":"run-1"}`), OccurredAt: now})
	relay := NewRelay(store, DatabaseReconciledPublisher{}, "database-relay", time.Minute, 10, 3)
	relay.now = func() time.Time { return now }

	count, err := relay.RunOnce(context.Background())
	item := store.events["event-database"]
	if err != nil || count != 1 || item.status != "published" || item.ack.Topic != DatabaseReconciledTopic {
		t.Fatalf("relay count=%d state=%#v err=%v", count, item, err)
	}
}

func TestRelayRetriesWithBackoffThenMovesToDLQAndReplays(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 7, 6, 9, 0, 0, 0, time.UTC)
	store.Add(Event{ID: "event-fail", AggregateType: "skill_run", AggregateID: "run-1", Type: "skill.execute.v1", Version: 1, Payload: json.RawMessage(`{}`), OccurredAt: now})
	publisher := &fakePublisher{failures: 10}
	relay := NewRelay(store, publisher, "relay-1", time.Minute, 1, 3)
	current := now
	relay.now = func() time.Time { return current }
	for attempt := 1; attempt <= 3; attempt++ {
		count, err := relay.RunOnce(context.Background())
		if count != 1 || err == nil {
			t.Fatalf("attempt %d count=%d err=%v", attempt, count, err)
		}
		current = store.events["event-fail"].availableAt
	}
	if item := store.events["event-fail"]; item.status != "dead_letter" || item.event.Attempts != 3 {
		t.Fatalf("dlq state=%#v", item)
	}
	if err := store.ReplayOutboxEvent(context.Background(), "event-fail", current); err != nil || store.events["event-fail"].status != "pending" || store.events["event-fail"].event.Attempts != 0 {
		t.Fatalf("replay state=%#v err=%v", store.events["event-fail"], err)
	}
}

func TestTopicForRejectsUntrustedNames(t *testing.T) {
	if _, err := TopicFor("acceptance.untrusted.v1"); err == nil {
		t.Fatal("expected invalid topic error")
	}
	if topic, err := TopicFor("skill.execute.v1"); err != nil || topic != "skill.execute.v1" {
		t.Fatalf("topic=%q err=%v", topic, err)
	}
	for _, topic := range []string{"chat.command.v1", "agent.run.requested.v1", "agent.run.resume.requested.v1", "memory.extract.v1", "document.cleanup.v1", "skill.run.failed.v1", "skill.run.cancelled.v1", "ledger.export.v1", "reminder.rescheduled.v1", "notification.deliver.v1", "email.deliver.v1"} {
		if actual, err := TopicFor(topic); err != nil || actual != topic {
			t.Fatalf("topic=%q err=%v", actual, err)
		}
	}
}
