package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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
)

type Limits struct {
	Documents         int `json:"documents"`
	SkillRunsPerMonth int `json:"skill_runs_per_month"`
	Workspaces        int `json:"workspaces"`
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
	Used        int        `json:"used"`
	Limit       int        `json:"limit"`
	Remaining   int        `json:"remaining"`
	PeriodStart *time.Time `json:"period_start,omitempty"`
	PeriodEnd   *time.Time `json:"period_end,omitempty"`
}

type Summary struct {
	Plan         Plan         `json:"plan"`
	Subscription Subscription `json:"subscription"`
	Usage        []UsageItem  `json:"usage"`
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

type Service struct {
	store   Store
	catalog map[string]Plan
	now     func() time.Time
}

func NewService(store Store, plans []Plan) *Service {
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
	if _, exists := catalog["free"]; !exists {
		for _, plan := range DefaultPlans() {
			catalog[plan.Code] = plan
		}
	}
	return &Service{store: store, catalog: catalog, now: time.Now}
}

func DefaultPlans() []Plan {
	return []Plan{
		{Code: "free", DisplayName: "Free", Status: "active", Limits: Limits{Documents: 10, SkillRunsPerMonth: 20, Workspaces: 3}},
		{Code: "pro", DisplayName: "Pro", Status: "active", Limits: Limits{Documents: 200, SkillRunsPerMonth: 1000, Workspaces: 20}},
		{Code: "team", DisplayName: "Team", Status: "active", Limits: Limits{Documents: 1000, SkillRunsPerMonth: 5000, Workspaces: 100}},
	}
}

func (s *Service) Summary(ctx context.Context, userID string) (Summary, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Summary{}, ErrValidation
	}
	now := s.now().UTC()
	subscription, plan := s.subscriptionAndPlan(ctx, userID, now)
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
	return Summary{Plan: plan, Subscription: subscription, Usage: []UsageItem{
		usage(ResourceDocuments, documents, plan.Limits.Documents, nil, nil),
		usage(ResourceSkillRuns, skillRuns, plan.Limits.SkillRunsPerMonth, &periodStart, &periodEnd),
		usage(ResourceWorkspaces, workspaces, plan.Limits.Workspaces, nil, nil),
	}}, nil
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
	plan, ok := s.catalog[strings.ToLower(subscription.PlanCode)]
	if !ok || plan.Status != "active" {
		plan = s.catalog["free"]
		subscription.PlanCode = plan.Code
	}
	return subscription, plan
}

func usage(resource string, used, limit int, start, end *time.Time) UsageItem {
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}
	if limit < 0 {
		remaining = -1
	}
	return UsageItem{Resource: resource, Used: used, Limit: limit, Remaining: remaining, PeriodStart: start, PeriodEnd: end}
}

func monthBounds(now time.Time) (time.Time, time.Time) {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}
