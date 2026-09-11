package eventbus

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

type fakeConsumer struct {
	messages []KafkaMessage
	errors   []error
	commits  int
}

func (c *fakeConsumer) Poll(context.Context) (KafkaMessage, error) {
	if len(c.errors) > 0 {
		err := c.errors[0]
		c.errors = c.errors[1:]
		return KafkaMessage{}, err
	}
	if len(c.messages) == 0 {
		return KafkaMessage{}, errors.New("no message")
	}
	message := c.messages[0]
	c.messages = c.messages[1:]
	return message, nil
}

func (c *fakeConsumer) Commit(context.Context, KafkaMessage) error {
	c.commits++
	return nil
}

func TestConsumerRunnerProcessesBeforeInboxAndCommitsDuplicate(t *testing.T) {
	store := NewMemoryStore()
	producerCtx := tracectx.WithID(context.Background(), "trace-1234567890abcdef")
	event := Event{ID: "11111111-1111-4111-8111-111111111111", TraceID: tracectx.ID(producerCtx), TraceParent: tracectx.TraceParent(producerCtx), Type: "skill.execute.v1", AggregateID: "22222222-2222-4222-8222-222222222222"}
	consumer := &fakeConsumer{messages: []KafkaMessage{{Event: event}, {Event: event}}}
	calls := 0
	runner := NewConsumerRunner(store, consumer, ProcessorFunc(func(ctx context.Context, _ Event) error {
		calls++
		if got := tracectx.ID(ctx); got != event.TraceID {
			t.Fatalf("processor trace id=%q", got)
		}
		if got := tracectx.SpanID(ctx); got == tracectx.SpanID(producerCtx) {
			t.Fatalf("consumer did not create a child span: %q", got)
		}
		return nil
	}), "skill-worker")
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || consumer.commits != 2 {
		t.Fatalf("calls=%d commits=%d", calls, consumer.commits)
	}
}

func TestConsumerRunnerDoesNotCommitOrRecordInboxOnProcessingFailure(t *testing.T) {
	store := NewMemoryStore()
	event := Event{ID: "33333333-3333-4333-8333-333333333333", Type: "document.ingest.v1", AggregateID: "44444444-4444-4444-8444-444444444444"}
	consumer := &fakeConsumer{messages: []KafkaMessage{{Event: event}}}
	runner := NewConsumerRunner(store, consumer, ProcessorFunc(func(context.Context, Event) error { return errors.New("processing failed") }), "document-worker")
	if err := runner.RunOnce(context.Background()); err == nil {
		t.Fatal("expected processing error")
	}
	seen, _ := store.InboxEventExists(context.Background(), "document-worker", event.ID)
	if seen || consumer.commits != 0 {
		t.Fatalf("seen=%v commits=%d", seen, consumer.commits)
	}
}

func TestConsumerRunnerCommitsInvalidEventIDWithoutProcessing(t *testing.T) {
	store := NewMemoryStore()
	event := Event{ID: "event-fail", Type: "ledger.export.v1", AggregateID: "55555555-5555-4555-8555-555555555555"}
	consumer := &fakeConsumer{messages: []KafkaMessage{{Event: event}}}
	calls := 0
	runner := NewConsumerRunner(store, consumer, ProcessorFunc(func(context.Context, Event) error {
		calls++
		return nil
	}), "ledger-worker")

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	seen, _ := store.InboxEventExists(context.Background(), "ledger-worker", event.ID)
	if calls != 0 || seen || consumer.commits != 1 {
		t.Fatalf("calls=%d seen=%v commits=%d", calls, seen, consumer.commits)
	}
}

func TestConsumerRunnerCommitsPoisonMessageWithoutProcessing(t *testing.T) {
	store := NewMemoryStore()
	consumer := &fakeConsumer{errors: []error{PoisonMessageError{Message: KafkaMessage{}, Cause: errors.New("bad envelope")}}}
	calls := 0
	runner := NewConsumerRunner(store, consumer, ProcessorFunc(func(context.Context, Event) error {
		calls++
		return nil
	}), "chat-worker")

	if err := runner.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || consumer.commits != 1 {
		t.Fatalf("calls=%d commits=%d", calls, consumer.commits)
	}
}

func TestConsumerRunnerRetriesTransientConsumerErrors(t *testing.T) {
	store := NewMemoryStore()
	event := Event{ID: "66666666-6666-4666-8666-666666666666", Type: "memory.extract.v1", AggregateID: "77777777-7777-4777-8777-777777777777"}
	consumer := &fakeConsumer{
		errors:   []error{ErrConsumerUnavailable},
		messages: []KafkaMessage{{Event: event}},
	}
	var calls int32
	runner := NewConsumerRunner(store, consumer, ProcessorFunc(func(context.Context, Event) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}), "memory-worker")
	runner.backoff = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(ctx)
	}()

	deadline := time.After(time.Second)
	for atomic.LoadInt32(&calls) == 0 {
		select {
		case err := <-done:
			t.Fatalf("runner stopped early: %v", err)
		case <-deadline:
			t.Fatal("runner did not retry before deadline")
		default:
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if consumer.commits != 1 {
		t.Fatalf("commits=%d", consumer.commits)
	}
}
