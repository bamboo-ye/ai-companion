package team

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/windcry1/ai-companion/internal/identity"
)

type MemoryStore struct {
	mu         sync.Mutex
	workspaces map[string]Workspace
	members    map[string]Member
	invites    map[string]Invitation
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{workspaces: map[string]Workspace{}, members: map[string]Member{}, invites: map[string]Invitation{}}
}

func (s *MemoryStore) CreateWorkspace(_ context.Context, workspace Workspace, owner Member) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.workspaces[workspace.ID]; exists {
		return ErrConflict
	}
	s.workspaces[workspace.ID] = workspace
	s.members[memberKey(workspace.ID, owner.UserID)] = owner
	return nil
}

func (s *MemoryStore) ListWorkspacesForUser(_ context.Context, userID string) ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Workspace, 0)
	for _, member := range s.members {
		if member.UserID != userID || member.Status != "active" {
			continue
		}
		if workspace, ok := s.workspaces[member.WorkspaceID]; ok && workspace.Status == "active" {
			result = append(result, workspace)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

func (s *MemoryStore) GetWorkspace(_ context.Context, workspaceID string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workspace, ok := s.workspaces[workspaceID]
	if !ok || workspace.Status != "active" {
		return Workspace{}, ErrNotFound
	}
	return workspace, nil
}

func (s *MemoryStore) GetMember(_ context.Context, workspaceID, userID string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, ok := s.members[memberKey(workspaceID, userID)]
	if !ok {
		return Member{}, ErrNotFound
	}
	return member, nil
}

func (s *MemoryStore) ListMembers(_ context.Context, workspaceID string) ([]Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Member, 0)
	for _, member := range s.members {
		if member.WorkspaceID == workspaceID && member.Status == "active" {
			result = append(result, member)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Role == result[j].Role {
			return result[i].JoinedAt.Before(result[j].JoinedAt)
		}
		return roleRank(result[i].Role) < roleRank(result[j].Role)
	})
	return result, nil
}

func (s *MemoryStore) CreateInvitation(_ context.Context, invitation Invitation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.invites {
		if existing.WorkspaceID == invitation.WorkspaceID && strings.EqualFold(existing.Email, invitation.Email) && existing.Status == "pending" {
			return ErrConflict
		}
	}
	s.invites[invitation.ID] = invitation
	return nil
}

func (s *MemoryStore) ListInvitations(_ context.Context, workspaceID string) ([]Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Invitation, 0)
	for _, invitation := range s.invites {
		if invitation.WorkspaceID == workspaceID {
			result = append(result, invitation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *MemoryStore) GetInvitation(_ context.Context, invitationID string) (Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invitation, ok := s.invites[invitationID]
	if !ok {
		return Invitation{}, ErrNotFound
	}
	return invitation, nil
}

func (s *MemoryStore) AcceptInvitation(_ context.Context, invitationID string, user identity.User, now time.Time) (Member, Invitation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	invitation, ok := s.invites[invitationID]
	if !ok {
		return Member{}, Invitation{}, ErrNotFound
	}
	if invitation.Status != "pending" || !now.Before(invitation.ExpiresAt) {
		return Member{}, Invitation{}, ErrConflict
	}
	invitation.Status = "accepted"
	invitation.AcceptedAt = &now
	s.invites[invitationID] = invitation
	member := Member{WorkspaceID: invitation.WorkspaceID, UserID: user.ID, Email: user.Email, DisplayName: user.DisplayName, Role: invitation.Role, Status: "active", JoinedAt: now, UpdatedAt: now}
	s.members[memberKey(invitation.WorkspaceID, user.ID)] = member
	return member, invitation, nil
}

func memberKey(workspaceID, userID string) string { return workspaceID + ":" + userID }

func roleRank(role string) int {
	switch role {
	case "owner":
		return 0
	case "admin":
		return 1
	default:
		return 2
	}
}
