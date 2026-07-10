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

	"github.com/windcry1/ai-companion/internal/email"
	"github.com/windcry1/ai-companion/internal/eventbus"
	"github.com/windcry1/ai-companion/internal/opsauth"
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

func TestOperatorCanInspectAndReplayFailedEmailDelivery(t *testing.T) {
	opsStore := eventbus.NewMemoryStore()
	emailStore := email.NewMemoryStore()
	now := time.Date(2026, 7, 8, 10, 0, 0, 0, time.UTC)
	if err := emailStore.CreateDelivery(context.Background(), email.Delivery{
		ID: "11111111-1111-4111-8111-111111111111", ResourceType: "workspace_invitation", ResourceID: "22222222-2222-4222-8222-222222222222",
		Template: "workspace.invitation.v1", RecipientEmail: "invitee@example.com", Subject: "邀请", BodyText: "hello",
		Status: "failed", FailureCode: "send_failed", CreatedAt: now, UpdatedAt: now, AvailableAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := emailStore.CreateDelivery(context.Background(), email.Delivery{
		ID: "33333333-3333-4333-8333-333333333333", ResourceType: "workspace_invitation", ResourceID: "44444444-4444-4444-8444-444444444444",
		Template: "workspace.invitation.v1", RecipientEmail: "ok@example.com", Subject: "邀请", BodyText: "hello",
		Status: "sent", CreatedAt: now, UpdatedAt: now, AvailableAt: now, SentAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperationsStore(opsStore)
	server.emails = email.NewService(emailStore, email.NoopSender{})

	listed := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/email/deliveries?status=failed", "ops-token", "sre-mail", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "invitee@example.com") || strings.Contains(listed.Body.String(), "ok@example.com") {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	detail := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/email/deliveries/11111111-1111-4111-8111-111111111111", "ops-token", "sre-mail", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"failure_code":"send_failed"`) {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}
	replayed := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/email/deliveries/11111111-1111-4111-8111-111111111111/replay", "ops-token", "sre-mail", map[string]string{"reason": "SMTP outage recovered"})
	if replayed.Code != http.StatusAccepted || !strings.Contains(replayed.Body.String(), `"status":"queued"`) || !strings.Contains(replayed.Body.String(), `"action":"email.delivery.replay"`) {
		t.Fatalf("replayed=%d %s", replayed.Code, replayed.Body.String())
	}
	replaySent := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/email/deliveries/33333333-3333-4333-8333-333333333333/replay", "ops-token", "sre-mail", map[string]string{"reason": "should fail"})
	if replaySent.Code != http.StatusConflict {
		t.Fatalf("replay sent=%d %s", replaySent.Code, replaySent.Body.String())
	}
	compensations := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/compensations", "ops-token", "sre-mail", nil)
	if compensations.Code != http.StatusOK || !strings.Contains(compensations.Body.String(), `"source_type":"email_delivery"`) || !strings.Contains(compensations.Body.String(), `"reason":"SMTP outage recovered"`) {
		t.Fatalf("compensations=%d %s", compensations.Code, compensations.Body.String())
	}
}

func TestOperatorCanModerateUsersAndInspectAuditLogs(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-user-secret-with-enough-entropy", OperatorToken: "ops-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	register := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "moderation-target@example.com", "password": "correct-horse-battery", "display_name": "Moderation Target", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "moderation-target", "name": "web", "platform": "web"},
	})
	var tokens struct {
		AccessToken string `json:"access_token"`
		User        struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(register.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}

	listed := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/users?q=moderation-target", "ops-token", "trust-safety", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"email":"moderation-target@example.com"`) || !strings.Contains(listed.Body.String(), `"status":"active"`) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	disabled := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/users/"+tokens.User.ID+"/disable", "ops-token", "trust-safety", map[string]string{"reason": "confirmed abuse report"})
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"status":"disabled"`) {
		t.Fatalf("disabled=%d %s", disabled.Code, disabled.Body.String())
	}
	authAfterDisable := performJSON(t, server, http.MethodGet, "/v1/characters", tokens.AccessToken, nil)
	if authAfterDisable.Code != http.StatusUnauthorized {
		t.Fatalf("auth after disable=%d %s", authAfterDisable.Code, authAfterDisable.Body.String())
	}
	loginAfterDisable := performJSON(t, server, http.MethodPost, "/v1/auth/login", "", map[string]any{
		"email": "moderation-target@example.com", "password": "correct-horse-battery", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "moderation-target-2", "name": "web", "platform": "web"},
	})
	if loginAfterDisable.Code != http.StatusUnauthorized {
		t.Fatalf("login after disable=%d %s", loginAfterDisable.Code, loginAfterDisable.Body.String())
	}
	detail := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/users/"+tokens.User.ID, "ops-token", "trust-safety", nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"status":"disabled"`) {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}
	audits := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/audit-logs?resource_type=user&resource_id="+tokens.User.ID, "ops-token", "trust-safety", nil)
	if audits.Code != http.StatusOK || !strings.Contains(audits.Body.String(), `"action":"user.status.update"`) || !strings.Contains(audits.Body.String(), `"actor_label":"trust-safety"`) || !strings.Contains(audits.Body.String(), "confirmed abuse report") {
		t.Fatalf("audits=%d %s", audits.Code, audits.Body.String())
	}
	enabled := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/users/"+tokens.User.ID+"/enable", "ops-token", "trust-safety", map[string]string{"reason": "appeal accepted"})
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"status":"active"`) {
		t.Fatalf("enabled=%d %s", enabled.Code, enabled.Body.String())
	}
	loginAfterEnable := performJSON(t, server, http.MethodPost, "/v1/auth/login", "", map[string]any{
		"email": "moderation-target@example.com", "password": "correct-horse-battery", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "moderation-target-3", "name": "web", "platform": "web"},
	})
	if loginAfterEnable.Code != http.StatusOK || !strings.Contains(loginAfterEnable.Body.String(), `"access_token"`) {
		t.Fatalf("login after enable=%d %s", loginAfterEnable.Code, loginAfterEnable.Body.String())
	}
}

func TestOperatorAccountRequiresTOTPAndRoleForMutations(t *testing.T) {
	store := eventbus.NewMemoryStore()
	now := time.Now().UTC().Add(-time.Minute)
	store.Add(eventbus.Event{ID: "event-mfa", AggregateType: "skill_run", AggregateID: "run-mfa", Type: "skill.execute.v1", Version: 1, Payload: json.RawMessage(`{"run_id":"run-mfa"}`), OccurredAt: now})
	relay := eventbus.NewRelay(store, alwaysFailPublisher{}, "relay-mfa-test", time.Minute, 1, 1)
	relay.SetErrorHandler(func(error) {})
	if count, err := relay.RunOnce(context.Background()); count != 1 || err == nil {
		t.Fatalf("seed dlq count=%d err=%v", count, err)
	}

	secret := "JBSWY3DPEHPK3PXP"
	support, err := opsauth.BootstrapAccount("support-1", "Support One", "support", "support-token", secret, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := opsauth.BootstrapAccount("viewer-1", "Viewer One", "viewer", "viewer-token", secret, true, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-mfa-secret-with-enough-entropy", OperatorToken: "legacy-token", OperatorMFARequired: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperationsStore(store)
	server.SetOperatorAuthStore(opsauth.NewMemoryStore(support, viewer))

	missingOTP := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/outbox/dead-letter", "support-token", "", nil)
	if missingOTP.Code != http.StatusUnauthorized {
		t.Fatalf("missing otp=%d %s", missingOTP.Code, missingOTP.Body.String())
	}
	legacyBlocked := performOperatorJSON(t, server, http.MethodGet, "/v1/ops/outbox/dead-letter", "legacy-token", "legacy", nil)
	if legacyBlocked.Code != http.StatusUnauthorized || !strings.Contains(legacyBlocked.Body.String(), "operator_mfa_required") {
		t.Fatalf("legacy blocked=%d %s", legacyBlocked.Code, legacyBlocked.Body.String())
	}
	otp, err := opsauth.GenerateTOTP(secret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	listed := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/outbox/dead-letter", "viewer-token", "", otp, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "event-mfa") {
		t.Fatalf("viewer listed=%d %s", listed.Code, listed.Body.String())
	}
	viewerReplay := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/outbox/event-mfa/replay", "viewer-token", "", otp, map[string]string{"reason": "should be forbidden"})
	if viewerReplay.Code != http.StatusForbidden || !strings.Contains(viewerReplay.Body.String(), "operator_forbidden") {
		t.Fatalf("viewer replay=%d %s", viewerReplay.Code, viewerReplay.Body.String())
	}
	replayed := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/outbox/event-mfa/replay", "support-token", "", otp, map[string]string{"reason": "MFA verified replay"})
	if replayed.Code != http.StatusAccepted || !strings.Contains(replayed.Body.String(), `"actor":"support-1"`) || !strings.Contains(replayed.Body.String(), `"status":"pending"`) {
		t.Fatalf("support replay=%d %s", replayed.Code, replayed.Body.String())
	}
}

func TestOperatorAccountManagementLifecycle(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "ops-account-secret-with-enough-entropy", OperatorToken: "legacy-root-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.SetOperatorAuthStore(opsauth.NewMemoryStore())

	adminCreated := performOperatorJSON(t, server, http.MethodPost, "/v1/ops/operators", "legacy-root-token", "bootstrap-root", map[string]any{
		"id": "admin-1", "display_name": "Admin One", "role": "admin", "mfa_enabled": true, "reason": "bootstrap first admin",
	})
	if adminCreated.Code != http.StatusCreated || !strings.Contains(adminCreated.Body.String(), `"id":"admin-1"`) || !strings.Contains(adminCreated.Body.String(), `"token":"op_`) || strings.Contains(adminCreated.Body.String(), "token_hash") {
		t.Fatalf("admin created=%d %s", adminCreated.Code, adminCreated.Body.String())
	}
	var adminEnvelope struct {
		Token      string `json:"token"`
		TOTPSecret string `json:"totp_secret"`
	}
	if err := json.Unmarshal(adminCreated.Body.Bytes(), &adminEnvelope); err != nil {
		t.Fatal(err)
	}
	adminOTP, err := opsauth.GenerateTOTP(adminEnvelope.TOTPSecret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	supportCreated := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/operators", adminEnvelope.Token, "", adminOTP, map[string]any{
		"id": "support-2", "display_name": "Support Two", "role": "support", "mfa_enabled": true, "reason": "bootstrap support operator",
	})
	if supportCreated.Code != http.StatusCreated || !strings.Contains(supportCreated.Body.String(), `"role":"support"`) || !strings.Contains(supportCreated.Body.String(), `"totp_secret"`) {
		t.Fatalf("support created=%d %s", supportCreated.Code, supportCreated.Body.String())
	}
	var supportEnvelope struct {
		Token      string `json:"token"`
		TOTPSecret string `json:"totp_secret"`
	}
	if err := json.Unmarshal(supportCreated.Body.Bytes(), &supportEnvelope); err != nil {
		t.Fatal(err)
	}
	supportOTP, err := opsauth.GenerateTOTP(supportEnvelope.TOTPSecret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	viewerCreated := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/operators", adminEnvelope.Token, "", adminOTP, map[string]any{
		"id": "viewer-2", "display_name": "Viewer Two", "role": "viewer", "mfa_enabled": true, "reason": "bootstrap read-only operator",
	})
	if viewerCreated.Code != http.StatusCreated {
		t.Fatalf("viewer created=%d %s", viewerCreated.Code, viewerCreated.Body.String())
	}
	var viewerEnvelope struct {
		Token      string `json:"token"`
		TOTPSecret string `json:"totp_secret"`
	}
	if err := json.Unmarshal(viewerCreated.Body.Bytes(), &viewerEnvelope); err != nil {
		t.Fatal(err)
	}
	viewerOTP, err := opsauth.GenerateTOTP(viewerEnvelope.TOTPSecret, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	viewerCreate := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/operators", viewerEnvelope.Token, "", viewerOTP, map[string]any{
		"id": "blocked-admin", "display_name": "Blocked Admin", "role": "admin", "mfa_enabled": true, "reason": "should be forbidden",
	})
	if viewerCreate.Code != http.StatusForbidden {
		t.Fatalf("viewer create=%d %s", viewerCreate.Code, viewerCreate.Body.String())
	}

	supportDeliveries := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/email/deliveries", supportEnvelope.Token, "", supportOTP, nil)
	if supportDeliveries.Code != http.StatusOK {
		t.Fatalf("support deliveries=%d %s", supportDeliveries.Code, supportDeliveries.Body.String())
	}
	listed := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/operators?role=support", adminEnvelope.Token, "", adminOTP, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"support-2"`) || strings.Contains(listed.Body.String(), `"id":"viewer-2"`) {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}

	reset := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/operators/support-2/reset-token", adminEnvelope.Token, "", adminOTP, map[string]string{"reason": "suspected token exposure"})
	if reset.Code != http.StatusOK || !strings.Contains(reset.Body.String(), `"token":"op_`) {
		t.Fatalf("reset=%d %s", reset.Code, reset.Body.String())
	}
	var resetEnvelope struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(reset.Body.Bytes(), &resetEnvelope); err != nil {
		t.Fatal(err)
	}
	oldToken := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/email/deliveries", supportEnvelope.Token, "", supportOTP, nil)
	if oldToken.Code != http.StatusUnauthorized {
		t.Fatalf("old token=%d %s", oldToken.Code, oldToken.Body.String())
	}
	newToken := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/email/deliveries", resetEnvelope.Token, "", supportOTP, nil)
	if newToken.Code != http.StatusOK {
		t.Fatalf("new token=%d %s", newToken.Code, newToken.Body.String())
	}
	disabled := performOperatorJSONWithOTP(t, server, http.MethodPost, "/v1/ops/operators/support-2/disable", adminEnvelope.Token, "", adminOTP, map[string]string{"reason": "contract ended"})
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"status":"disabled"`) {
		t.Fatalf("disabled=%d %s", disabled.Code, disabled.Body.String())
	}
	disabledToken := performOperatorJSONWithOTP(t, server, http.MethodGet, "/v1/ops/email/deliveries", resetEnvelope.Token, "", supportOTP, nil)
	if disabledToken.Code != http.StatusUnauthorized {
		t.Fatalf("disabled token=%d %s", disabledToken.Code, disabledToken.Body.String())
	}
}

type alwaysFailPublisher struct{}

func (alwaysFailPublisher) Publish(context.Context, eventbus.Event) (eventbus.PublishAck, error) {
	return eventbus.PublishAck{}, errors.New("broker unavailable")
}

func performOperatorJSON(t *testing.T, server *Server, method, path, token, actor string, body any) *httptest.ResponseRecorder {
	return performOperatorJSONWithOTP(t, server, method, path, token, actor, "", body)
}

func performOperatorJSONWithOTP(t *testing.T, server *Server, method, path, token, actor, otp string, body any) *httptest.ResponseRecorder {
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
	if otp != "" {
		request.Header.Set("X-Operator-TOTP", otp)
	}
	response := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(response, request)
	return response
}
