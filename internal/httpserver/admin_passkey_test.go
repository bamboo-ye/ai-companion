package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/adminpasskey"
	"github.com/windcry1/ai-companion/internal/adminpasskey/passkeytest"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/platform/config"
)

func passkeyServer(t *testing.T, role string) (*Server, *passkeytest.Authenticator, *http.Cookie) {
	t.Helper()
	account, err := opsauth.BootstrapAccount("alice", "Alice", role, "old-operator-token", "", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Config{Environment: "test", WebOrigin: "https://admin.example.com", OperatorMFARequired: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetOperatorAuthStore(opsauth.NewMemoryStore(account))
	if s.adminPasskeys == nil {
		t.Fatal("passkeys not configured")
	}
	invite, err := s.adminPasskeys.IssueInvite(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	begin := adminRequest(s, "POST", "/v1/ops/auth/passkey/register/options", passkeytest.JSON(t, map[string]string{"invite": invite}), nil, true)
	if begin.Code != 200 {
		t.Fatalf("begin: %d %s", begin.Code, begin.Body.String())
	}
	var opts any
	_ = json.Unmarshal(begin.Body.Bytes(), &opts)
	device := passkeytest.New(t)
	payload := device.Registration(t, opts, "https://admin.example.com", "admin.example.com", true)
	finish := adminRequest(s, "POST", "/v1/ops/auth/passkey/register/verify", payload, begin.Result().Cookies(), true)
	if finish.Code != 200 {
		t.Fatalf("finish: %d %s", finish.Code, finish.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range finish.Result().Cookies() {
		if c.Name == "__Host-ai_admin" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("insecure session cookie: %+v", cookie)
	}
	return s, device, cookie
}
func adminRequest(s *Server, method, path string, body []byte, cookies []*http.Cookie, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	for _, c := range cookies {
		r.AddCookie(c)
	}
	if csrf {
		r.Header.Set("Origin", "https://admin.example.com")
		r.Header.Set("X-Admin-CSRF", "1")
	}
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, r)
	return w
}
func TestAdminPasskeyHTTPFlowAndCookieIsolation(t *testing.T) {
	s, device, cookie := passkeyServer(t, "admin")
	bootstrap := adminRequest(s, "GET", "/v1/ops/console/bootstrap", nil, []*http.Cookie{cookie}, false)
	if bootstrap.Code != 200 || !strings.Contains(bootstrap.Body.String(), `"mfa_verified":true`) || strings.Contains(bootstrap.Body.String(), `"legacy":true`) {
		t.Fatalf("bootstrap: %d %s", bootstrap.Code, bootstrap.Body.String())
	}
	for _, cookies := range [][]*http.Cookie{nil, {{Name: "user_session", Value: cookie.Value}}, {{Name: "__Host-ai_admin", Value: "forged"}}} {
		if w := adminRequest(s, "GET", "/v1/ops/console/bootstrap", nil, cookies, false); w.Code != 401 {
			t.Fatalf("untrusted cookie accepted: %d", w.Code)
		}
	}
	begin := adminRequest(s, "POST", "/v1/ops/auth/passkey/login/options", []byte("{}"), []*http.Cookie{cookie}, true)
	var options any
	_ = json.Unmarshal(begin.Body.Bytes(), &options)
	payload := device.Assertion(t, options, "https://admin.example.com", "admin.example.com", true)
	loginCookies := append(begin.Result().Cookies(), cookie)
	finish := adminRequest(s, "POST", "/v1/ops/auth/passkey/login/verify", payload, loginCookies, true)
	if finish.Code != 200 {
		t.Fatalf("login: %d %s", finish.Code, finish.Body.String())
	}
	if w := adminRequest(s, "GET", "/v1/ops/console/bootstrap", nil, []*http.Cookie{cookie}, false); w.Code != 401 {
		t.Fatal("previous session was not rotated")
	}
	if w := adminRequest(s, "POST", "/v1/ops/auth/passkey/login/verify", payload, loginCookies, true); w.Code != 401 {
		t.Fatal("challenge replay accepted")
	}
	var newCookie *http.Cookie
	for _, c := range finish.Result().Cookies() {
		if c.Name == cookie.Name {
			newCookie = c
		}
	}
	if w := adminRequest(s, "POST", "/v1/ops/auth/logout", nil, []*http.Cookie{newCookie}, true); w.Code != 204 {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := adminRequest(s, "GET", "/v1/ops/console/bootstrap", nil, []*http.Cookie{newCookie}, false); w.Code != 401 {
		t.Fatal("logout did not revoke session")
	}
}
func TestAdminPasskeyCSRFAndRecentVerification(t *testing.T) {
	s, _, cookie := passkeyServer(t, "admin")
	for _, path := range []string{"/v1/ops/auth/passkey/login/options", "/v1/ops/auth/passkey/register/options", "/v1/ops/auth/logout", "/v1/ops/auth/invitations"} {
		if w := adminRequest(s, "POST", path, []byte("{}"), []*http.Cookie{cookie}, false); w.Code != 403 {
			t.Fatalf("missing CSRF accepted for %s: %d", path, w.Code)
		}
		r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		r.AddCookie(cookie)
		r.Header.Set("Origin", "https://evil.example.com")
		r.Header.Set("X-Admin-CSRF", "1")
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("foreign origin accepted: %s %d", path, w.Code)
		}
	}
	ctx := context.Background()
	record, err := s.adminPasskeys.Store.Get(ctx, adminpasskey.Key("session", cookie.Value))
	if err != nil {
		t.Fatal(err)
	}
	var session adminpasskey.Session
	_ = json.Unmarshal(record.Data, &session)
	session.AuthenticatedAt = time.Now().Add(-6 * time.Minute)
	record.Data = passkeytest.JSON(t, session)
	if err = s.adminPasskeys.Store.Update(ctx, record); err != nil {
		t.Fatal(err)
	}
	w := adminRequest(s, "POST", "/v1/ops/auth/invitations", []byte(`{"id":"bob","name":"Bob","role":"viewer","reason":"test"}`), []*http.Cookie{cookie}, true)
	if w.Code != 401 || !strings.Contains(w.Body.String(), "operator_reauth_required") {
		t.Fatalf("stale MFA accepted: %d %s", w.Code, w.Body.String())
	}
	if _, err = s.operatorAuth.Get(ctx, "bob"); err == nil {
		t.Fatal("mutation ran before step-up")
	}
	if w = adminRequest(s, "GET", "/v1/ops/console/bootstrap", nil, []*http.Cookie{cookie}, false); w.Code != 200 {
		t.Fatal("read requires unnecessary step-up")
	}
}
func TestAdminInvitationsRespectRolesAndCannotReplaceEnrolledAccount(t *testing.T) {
	for _, role := range []string{"viewer", "support", "admin"} {
		t.Run(role, func(t *testing.T) {
			s, _, cookie := passkeyServer(t, role)
			body := []byte(`{"id":"bob","name":"Bob","role":"viewer","reason":"test invitation"}`)
			w := adminRequest(s, "POST", "/v1/ops/auth/invitations", body, []*http.Cookie{cookie}, true)
			if role != "admin" {
				if w.Code != 403 {
					t.Fatalf("role escalation: %d", w.Code)
				}
				return
			}
			if w.Code != 201 {
				t.Fatalf("invite: %d %s", w.Code, w.Body.String())
			}
			var first struct {
				Invite string `json:"invite"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &first)
			w = adminRequest(s, "POST", "/v1/ops/auth/invitations", body, []*http.Cookie{cookie}, true)
			if w.Code != 201 {
				t.Fatalf("reissue: %d %s", w.Code, w.Body.String())
			}
			if _, _, err := s.adminPasskeys.BeginRegistration(context.Background(), first.Invite); err == nil {
				t.Fatal("old invitation still valid")
			}
			w = adminRequest(s, "POST", "/v1/ops/auth/invitations", []byte(`{"id":"alice","name":"Alice","role":"admin","reason":"replace enrolled account"}`), []*http.Cookie{cookie}, true)
			if w.Code != 409 {
				t.Fatalf("enrolled account could be replaced: %d", w.Code)
			}
		})
	}
}
