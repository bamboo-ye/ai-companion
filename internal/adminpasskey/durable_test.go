package adminpasskey_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/adminpasskey"
	"github.com/windcry1/ai-companion/internal/adminpasskey/passkeytest"
	"github.com/windcry1/ai-companion/internal/opsauth"
	"github.com/windcry1/ai-companion/internal/persistence"
)

// These DSNs must point to disposable databases with all migrations applied.
func TestDurablePasskeys(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			env := "ADMIN_PASSKEY_POSTGRES_TEST_DSN"
			if driver == "mysql" {
				env = "ADMIN_PASSKEY_MYSQL_TEST_DSN"
			}
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip(env + " not set")
			}
			ctx := context.Background()
			store, err := persistence.Open(ctx, driver, dsn, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			origin := "https://admin.example.com"
			s, err := adminpasskey.New(store.AdminPasskeyStore(), store, origin)
			if err != nil {
				t.Fatal(err)
			}
			accounts := opsauth.NewService(store)
			owner := fmt.Sprintf("passkey-%d", time.Now().UnixNano())
			for _, id := range []string{owner, owner + "-pending"} {
				if _, err = accounts.Create(ctx, opsauth.CreateInput{ID: id, DisplayName: id, Role: "admin", Actor: "durable-test", Reason: "passkey integration"}); err != nil {
					t.Fatal(err)
				}
			}
			invite, err := s.IssueInvite(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			opts, challenge, err := s.BeginRegistration(ctx, invite)
			if err != nil {
				t.Fatal(err)
			}
			device := passkeytest.New(t)
			payload := device.Registration(t, opts, origin, "admin.example.com", true)
			token, err := s.FinishRegistration(ctx, challenge, httptest.NewRequest("POST", "/", bytes.NewReader(payload)))
			if err != nil {
				t.Fatal(err)
			}
			account, _, err := s.Authenticate(ctx, token)
			if err != nil || !account.PasskeyEnabled {
				t.Fatalf("durable session/account: %+v %v", account, err)
			}
			if _, err = accounts.Disable(ctx, owner, "durable-test", "last passkey admin"); !errors.Is(err, opsauth.ErrAdminLockout) {
				t.Fatalf("last passkey admin lockout allowed: %v", err)
			}
			// Also exercise the transactional store guard without the service preflight.
			if _, err = store.SetOperatorStatus(ctx, owner, "disabled", opsauth.AuditInput{Actor: "durable-test", Action: "test", Reason: "last admin", Now: time.Now()}); !errors.Is(err, opsauth.ErrAdminLockout) {
				t.Fatalf("store guard: %v", err)
			}
			reopened, err := persistence.Open(ctx, driver, dsn, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restarted, err := adminpasskey.New(reopened.AdminPasskeyStore(), reopened, origin)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = restarted.Authenticate(ctx, token); err != nil {
				t.Fatalf("restart lost session: %v", err)
			}
			opts, challenge, err = restarted.BeginLogin(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			payload = device.Assertion(t, opts, origin, "admin.example.com", true)
			if _, err = restarted.FinishLogin(ctx, challenge, httptest.NewRequest("POST", "/", bytes.NewReader(payload))); err != nil {
				t.Fatal(err)
			}
			if _, err = accounts.ResetToken(ctx, owner, "durable-test", "revoke sessions"); err != nil {
				t.Fatal(err)
			}
			if _, _, err = restarted.Authenticate(ctx, token); err == nil {
				t.Fatal("rotated account retained session")
			}
			credentials, err := s.Store.List(ctx, "credential", owner)
			if err != nil || len(credentials) != 1 {
				t.Fatalf("stored credentials: %v", err)
			}
			stale := credentials[0]
			stale.Key = "stale-enrollment-" + owner
			if err = s.Store.Insert(ctx, stale); !errors.Is(err, adminpasskey.ErrDenied) {
				t.Fatalf("enrollment authorized before revocation accepted: %v", err)
			}
			recovery := s.Store.(adminpasskey.RecoveryStore)
			if err = recovery.RecoverAccount(ctx, owner, "lost-device recovery test"); err != nil {
				t.Fatal(err)
			}
			credentials, err = s.Store.List(ctx, "credential", owner)
			if err != nil || len(credentials) != 0 {
				t.Fatal("recovery retained credentials")
			}
			if err = s.Store.Insert(ctx, stale); !errors.Is(err, adminpasskey.ErrDenied) {
				t.Fatalf("recovery race accepted stale enrollment: %v", err)
			}
			key := "single-use-" + owner
			state := store.AdminPasskeyStore()
			if err = state.Insert(ctx, adminpasskey.Record{Key: key, Owner: owner, Kind: "test", Data: []byte(`{}`), Expires: time.Now().Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			var successes atomic.Int32
			var wg sync.WaitGroup
			for range 20 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, takeErr := state.Take(ctx, key); takeErr == nil {
						successes.Add(1)
					}
				}()
			}
			wg.Wait()
			if successes.Load() != 1 {
				t.Fatalf("challenge consumed %d times", successes.Load())
			}
		})
	}
}
