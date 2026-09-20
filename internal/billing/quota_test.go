package billing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quotaInt(v int) *int { return &v }

func TestQuotaPrecedenceInheritanceAndEnforcement(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	s := NewService(store, DefaultPlans())
	store.PutSubscription(Subscription{UserID: "paid-user", PlanCode: "pro", Status: "active", CurrentPeriodStart: time.Now().Add(-time.Hour), CurrentPeriodEnd: time.Now().Add(time.Hour)})
	set := func(user string, limits QuotaLimits, revision int64) {
		t.Helper()
		if _, err := s.SetQuotaPolicy(ctx, QuotaPolicy{UserID: user, Limits: limits, Actor: "admin", Reason: "adjust quotas"}, revision); err != nil {
			t.Fatal(err)
		}
	}
	set("", QuotaLimits{Documents: quotaInt(0), SkillRunsPerMonth: quotaInt(40), ModelCostMicrosMonthly: quotaInt(0)}, 0)
	set("alice", QuotaLimits{Documents: quotaInt(-1), Workspaces: quotaInt(1)}, 0)
	for _, user := range []string{"bob", "future-user", "paid-user"} {
		for _, resource := range []string{ResourceDocuments, ResourceModelCost} {
			if err := s.Check(ctx, user, resource); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("global quota not enforced for %s/%s: %v", user, resource, err)
			}
		}
	}
	store.SetUsage("alice", 100, 1, nil)
	if err := s.Check(ctx, "alice", ResourceDocuments); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(ctx, "alice", ResourceWorkspaces); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal("individual zero remaining not enforced")
	}
	summary, err := s.Summary(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Plan.Limits.Documents != 10 || summary.EffectiveLimits.Documents != -1 || summary.EffectiveLimits.SkillRunsPerMonth != 40 || summary.Usage[0].Actual != 100 || summary.Usage[0].LimitSource != "user" || summary.Usage[1].LimitSource != "global" || summary.Usage[3].LimitSource != "plan" {
		t.Fatalf("wrong effective summary: %+v", summary)
	}
	set("", QuotaLimits{Documents: quotaInt(5)}, 1)
	if err := s.Check(ctx, "alice", ResourceDocuments); err != nil {
		t.Fatal("global update replaced user override")
	}
	set("alice", QuotaLimits{}, 1)
	if err := s.Check(ctx, "alice", ResourceDocuments); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatal("restored user inheritance did not take effect")
	}
	set("", QuotaLimits{}, 2)
	summary, _ = s.Summary(ctx, "alice")
	if summary.EffectiveLimits.Documents != 10 || summary.Usage[0].Actual != 100 || summary.Usage[0].LimitSource != "plan" {
		t.Fatal("restoring defaults changed usage or failed inheritance")
	}
	summary, _ = s.Summary(ctx, "paid-user")
	if summary.EffectiveLimits.Documents != 200 {
		t.Fatal("paid plan not restored")
	}
}

func TestQuotaValidationAndConcurrentRevision(t *testing.T) {
	ctx := context.Background()
	s := NewService(NewMemoryStore(), DefaultPlans())
	for _, p := range []QuotaPolicy{
		{Reason: "test"}, {Actor: "admin", Reason: " "},
		{Actor: "admin", Reason: "test", Limits: QuotaLimits{Documents: quotaInt(-2)}},
		{Actor: "admin", Reason: "test", Limits: QuotaLimits{AgentRunsPerMonth: quotaInt(9_007_199_254_740_992)}},
	} {
		if _, err := s.SetQuotaPolicy(ctx, p, 0); !errors.Is(err, ErrValidation) {
			t.Fatalf("accepted invalid policy: %+v %v", p, err)
		}
	}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.SetQuotaPolicy(ctx, QuotaPolicy{Actor: "admin", Reason: "race", Limits: QuotaLimits{Documents: quotaInt(20)}}, 0)
			if err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("concurrent saves=%d", success.Load())
	}
	p, _ := s.QuotaPolicies(ctx, "")
	*p.Global.Limits.Documents = 999
	p, _ = s.QuotaPolicies(ctx, "")
	if *p.Global.Limits.Documents != 20 {
		t.Fatal("read changed stored policy")
	}
}

type failedQuotaStore struct{ *MemoryStore }

func (failedQuotaStore) GetQuotaPolicies(context.Context, string) (QuotaPolicies, error) {
	return QuotaPolicies{}, errors.New("database unavailable")
}

func TestQuotaReadFailureDoesNotBypassLimit(t *testing.T) {
	s := NewService(failedQuotaStore{NewMemoryStore()}, DefaultPlans())
	if err := s.Check(context.Background(), "user", ResourceDocuments); err == nil {
		t.Fatal("quota store error failed open")
	}
}
