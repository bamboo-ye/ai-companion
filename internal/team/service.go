package team

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrConflict     = errors.New("resource already exists")
	ErrForbidden    = errors.New("forbidden")
	ErrNotFound     = errors.New("resource not found")
	ErrValidation   = errors.New("validation failed")
	ErrUnauthorized = errors.New("unauthorized")
)

type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	OwnerID   string    `json:"owner_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Member struct {
	WorkspaceID string    `json:"workspace_id"`
	UserID      string    `json:"user_id"`
	Email       string    `json:"email,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	Role        string    `json:"role"`
	Status      string    `json:"status"`
	JoinedAt    time.Time `json:"joined_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Invitation struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspace_id"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	InvitedBy   string     `json:"invited_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	AcceptedAt  *time.Time `json:"accepted_at,omitempty"`
}

type Store interface {
	CreateWorkspace(context.Context, Workspace, Member) error
	ListWorkspacesForUser(context.Context, string) ([]Workspace, error)
	GetWorkspace(context.Context, string) (Workspace, error)
	GetMember(context.Context, string, string) (Member, error)
	ListMembers(context.Context, string) ([]Member, error)
	CreateInvitation(context.Context, Invitation) error
	ListInvitations(context.Context, string) ([]Invitation, error)
	GetInvitation(context.Context, string) (Invitation, error)
	AcceptInvitation(context.Context, string, identity.User, time.Time) (Member, Invitation, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) CreateWorkspace(ctx context.Context, user identity.User, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if len([]rune(name)) < 2 || len([]rune(name)) > 80 {
		return Workspace{}, fmt.Errorf("%w: workspace name must contain 2-80 characters", ErrValidation)
	}
	workspaceID, err := id.New()
	if err != nil {
		return Workspace{}, err
	}
	now := s.now().UTC()
	workspace := Workspace{ID: workspaceID, Name: name, OwnerID: user.ID, Status: "active", CreatedAt: now, UpdatedAt: now}
	member := Member{WorkspaceID: workspaceID, UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Role: "owner", Status: "active", JoinedAt: now, UpdatedAt: now}
	if err = s.store.CreateWorkspace(ctx, workspace, member); err != nil {
		return Workspace{}, err
	}
	return workspace, nil
}

func (s *Service) ListWorkspaces(ctx context.Context, userID string) ([]Workspace, error) {
	return s.store.ListWorkspacesForUser(ctx, userID)
}

func (s *Service) ListMembers(ctx context.Context, userID, workspaceID string) ([]Member, error) {
	if _, err := s.requireActiveMember(ctx, userID, workspaceID); err != nil {
		return nil, err
	}
	return s.store.ListMembers(ctx, workspaceID)
}

func (s *Service) RequireActiveMember(ctx context.Context, userID, workspaceID string) (Member, error) {
	return s.requireActiveMember(ctx, userID, workspaceID)
}

func (s *Service) Invite(ctx context.Context, inviter identity.User, workspaceID, email, role string) (Invitation, error) {
	email = normalizeEmail(email)
	if _, err := mail.ParseAddress(email); err != nil {
		return Invitation{}, fmt.Errorf("%w: invalid email", ErrValidation)
	}
	role = normalizeRole(role)
	if role != "admin" && role != "member" {
		return Invitation{}, fmt.Errorf("%w: invalid role", ErrValidation)
	}
	member, err := s.requireActiveMember(ctx, inviter.ID, workspaceID)
	if err != nil {
		return Invitation{}, err
	}
	if member.Role != "owner" && member.Role != "admin" {
		return Invitation{}, ErrForbidden
	}
	inviteID, err := id.New()
	if err != nil {
		return Invitation{}, err
	}
	now := s.now().UTC()
	invitation := Invitation{ID: inviteID, WorkspaceID: workspaceID, Email: email, Role: role, Status: "pending", InvitedBy: inviter.ID, CreatedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour)}
	if err = s.store.CreateInvitation(ctx, invitation); err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}

func (s *Service) ListInvitations(ctx context.Context, userID, workspaceID string) ([]Invitation, error) {
	member, err := s.requireActiveMember(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	if member.Role != "owner" && member.Role != "admin" {
		return nil, ErrForbidden
	}
	return s.store.ListInvitations(ctx, workspaceID)
}

func (s *Service) AcceptInvitation(ctx context.Context, user identity.User, invitationID string) (Member, Invitation, error) {
	invitation, err := s.store.GetInvitation(ctx, invitationID)
	if err != nil {
		return Member{}, Invitation{}, err
	}
	now := s.now().UTC()
	if invitation.Status != "pending" || !now.Before(invitation.ExpiresAt) {
		return Member{}, Invitation{}, ErrConflict
	}
	if !strings.EqualFold(invitation.Email, user.Email) {
		return Member{}, Invitation{}, ErrForbidden
	}
	return s.store.AcceptInvitation(ctx, invitationID, user, now)
}

func (s *Service) requireActiveMember(ctx context.Context, userID, workspaceID string) (Member, error) {
	member, err := s.store.GetMember(ctx, workspaceID, userID)
	if err != nil {
		return Member{}, err
	}
	if member.Status != "active" {
		return Member{}, ErrForbidden
	}
	return member, nil
}

func normalizeEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func normalizeRole(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "member"
	}
	return value
}
