package billing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrConflict = errors.New("billing quota policy changed")

// Null inherits the next layer. Zero blocks new usage; -1 is unlimited.
type QuotaLimits struct {
	Documents              *int `json:"documents"`
	SkillRunsPerMonth      *int `json:"skill_runs_per_month"`
	Workspaces             *int `json:"workspaces"`
	AgentRunsPerMonth      *int `json:"agent_runs_per_month"`
	ModelCostMicrosMonthly *int `json:"model_cost_micros_monthly"`
}

type QuotaPolicy struct {
	UserID    string      `json:"user_id,omitempty"`
	Limits    QuotaLimits `json:"limits"`
	Revision  int64       `json:"revision"`
	Actor     string      `json:"actor,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	UpdatedAt *time.Time  `json:"updated_at,omitempty"`
}

type QuotaPolicies struct {
	Global QuotaPolicy `json:"global"`
	User   QuotaPolicy `json:"user"`
}

type QuotaStore interface {
	GetQuotaPolicies(context.Context, string) (QuotaPolicies, error)
	SaveQuotaPolicy(context.Context, QuotaPolicy, int64) (QuotaPolicy, error)
}

func (s *Service) QuotaPolicies(ctx context.Context, userID string) (QuotaPolicies, error) {
	store, ok := s.store.(QuotaStore)
	if !ok {
		return QuotaPolicies{}, ErrNotFound
	}
	return store.GetQuotaPolicies(ctx, userID)
}

// SetQuotaPolicy replaces one complete layer using an optimistic revision.
// Empty limits restore inheritance without deleting the revision history.
func (s *Service) SetQuotaPolicy(ctx context.Context, policy QuotaPolicy, expected int64) (QuotaPolicy, error) {
	policy.UserID = strings.TrimSpace(policy.UserID)
	policy.Actor = strings.TrimSpace(policy.Actor)
	policy.Reason = strings.TrimSpace(policy.Reason)
	if expected < 0 || expected >= 9_007_199_254_740_991 || len(policy.UserID) > 64 || policy.Actor == "" || len(policy.Actor) > 128 || policy.Reason == "" || len(policy.Reason) > 512 {
		return QuotaPolicy{}, ErrValidation
	}
	for _, value := range policy.Limits.values() {
		if value != nil && (*value < -1 || *value > 9_007_199_254_740_991) {
			return QuotaPolicy{}, fmt.Errorf("%w: 额度须为 -1 或不小于 0 的安全整数", ErrValidation)
		}
	}
	store, ok := s.store.(QuotaStore)
	if !ok {
		return QuotaPolicy{}, ErrNotFound
	}
	now := s.now().UTC()
	policy.UpdatedAt = &now
	policy.Revision = expected + 1
	return store.SaveQuotaPolicy(ctx, policy, expected)
}

func (limits QuotaLimits) values() []*int {
	return []*int{limits.Documents, limits.SkillRunsPerMonth, limits.Workspaces, limits.AgentRunsPerMonth, limits.ModelCostMicrosMonthly}
}

func (limits QuotaLimits) apply(base Limits) Limits {
	targets := []*int{&base.Documents, &base.SkillRunsPerMonth, &base.Workspaces, &base.AgentRunsPerMonth, &base.ModelCostMicrosMonthly}
	for i, value := range limits.values() {
		if value != nil {
			*targets[i] = *value
		}
	}
	return base
}

func quotaKey(userID string) string {
	if userID == "" {
		return "global"
	}
	return "user:" + userID
}
