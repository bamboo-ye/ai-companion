package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AdminStore interface {
	ListUserAccounts(context.Context, UserAccountFilter) ([]User, error)
	GetUserAccount(context.Context, string) (User, error)
	SetUserStatus(context.Context, UserModerationInput) (User, error)
	ListAuditLogs(context.Context, AuditLogFilter) ([]AuditLog, error)
}

type UserAccountFilter struct {
	Query  string
	Status string
	Limit  int
}

type UserModerationInput struct {
	UserID string
	Status string
	Actor  string
	Reason string
	Now    time.Time
}

type AuditLogFilter struct {
	ResourceType string
	ResourceID   string
	ActorType    string
	Action       string
	Limit        int
}

type AuditLog struct {
	ID           uint64          `json:"id"`
	ActorType    string          `json:"actor_type"`
	ActorID      string          `json:"actor_id,omitempty"`
	ActorLabel   string          `json:"actor_label,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id,omitempty"`
	TraceID      string          `json:"trace_id,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	OccurredAt   time.Time       `json:"occurred_at"`
}

type AdminService struct {
	store AdminStore
	now   func() time.Time
}

func NewAdminService(store AdminStore) *AdminService {
	return &AdminService{store: store, now: time.Now}
}

func (s *AdminService) ListUsers(ctx context.Context, filter UserAccountFilter) ([]User, error) {
	filter.Query = strings.TrimSpace(filter.Query)
	filter.Status = strings.ToLower(strings.TrimSpace(filter.Status))
	if filter.Status != "" && filter.Status != "active" && filter.Status != "disabled" && filter.Status != "deleted" {
		return nil, fmt.Errorf("%w: invalid status", ErrValidation)
	}
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	return s.store.ListUserAccounts(ctx, filter)
}

func (s *AdminService) GetUser(ctx context.Context, userID string) (User, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return User{}, ErrValidation
	}
	return s.store.GetUserAccount(ctx, userID)
}

func (s *AdminService) DisableUser(ctx context.Context, userID, actor, reason string) (User, error) {
	return s.setUserStatus(ctx, userID, "disabled", actor, reason)
}

func (s *AdminService) EnableUser(ctx context.Context, userID, actor, reason string) (User, error) {
	return s.setUserStatus(ctx, userID, "active", actor, reason)
}

func (s *AdminService) ListAuditLogs(ctx context.Context, filter AuditLogFilter) ([]AuditLog, error) {
	filter.ResourceType = strings.TrimSpace(filter.ResourceType)
	filter.ResourceID = strings.TrimSpace(filter.ResourceID)
	filter.ActorType = strings.ToLower(strings.TrimSpace(filter.ActorType))
	filter.Action = strings.TrimSpace(filter.Action)
	if filter.ActorType != "" && filter.ActorType != "user" && filter.ActorType != "operator" && filter.ActorType != "system" {
		return nil, ErrValidation
	}
	if len(filter.ResourceType) > 64 || len(filter.Action) > 128 {
		return nil, ErrValidation
	}
	if filter.Limit <= 0 || filter.Limit > 5000 {
		filter.Limit = 100
	}
	return s.store.ListAuditLogs(ctx, filter)
}

func (s *AdminService) setUserStatus(ctx context.Context, userID, status, actor, reason string) (User, error) {
	userID, actor, reason = strings.TrimSpace(userID), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if userID == "" || actor == "" || reason == "" || len(reason) > 512 {
		return User{}, fmt.Errorf("%w: user_id, actor and reason are required", ErrValidation)
	}
	if status != "active" && status != "disabled" {
		return User{}, ErrValidation
	}
	user, err := s.store.SetUserStatus(ctx, UserModerationInput{UserID: userID, Status: status, Actor: actor, Reason: reason, Now: s.now().UTC()})
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrNotFound
	}
	return user, err
}
