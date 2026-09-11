package eventbus

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/tracectx"
)

func TestMarshalKafkaEnvelopeIncludesW3CTraceContext(t *testing.T) {
	ctx := tracectx.WithID(context.Background(), "trace-1234567890abcdef")
	event := Event{
		ID: "11111111-1111-4111-8111-111111111111", TraceID: tracectx.ID(ctx),
		TraceParent: tracectx.TraceParent(ctx), TraceState: tracectx.TraceState(ctx),
		AggregateType: "agent_run", AggregateID: "22222222-2222-4222-8222-222222222222",
		Type: "agent.run.requested.v1", Version: 1, Payload: json.RawMessage(`{"run_id":"run-1"}`),
		OccurredAt: time.Date(2026, 9, 4, 1, 2, 3, 0, time.UTC),
	}
	data, err := marshalKafkaEnvelope(event)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err = json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["trace_id"] != event.TraceID || envelope["traceparent"] != event.TraceParent || envelope["event_id"] != event.ID {
		t.Fatalf("envelope=%s", data)
	}
}
