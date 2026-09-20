package billing_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/persistence"
)

// Run only against disposable databases with all migrations applied.
func TestDurableQuotaPolicies(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql"} {
		t.Run(driver, func(t *testing.T) {
			env := "BILLING_QUOTA_POSTGRES_TEST_DSN"
			if driver == "mysql" {
				env = "BILLING_QUOTA_MYSQL_TEST_DSN"
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
			svc := billing.NewService(store, billing.DefaultPlans())
			v := 25
			p := billing.QuotaPolicy{Actor: "durable-quota-admin", Reason: "quota integration", Limits: billing.QuotaLimits{Documents: &v}}
			var saved atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := svc.SetQuotaPolicy(ctx, p, 0)
					if err == nil {
						saved.Add(1)
					} else if !errors.Is(err, billing.ErrConflict) {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if saved.Load() != 1 {
				t.Fatalf("first writers succeeded=%d", saved.Load())
			}
			reopened, err := persistence.Open(ctx, driver, dsn, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restarted := billing.NewService(reopened, billing.DefaultPlans())
			policies, err := restarted.QuotaPolicies(ctx, "00000000-0000-0000-0000-000000000001")
			if err != nil || policies.Global.Revision != 1 || *policies.Global.Limits.Documents != 25 {
				t.Fatalf("persisted=%+v %v", policies, err)
			}
			// Optimistic updates must also reject concurrent stale writers.
			v = 40
			saved.Store(0)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := restarted.SetQuotaPolicy(ctx, p, 1)
					if err == nil {
						saved.Add(1)
					} else if !errors.Is(err, billing.ErrConflict) {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if saved.Load() != 1 {
				t.Fatalf("revision writers succeeded=%d", saved.Load())
			}
			dbDriver, audit := "pgx", "eventing.audit_logs"
			if driver == "mysql" {
				dbDriver, audit = "mysql", "audit_logs"
			}
			db, err := sql.Open(dbDriver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows, err := db.QueryContext(ctx, "SELECT metadata FROM "+audit+" WHERE action='billing.quota.update' ORDER BY id")
			if err != nil {
				t.Fatal(err)
			}
			var entries []map[string]any
			for rows.Next() {
				var data []byte
				if err := rows.Scan(&data); err != nil {
					t.Fatal(err)
				}
				var entry map[string]any
				if err := json.Unmarshal(data, &entry); err != nil {
					t.Fatal(err)
				}
				entries = append(entries, entry)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[1]["actor"] != p.Actor || entries[1]["before"].(map[string]any)["documents"] != float64(25) || entries[1]["after"].(map[string]any)["documents"] != float64(40) {
				t.Fatalf("audit mismatch: %+v", entries)
			}
			check := "CHECK (metadata->>'reason' <> 'force audit failure')"
			if driver == "mysql" {
				check = "CHECK (JSON_UNQUOTE(JSON_EXTRACT(metadata,'$.reason')) <> 'force audit failure')"
			}
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+audit+" ADD CONSTRAINT quota_test_audit_failure "+check); err != nil {
				t.Fatal(err)
			}
			defer db.ExecContext(ctx, "ALTER TABLE "+audit+" DROP CONSTRAINT quota_test_audit_failure")
			p.Reason = "force audit failure"
			v = 99
			if _, err := restarted.SetQuotaPolicy(ctx, p, 2); err == nil {
				t.Fatal("audit failure was ignored")
			}
			policies, err = restarted.QuotaPolicies(ctx, "")
			if err != nil || policies.Global.Revision != 2 || *policies.Global.Limits.Documents != 40 {
				t.Fatalf("unaudited update was committed: %+v %v", policies, err)
			}
		})
	}
}
