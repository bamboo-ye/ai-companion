package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrNotFound      = errors.New("billing resource not found")
	ErrValidation    = errors.New("billing validation failed")
	ErrQuotaExceeded = errors.New("billing quota exceeded")
)

const (
	ResourceDocuments  = "documents"
	ResourceSkillRuns  = "skill_runs"
	ResourceWorkspaces = "workspaces"
	ResourceAgentRuns  = "agent_runs"
	ResourceModelCost  = "model_cost_micros"
)

type Limits struct {
	Documents              int `json:"documents"`
	SkillRunsPerMonth      int `json:"skill_runs_per_month"`
	Workspaces             int `json:"workspaces"`
	AgentRunsPerMonth      int `json:"agent_runs_per_month"`
	ModelCostMicrosMonthly int `json:"model_cost_micros_monthly"`
}

type Plan struct {
	Code        string    `json:"code"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	Limits      Limits    `json:"limits"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

type Subscription struct {
	ID                 string    `json:"id,omitempty"`
	UserID             string    `json:"-"`
	PlanCode           string    `json:"plan_code"`
	Status             string    `json:"status"`
	CurrentPeriodStart time.Time `json:"current_period_start"`
	CurrentPeriodEnd   time.Time `json:"current_period_end"`
	Provider           string    `json:"provider,omitempty"`
	ProviderRef        string    `json:"provider_ref,omitempty"`
	CreatedAt          time.Time `json:"created_at,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type UsageItem struct {
	Resource    string     `json:"resource"`
	LimitSource string     `json:"limit_source"`
	Actual      int        `json:"actual"`
	Adjustment  int        `json:"adjustment"`
	Used        int        `json:"used"`
	Limit       int        `json:"limit"`
	Remaining   int        `json:"remaining"`
	PeriodStart *time.Time `json:"period_start,omitempty"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
}

type UsageAdjustment struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Resource    string     `json:"resource"`
	Delta       int        `json:"delta"`
	Reason      string     `json:"reason"`
	Actor       string     `json:"actor"`
	PeriodStart *time.Time `json:"period_start,omitempty"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type CreateUsageAdjustmentInput struct {
	UserID   string
	Resource string
	Delta    int
	Reason   string
	Actor    string
	Now      time.Time
}

type Summary struct {
	Plan            Plan         `json:"plan"`
	EffectiveLimits Limits       `json:"effective_limits"`
	Subscription    Subscription `json:"subscription"`
	Usage           []UsageItem  `json:"usage"`
}

type QuotaError struct {
	Resource string `json:"resource"`
	PlanCode string `json:"plan_code"`
	Used     int    `json:"used"`
	Limit    int    `json:"limit"`
}

func (e QuotaError) Error() string {
	return fmt.Sprintf("%s: %s quota exceeded for plan %s (%d/%d)", ErrQuotaExceeded, e.Resource, e.PlanCode, e.Used, e.Limit)
}

func (e QuotaError) Unwrap() error { return ErrQuotaExceeded }

type Store interface {
	GetActiveSubscription(context.Context, string, time.Time) (Subscription, error)
	CountActiveDocuments(context.Context, string) (int, error)
	CountSkillRunsInPeriod(context.Context, string, time.Time, time.Time) (int, error)
	CountOwnedWorkspaces(context.Context, string) (int, error)
}

// UsageStore is an optional extension implemented by durable stores. Keeping
// it separate preserves compatibility with older MySQL deployments while the
// PostgreSQL control plane supplies the complete unified ledger.
type UsageStore interface {
	CountAgentRunsInPeriod(context.Context, string, time.Time, time.Time) (int, error)
	SumModelCostMicrosInPeriod(context.Context, string, time.Time, time.Time) (int, error)
	SumUsageAdjustments(context.Context, string, string, *time.Time, *time.Time) (int, error)
	CreateUsageAdjustment(context.Context, UsageAdjustment) (UsageAdjustment, error)
	ListUsageAdjustments(context.Context, string, int) ([]UsageAdjustment, error)
}

type Service struct {
	store         Store
	mu            sync.RWMutex
	catalog       map[string]Plan
	now           func() time.Time
	quotaDisabled bool
}

// ReplaceCatalog atomically applies a validated, published plan catalog.
// Existing requests continue to see either the complete old or new snapshot.
func (s *Service) ReplaceCatalog(plans []Plan) error {
	catalog := normalizedCatalog(plans)
	if free, ok := catalog["free"]; !ok || free.Status != "active" {
		return fmt.Errorf("%w: published catalog requires an active free plan", ErrValidation)
	}
	s.mu.Lock()
	s.catalog = catalog
	s.mu.Unlock()
	return nil
}

// SetQuotaDisabled temporarily treats every billing resource as unlimited.
// Configuration prevents this development-only escape hatch in production.
func (s *Service) SetQuotaDisabled(disabled bool) {
	s.quotaDisabled = disabled
}

func NewService(store Store, plans []Plan) *Service {
	catalog := normalizedCatalog(plans)
	if _, exists := catalog["free"]; !exists {
		catalog = normalizedCatalog(DefaultPlans())
	}
	return &Service{store: store, catalog: catalog, now: time.Now}
}

func DefaultPlans() []Plan {
	return []Plan{
		{Code: "free", DisplayName: "Free", Status: "active", Limits: Limits{Documents: 10, SkillRunsPerMonth: 20, Workspaces: 3, AgentRunsPerMonth: -1, ModelCostMicrosMonthly: -1}},
		{Code: "pro", DisplayName: "Pro", Status: "active", Limits: Limits{Documents: 200, SkillRunsPerMonth: 1000, Workspaces: 20, AgentRunsPerMonth: -1, ModelCostMicrosMonthly: -1}},
		{Code: "team", DisplayName: "Team", Status: "active", Limits: Limits{Documents: 1000, SkillRunsPerMonth: 5000, Workspaces: 100, AgentRunsPerMonth: -1, ModelCostMicrosMonthly: -1}},
	}
}

func (s *Service) Summary(ctx context.Context, userID string) (Summary, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Summary{}, ErrValidation
	}
	now := s.now().UTC()
	subscription, plan := s.subscriptionAndPlan(ctx, userID, now)
	limits := plan.Limits
	sources := [5]string{"plan", "plan", "plan", "plan", "plan"}
	if store, ok := s.store.(QuotaStore); ok {
		policies, err := store.GetQuotaPolicies(ctx, userID)
		if err != nil {
			return Summary{}, err
		}
		limits = policies.User.Limits.apply(policies.Global.Limits.apply(limits))
		for i, value := range policies.Global.Limits.values() {
			if value != nil {
				sources[i] = "global"
			}
		}
		for i, value := range policies.User.Limits.values() {
			if value != nil {
				sources[i] = "user"
			}
		}
	}
	if s.quotaDisabled {
		limits = Limits{Documents: -1, SkillRunsPerMonth: -1, Workspaces: -1, AgentRunsPerMonth: -1, ModelCostMicrosMonthly: -1}
		for i := range sources {
			sources[i] = "development"
		}
	}
	periodStart, periodEnd := monthBounds(now)
	if !subscription.CurrentPeriodStart.IsZero() && !subscription.CurrentPeriodEnd.IsZero() {
		periodStart, periodEnd = subscription.CurrentPeriodStart, subscription.CurrentPeriodEnd
	}
	documents, err := s.store.CountActiveDocuments(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	skillRuns, err := s.store.CountSkillRunsInPeriod(ctx, userID, periodStart, periodEnd)
	if err != nil {
		return Summary{}, err
	}
	workspaces, err := s.store.CountOwnedWorkspaces(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	agentRuns, modelCost := 0, 0
	usageStore, durableUsage := s.store.(UsageStore)
	if durableUsage {
		agentRuns, err = usageStore.CountAgentRunsInPeriod(ctx, userID, periodStart, periodEnd)
		if err != nil {
			return Summary{}, err
		}
		modelCost, err = usageStore.SumModelCostMicrosInPeriod(ctx, userID, periodStart, periodEnd)
		if err != nil {
			return Summary{}, err
		}
	}
	items := []UsageItem{
		usage(ResourceDocuments, documents, 0, limits.Documents, nil, nil),
		usage(ResourceSkillRuns, skillRuns, 0, limits.SkillRunsPerMonth, &periodStart, &periodEnd),
		usage(ResourceWorkspaces, workspaces, 0, limits.Workspaces, nil, nil),
		usage(ResourceAgentRuns, agentRuns, 0, limits.AgentRunsPerMonth, &periodStart, &periodEnd),
		usage(ResourceModelCost, modelCost, 0, limits.ModelCostMicrosMonthly, &periodStart, &periodEnd),
	}
	if durableUsage {
		for index := range items {
			adjustment, adjustmentErr := usageStore.SumUsageAdjustments(ctx, userID, items[index].Resource, items[index].PeriodStart, items[index].PeriodEnd)
			if adjustmentErr != nil {
				return Summary{}, adjustmentErr
			}
			items[index] = usage(items[index].Resource, items[index].Actual, adjustment, items[index].Limit, items[index].PeriodStart, items[index].PeriodEnd)
		}
	}
	for i := range items {
		items[i].LimitSource = sources[i]
	}
	return Summary{Plan: plan, EffectiveLimits: limits, Subscription: subscription, Usage: items}, nil
}

func (s *Service) AdjustUsage(ctx context.Context, input CreateUsageAdjustmentInput) (UsageAdjustment, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.Resource = strings.ToLower(strings.TrimSpace(input.Resource))
	input.Reason = strings.TrimSpace(input.Reason)
	input.Actor = strings.TrimSpace(input.Actor)
	if input.UserID == "" || input.Actor == "" || input.Delta == 0 || input.Reason == "" || len(input.Reason) > 512 || !validResource(input.Resource) {
		return UsageAdjustment{}, ErrValidation
	}
	store, ok := s.store.(UsageStore)
	if !ok {
		return UsageAdjustment{}, ErrNotFound
	}
	now := input.Now.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	var periodStart, periodEnd *time.Time
	if input.Resource == ResourceSkillRuns || input.Resource == ResourceAgentRuns || input.Resource == ResourceModelCost {
		subscription, _ := s.subscriptionAndPlan(ctx, input.UserID, now)
		start, end := subscription.CurrentPeriodStart, subscription.CurrentPeriodEnd
		periodStart, periodEnd = &start, &end
	}
	adjustmentID, err := id.New()
	if err != nil {
		return UsageAdjustment{}, err
	}
	return store.CreateUsageAdjustment(ctx, UsageAdjustment{
		ID: adjustmentID, UserID: input.UserID, Resource: input.Resource, Delta: input.Delta,
		Reason: input.Reason, Actor: input.Actor, PeriodStart: periodStart,
		PeriodEnd: periodEnd, CreatedAt: now,
	})
}

func (s *Service) UsageAdjustments(ctx context.Context, userID string, limit int) ([]UsageAdjustment, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	store, ok := s.store.(UsageStore)
	if !ok {
		return nil, ErrNotFound
	}
	return store.ListUsageAdjustments(ctx, userID, limit)
}

func (s *Service) Check(ctx context.Context, userID, resource string) error {
	summary, err := s.Summary(ctx, userID)
	if err != nil {
		return err
	}
	resource = strings.ToLower(strings.TrimSpace(resource))
	for _, item := range summary.Usage {
		if item.Resource != resource {
			continue
		}
		if item.Limit >= 0 && item.Used >= item.Limit {
			return QuotaError{Resource: resource, PlanCode: summary.Plan.Code, Used: item.Used, Limit: item.Limit}
		}
		return nil
	}
	return fmt.Errorf("%w: unknown resource %s", ErrValidation, resource)
}

func (s *Service) subscriptionAndPlan(ctx context.Context, userID string, now time.Time) (Subscription, Plan) {
	subscription, err := s.store.GetActiveSubscription(ctx, userID, now)
	if err != nil {
		start, end := monthBounds(now)
		subscription = Subscription{UserID: userID, PlanCode: "free", Status: "implicit", CurrentPeriodStart: start, CurrentPeriodEnd: end}
	}
	s.mu.RLock()
	plan, ok := s.catalog[strings.ToLower(subscription.PlanCode)]
	if !ok || plan.Status != "active" {
		plan = s.catalog["free"]
		subscription.PlanCode = plan.Code
	}
	s.mu.RUnlock()
	return subscription, plan
}

func normalizedCatalog(plans []Plan) map[string]Plan {
	catalog := map[string]Plan{}
	for _, plan := range plans {
		plan.Code = strings.ToLower(strings.TrimSpace(plan.Code))
		if plan.Code == "" {
			continue
		}
		if plan.Status == "" {
			plan.Status = "active"
		}
		catalog[plan.Code] = plan
	}
	return catalog
}

func usage(resource string, actual, adjustment, limit int, start, end *time.Time) UsageItem {
	used := actual + adjustment
	if used < 0 {
		used = 0
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	if limit < 0 {
		remaining = -1
	}
	return UsageItem{Resource: resource, Actual: actual, Adjustment: adjustment, Used: used, Limit: limit, Remaining: remaining, PeriodStart: start, PeriodEnd: end}
}

func validResource(resource string) bool {
	switch resource {
	case ResourceDocuments, ResourceSkillRuns, ResourceWorkspaces, ResourceAgentRuns, ResourceModelCost:
		return true
	default:
		return false
	}
}

func monthBounds(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}
