package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestOperatorDLQReplayCreatesCompensation(t *testing.T) {
	store := eventbus.NewMemoryStore()
	now := time.Now().UTC().Add(-time.Minute)
	store.Add(eventbus.Event{ID: "event-dead", AggregateType: "skill_run", AggregateID: "run-1", Type: "skill.execute.v1", Version: 1, Payload: json.RawMessage(`{"run_id":"run-1"}`), OccurredAt: now})
	relay := eventbus.NewRelay(store, alwaysFailPublisher{}, "relay-test", time.Minute, 1, 1)
	relay.SetErrorHandler(func(error) {})
	if count, err := relay.RunOnce(context.Background()); count != 1 || err == nil {
		t.Fatalf("seed dlq count=%d err=%v", count, err)
	}

	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperationsStore(store)

	unauthorized := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/outbox/dead-letter", "", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	listed := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/outbox/dead-letter", "ops-token", "sre-a", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "event-dead") || !strings.Contains(listed.Body.String(), "dead_letter") {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	replayed := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/outbox/event-dead/replay", "ops-token", "sre-a", map[string]string{"reason": "broker outage recovered"})
	if replayed.Code != http.StatusAccepted || !strings.Contains(replayed.Body.String(), `"status":"pending"`) || !strings.Contains(replayed.Body.String(), `"action":"outbox.replay"`) || !strings.Contains(replayed.Body.String(), `"actor":"sre-a"`) {
		t.Fatalf("replayed=%d %s", replayed.Code, replayed.Body.String())
	}
	empty := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/outbox/dead-letter", "ops-token", "sre-a", nil)
	if empty.Code != http.StatusOK || strings.Contains(empty.Body.String(), "event-dead") {
		t.Fatalf("empty=%d %s", empty.Code, empty.Body.String())
	}
	compensations := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/compensations", "ops-token", "sre-a", nil)
	if compensations.Code != http.StatusOK || !strings.Contains(compensations.Body.String(), `"source_id":"event-dead"`) || !strings.Contains(compensations.Body.String(), `"reason":"broker outage recovered"`) {
		t.Fatalf("compensations=%d %s", compensations.Code, compensations.Body.String())
	}
}

func TestOperatorCanCreateManualCompensationRecord(t *testing.T) {
	store := eventbus.NewMemoryStore()
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperationsStore(store)
	created := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/compensations", "ops-token", "sre-b", map[string]any{
		"source_type": "notification", "source_id": "delivery-1", "action": "manual.notify", "reason": "user reported missed delivery", "status": "completed", "metadata": map[string]any{"channel": "support"},
	})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"actor":"sre-b"`) || !strings.Contains(created.Body.String(), `"status":"completed"`) {
		t.Fatalf("created=%d %s", created.Code, created.Body.String())
	}
	invalid := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/compensations", "ops-token", "sre-b", map[string]string{"source_type": "notification"})
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid=%d %s", invalid.Code, invalid.Body.String())
	}
}

func TestOperatorCanListKafkaPoisonMessages(t *testing.T) {
	store := eventbus.NewMemoryStore()
	now := time.Date(2026, 7, 8, 3, 0, 0, 0, time.UTC)
	if err := store.RecordPoisonMessage(context.Background(), eventbus.PoisonMessageInput{
		ConsumerName: "ai-companion-background-v1-ledger", Topic: "ledger.export.v1", Partition: 1, Offset: 42,
		EventID: "event-fail", EventType: "ledger.export.v1", AggregateID: "export-1", Reason: "poison Kafka message: invalid Kafka event id",
		Envelope: json.RawMessage(`{"event_id":"event-fail","event_type":"ledger.export.v1"}`), ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperationsStore(store)

	unauthorized := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/kafka/poison-messages", "", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	listed := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/kafka/poison-messages", "ops-token", "sre-a", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "event-fail") || !strings.Contains(listed.Body.String(), "ledger.export.v1") || !strings.Contains(listed.Body.String(), "invalid Kafka event id") {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
}

type alwaysFailPublisher struct{}

func (alwaysFailPublisher) Publish(context.Context, eventbus.Event) (eventbus.PublishAck, error) {
	return eventbus.PublishAck{}, errors.New("broker unavailable")
}

func performOperatorJSON(t *testing.T, server *Server, method, path, token, actor string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, _ := json.Marshal(body)
		reader = bytes.NewReader(payload)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if actor != "" {
		request.Header.Set("X-Operator-ID", actor)
	}
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}
