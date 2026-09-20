package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestAdminQuotaPolicyHTTPAndResourceEnforcement(t *testing.T) {
	s, _, cookie := passkeyServer(t, "admin")
	token, user := registerBillingUser(t, s, "quota-policy@example.com", "quota-policy")
	set := func(path, payload string, want int) {
		t.Helper()
		w := adminRequest(s, "PUT", path, []byte(payload), []*http.Cookie{cookie}, true)
		if w.Code != want {
			t.Fatalf("PUT %s = %d %s", path, w.Code, w.Body.String())
		}
	}
	set("/v1/ops/billing/quotas", `{"limits":{"workspaces":0},"revision":0,"reason":"global freeze"}`, 200)
	w := performJSON(t, s, "POST", "/v1/workspaces", token, map[string]string{"name": "blocked"})
	if w.Code != 402 {
		t.Fatalf("global limit ignored: %d %s", w.Code, w.Body.String())
	}
	path := "/v1/ops/billing/users/" + user + "/quotas"
	set(path, `{"limits":{"workspaces":3},"revision":0,"reason":"individual exception"}`, 200)
	w = performJSON(t, s, "POST", "/v1/workspaces", token, map[string]string{"name": "allowed"})
	if w.Code != 201 {
		t.Fatalf("user override ignored: %d %s", w.Code, w.Body.String())
	}
	w = performJSON(t, s, "GET", "/v1/billing/me", token, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"limit_source":"user"`) {
		t.Fatalf("effective summary: %d %s", w.Code, w.Body.String())
	}
	set(path, `{"limits":{},"revision":0,"reason":"stale"}`, 409)
	set(path, `{"limits":{},"revision":1,"reason":"restore inheritance"}`, 200)
	w = performJSON(t, s, "POST", "/v1/workspaces", token, map[string]string{"name": "blocked-again"})
	if w.Code != 402 {
		t.Fatal("restored global limit not enforced")
	}
	set(path, `{"limits":{"documents":-2},"revision":2,"reason":"invalid"}`, 422)
	set(path, `{"limits":{"documents":1.5},"revision":2,"reason":"invalid"}`, 400)
	set(path, `{"limits":{},"reason":"missing revision"}`, 422)
	set(path, `{"limits":{},"revision":2,"reason":" "}`, 422)
	set("/v1/ops/billing/users/00000000-0000-0000-0000-000000000000/quotas", `{"limits":{},"revision":0,"reason":"missing user"}`, 404)
	w = adminRequest(s, "PUT", path, []byte(`{"limits":{},"revision":2,"reason":"csrf"}`), []*http.Cookie{cookie}, false)
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	w = performJSON(t, s, "PUT", path, token, map[string]any{"limits": map[string]int{}, "revision": 2, "reason": "ordinary user"})
	if w.Code != 401 && w.Code != 403 {
		t.Fatalf("ordinary user accepted: %d", w.Code)
	}
}

func TestQuotaPoliciesRoleAndUserIsolation(t *testing.T) {
	for _, role := range []string{"support", "viewer"} {
		t.Run(role, func(t *testing.T) {
			s, _, cookie := passkeyServer(t, role)
			w := adminRequest(s, "GET", "/v1/ops/billing/quotas", nil, []*http.Cookie{cookie}, false)
			want := 200
			if role == "viewer" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("read role %s: %d", role, w.Code)
			}
			w = adminRequest(s, "PUT", "/v1/ops/billing/quotas", []byte(`{"limits":{"documents":-1},"revision":0,"reason":"deny"}`), []*http.Cookie{cookie}, true)
			if w.Code != 403 {
				t.Fatalf("role %s can write", role)
			}
		})
	}
	s, _, cookie := passkeyServer(t, "admin")
	_, alice := registerBillingUser(t, s, "alice-quota@example.com", "alice-quota")
	bobToken, _ := registerBillingUser(t, s, "bob-quota@example.com", "bob-quota")
	w := adminRequest(s, "PUT", "/v1/ops/billing/users/"+alice+"/quotas", []byte(`{"limits":{"documents":0},"revision":0,"reason":"alice only"}`), []*http.Cookie{cookie}, true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = performJSON(t, s, "GET", "/v1/billing/me", bobToken, nil)
	var result struct {
		EffectiveLimits struct {
			Documents int `json:"documents"`
		} `json:"effective_limits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.EffectiveLimits.Documents != 10 {
		t.Fatalf("Alice policy leaked to Bob: %s", w.Body.String())
	}
}
