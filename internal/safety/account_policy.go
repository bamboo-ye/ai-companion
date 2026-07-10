package safety

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("safety policy not found")
	ErrValidation        = errors.New("safety policy validation failed")
	ErrCapabilityBlocked = errors.New("capability blocked by safety policy")
)

type UserPolicy struct {
	UserID             string    `json:"-"`
	MinorMode          bool      `json:"minor_mode"`
	GuardianEmail      string    `json:"guardian_email,omitempty"`
	RiskySkillsAllowed bool      `json:"risky_skills_allowed"`
	CreatedAt          time.Time `json:"created_at,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type UpdateUserPolicy struct {
	MinorMode          *bool   `json:"minor_mode"`
	GuardianEmail      *string `json:"guardian_email"`
	RiskySkillsAllowed *bool   `json:"risky_skills_allowed"`
}

type Store interface {
	GetUserPolicy(context.Context, string) (UserPolicy, error)
	UpsertUserPolicy(context.Context, UserPolicy) (UserPolicy, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) Get(ctx context.Context, userID string) (UserPolicy, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return UserPolicy{}, ErrValidation
	}
	policy, err := s.store.GetUserPolicy(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return defaultUserPolicy(userID, s.now().UTC()), nil
	}
	return policy, err
}

func (s *Service) Update(ctx context.Context, userID string, input UpdateUserPolicy) (UserPolicy, error) {
	policy, err := s.Get(ctx, userID)
	if err != nil {
		return UserPolicy{}, err
	}
	if input.MinorMode != nil {
		policy.MinorMode = *input.MinorMode
	}
	if input.GuardianEmail != nil {
		email := strings.ToLower(strings.TrimSpace(*input.GuardianEmail))
		if email != "" {
			parsed, parseErr := mail.ParseAddress(email)
			if parseErr != nil || parsed.Address != email {
				return UserPolicy{}, fmt.Errorf("%w: invalid guardian_email", ErrValidation)
			}
		}
		policy.GuardianEmail = email
	}
	if input.RiskySkillsAllowed != nil {
		policy.RiskySkillsAllowed = *input.RiskySkillsAllowed
	}
	if policy.MinorMode {
		if policy.GuardianEmail == "" {
			return UserPolicy{}, fmt.Errorf("%w: guardian_email is required when minor_mode is enabled", ErrValidation)
		}
		policy.RiskySkillsAllowed = false
	}
	now := s.now().UTC()
	if policy.CreatedAt.IsZero() {
		policy.CreatedAt = now
	}
	policy.UpdatedAt = now
	return s.store.UpsertUserPolicy(ctx, policy)
}

func (s *Service) CheckSkillRisk(ctx context.Context, userID, skillName, riskLevel string) error {
	if !isRisky(riskLevel) {
		return nil
	}
	policy, err := s.Get(ctx, userID)
	if err != nil {
		return err
	}
	if policy.MinorMode || !policy.RiskySkillsAllowed {
		return CapabilityError{SkillName: skillName, RiskLevel: riskLevel, MinorMode: policy.MinorMode}
	}
	return nil
}

type CapabilityError struct {
	SkillName string `json:"skill_name"`
	RiskLevel string `json:"risk_level"`
	MinorMode bool   `json:"minor_mode"`
}

func (e CapabilityError) Error() string {
	return fmt.Sprintf("%s: %s risk=%s", ErrCapabilityBlocked, e.SkillName, e.RiskLevel)
}

func (e CapabilityError) Unwrap() error { return ErrCapabilityBlocked }

func defaultUserPolicy(userID string, now time.Time) UserPolicy {
	return UserPolicy{UserID: userID, MinorMode: false, RiskySkillsAllowed: true, CreatedAt: now, UpdatedAt: now}
}

func isRisky(riskLevel string) bool {
	switch strings.ToLower(strings.TrimSpace(riskLevel)) {
	case "medium", "high":
		return true
	default:
		return false
	}
}
