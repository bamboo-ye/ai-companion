package adminpasskey

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/adminpasskey/passkeytest"
	"github.com/windcry1/ai-companion/internal/opsauth"
)

func testService(t *testing.T) (*Service, *opsauth.MemoryStore) {
	t.Helper()
	a, err := opsauth.BootstrapAccount("alice", "Alice", "admin", "unused-token", "", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	accounts := opsauth.NewMemoryStore(a)
	s, err := New(NewMemoryStore(), accounts, "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return s, accounts
}
func enroll(t *testing.T, s *Service) (*passkeytest.Authenticator, string) {
	t.Helper()
	ctx := context.Background()
	token, err := s.IssueInvite(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	opts, challenge, err := s.BeginRegistration(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	a := passkeytest.New(t)
	payload := a.Registration(t, opts, s.Origin, s.WebAuthn.Config.RPID, true)
	session, err := s.FinishRegistration(ctx, challenge, httptest.NewRequest("POST", "/", bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	return a, session
}
func TestPasskeyRegistrationLoginAndRevocation(t *testing.T) {
	s, accounts := testService(t)
	ctx := context.Background()
	device, token := enroll(t, s)
	a, session, err := s.Authenticate(ctx, token)
	if err != nil || a.ID != "alice" || !s.Fresh(session) {
		t.Fatalf("session: %v", err)
	}
	opts, challenge, err := s.BeginLogin(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	payload := device.Assertion(t, opts, s.Origin, s.WebAuthn.Config.RPID, true)
	newToken, err := s.FinishLogin(ctx, challenge, httptest.NewRequest("POST", "/", bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishLogin(ctx, challenge, httptest.NewRequest("POST", "/", bytes.NewReader(payload))); !errors.Is(err, ErrDenied) {
		t.Fatalf("replay accepted: %v", err)
	}
	if err = s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Authenticate(ctx, token); !errors.Is(err, ErrDenied) {
		t.Fatal("logged-out session accepted")
	}
	if _, _, err = s.Authenticate(ctx, newToken); err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.SetOperatorStatus(ctx, "alice", "disabled", opsauth.AuditInput{Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Authenticate(ctx, newToken); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled account accepted")
	}
	_, _ = accounts.SetOperatorStatus(ctx, "alice", "active", opsauth.AuditInput{Now: time.Now()})
	if _, _, err = s.Authenticate(ctx, newToken); !errors.Is(err, ErrDenied) {
		t.Fatal("old session revived on re-enable")
	}
}
func TestRegistrationRequiresInvitationOriginAndUV(t *testing.T) {
	for _, scenario := range []string{"unknown_invite", "reused_invite", "expired_invite", "wrong_origin", "wrong_rp", "missing_uv", "changed_account"} {
		t.Run(scenario, func(t *testing.T) {
			s, accounts := testService(t)
			ctx := context.Background()
			invite, _ := s.IssueInvite(ctx, "alice")
			if scenario == "unknown_invite" {
				invite = "unknown"
			}
			if scenario == "expired_invite" {
				now := s.now()
				s.now = func() time.Time { return now.Add(InviteTTL + time.Second) }
			}
			opts, secret, err := s.BeginRegistration(ctx, invite)
			if scenario == "unknown_invite" || scenario == "expired_invite" {
				if !errors.Is(err, ErrDenied) {
					t.Fatalf("invite accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "reused_invite" {
				if _, _, err = s.BeginRegistration(ctx, invite); !errors.Is(err, ErrDenied) {
					t.Fatal("reused invite accepted")
				}
				return
			}
			origin, rp, uv := s.Origin, s.WebAuthn.Config.RPID, true
			if scenario == "wrong_origin" {
				origin = "https://evil.example.com"
			}
			if scenario == "wrong_rp" {
				rp = "example.com"
			}
			if scenario == "missing_uv" {
				uv = false
			}
			if scenario == "changed_account" {
				_, _ = accounts.ResetOperatorToken(ctx, "alice", "different-hash", opsauth.AuditInput{})
			}
			payload := passkeytest.New(t).Registration(t, opts, origin, rp, uv)
			if _, err = s.FinishRegistration(ctx, secret, httptest.NewRequest("POST", "/", bytes.NewReader(payload))); !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid registration accepted: %v", err)
			}
		})
	}
}
func TestLoginRejectsInvalidAssertions(t *testing.T) {
	for _, scenario := range []string{"origin", "rp", "uv", "signature", "handle", "owner", "challenge", "expired", "counter"} {
		t.Run(scenario, func(t *testing.T) {
			s, _ := testService(t)
			ctx := context.Background()
			device, _ := enroll(t, s)
			expected := ""
			if scenario == "owner" {
				expected = "other"
			}
			opts, secret, _ := s.BeginLogin(ctx, expected)
			origin, rp, uv := s.Origin, s.WebAuthn.Config.RPID, true
			if scenario == "origin" {
				origin = "https://evil.example.com"
			}
			if scenario == "rp" {
				rp = "example.com"
			}
			if scenario == "uv" {
				uv = false
			}
			if scenario == "handle" {
				device.Handle = "YWJj"
			}
			payload := device.Assertion(t, opts, origin, rp, uv)
			if scenario == "signature" {
				var body map[string]any
				_ = json.Unmarshal(payload, &body)
				body["response"].(map[string]any)["signature"] = "YWJj"
				payload = passkeytest.JSON(t, body)
			}
			if scenario == "challenge" {
				_, secret, _ = s.BeginLogin(ctx, "")
			}
			if scenario == "expired" {
				now := s.now()
				s.now = func() time.Time { return now.Add(CeremonyTTL + time.Second) }
			}
			if scenario == "counter" {
				if _, err := s.FinishLogin(ctx, secret, httptest.NewRequest("POST", "/", bytes.NewReader(payload))); err != nil {
					t.Fatal(err)
				}
				opts, secret, _ = s.BeginLogin(ctx, "")
				device.Counter = 0
				payload = device.Assertion(t, opts, origin, rp, uv)
			}
			if _, err := s.FinishLogin(ctx, secret, httptest.NewRequest("POST", "/", bytes.NewReader(payload))); !errors.Is(err, ErrDenied) {
				t.Fatalf("assertion accepted: %v", err)
			}
		})
	}
}
func TestSessionTimeoutsAndConcurrentTouch(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	_, token := enroll(t, s)
	now := s.now()
	s.now = func() time.Time { return now.Add(2 * time.Minute) }
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Authenticate(ctx, token); err != nil {
				t.Errorf("concurrent session touch: %v", err)
			}
		}()
	}
	wg.Wait()
	s.now = func() time.Time { return now.Add(7 * time.Minute) }
	_, session, err := s.Authenticate(ctx, token)
	if err != nil || s.Fresh(session) {
		t.Fatal("freshness not enforced")
	}
	s.now = func() time.Time { return now.Add(38 * time.Minute) }
	if _, _, err = s.Authenticate(ctx, token); !errors.Is(err, ErrDenied) {
		t.Fatal("idle session accepted")
	}
	s.now = func() time.Time { return now.Add(SessionTTL + time.Second) }
	if _, _, err = s.Authenticate(ctx, token); !errors.Is(err, ErrDenied) {
		t.Fatal("expired session accepted")
	}
}
func TestSingleUseStateIsAtomic(t *testing.T) {
	s := NewMemoryStore()
	ctx := context.Background()
	_ = s.Insert(ctx, Record{Key: "once", Expires: permanent()})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Take(ctx, "once"); err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("consumed %d times", successes.Load())
	}
}
func TestCeremonyRateLimit(t *testing.T) {
	s, _ := testService(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	for range 120 {
		if err := s.AllowCeremony(ctx, "proxy"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AllowCeremony(ctx, "proxy"); !errors.Is(err, ErrRateLimit) {
		t.Fatalf("limit missing: %v", err)
	}
	s.now = func() time.Time { return now.Add(time.Minute) }
	if err := s.AllowCeremony(ctx, "proxy"); err != nil {
		t.Fatal(err)
	}
}
func TestOriginConfiguration(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://example.com/admin", "https://user@example.com", "https://example.com?x=1", "*"} {
		if _, err := New(NewMemoryStore(), opsauth.NewMemoryStore(), origin); err == nil {
			t.Fatalf("invalid origin accepted: %s", origin)
		}
	}
}
