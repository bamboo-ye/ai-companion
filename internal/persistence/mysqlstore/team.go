package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/team"
)

func (s *Store) CreateWorkspace(ctx context.Context, workspace team.Workspace, owner team.Member) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspaces (id,name,owner_id,status,created_at,updated_at) VALUES (UUID_TO_BIN(?),?,UUID_TO_BIN(?),?,?,?)`, workspace.ID, workspace.Name, workspace.OwnerID, workspace.Status, workspace.CreatedAt, workspace.UpdatedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role,status,joined_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?)`, owner.WorkspaceID, owner.UserID, owner.Role, owner.Status, owner.JoinedAt, owner.UpdatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListWorkspacesForUser(ctx context.Context, userID string) ([]team.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, workspaceSelect+` JOIN workspace_members m ON m.workspace_id=w.id WHERE m.user_id=UUID_TO_BIN(?) AND m.status='active' AND w.status='active' ORDER BY w.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]team.Workspace, 0)
	for rows.Next() {
		item, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetWorkspace(ctx context.Context, workspaceID string) (team.Workspace, error) {
	item, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceSelect+` WHERE w.id=UUID_TO_BIN(?) AND w.status='active'`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Workspace{}, team.ErrNotFound
	}
	return item, err
}

func (s *Store) GetMember(ctx context.Context, workspaceID, userID string) (team.Member, error) {
	member, err := scanTeamMember(s.db.QueryRowContext(ctx, memberSelect+` WHERE m.workspace_id=UUID_TO_BIN(?) AND m.user_id=UUID_TO_BIN(?)`, workspaceID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Member{}, team.ErrNotFound
	}
	return member, err
}

func (s *Store) ListMembers(ctx context.Context, workspaceID string) ([]team.Member, error) {
	rows, err := s.db.QueryContext(ctx, memberSelect+` WHERE m.workspace_id=UUID_TO_BIN(?) AND m.status='active' ORDER BY FIELD(m.role,'owner','admin','member'),m.joined_at`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]team.Member, 0)
	for rows.Next() {
		item, scanErr := scanTeamMember(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateInvitation(ctx context.Context, invitation team.Invitation) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_invitations (id,workspace_id,email,role,status,invited_by,created_at,expires_at,accepted_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?)`, invitation.ID, invitation.WorkspaceID, invitation.Email, invitation.Role, invitation.Status, invitation.InvitedBy, invitation.CreatedAt, invitation.ExpiresAt, invitation.AcceptedAt)
	if isDuplicate(err) {
		return team.ErrConflict
	}
	return err
}

func (s *Store) ListInvitations(ctx context.Context, workspaceID string) ([]team.Invitation, error) {
	rows, err := s.db.QueryContext(ctx, invitationSelect+` WHERE workspace_id=UUID_TO_BIN(?) ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]team.Invitation, 0)
	for rows.Next() {
		item, scanErr := scanInvitation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetInvitation(ctx context.Context, invitationID string) (team.Invitation, error) {
	item, err := scanInvitation(s.db.QueryRowContext(ctx, invitationSelect+` WHERE id=UUID_TO_BIN(?)`, invitationID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Invitation{}, team.ErrNotFound
	}
	return item, err
}

func (s *Store) AcceptInvitation(ctx context.Context, invitationID string, user identity.User, now time.Time) (team.Member, team.Invitation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	defer tx.Rollback()

	invitation, err := scanInvitation(tx.QueryRowContext(ctx, invitationSelect+` WHERE id=UUID_TO_BIN(?) FOR UPDATE`, invitationID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Member{}, team.Invitation{}, team.ErrNotFound
	}
	if err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	if invitation.Status != "pending" || !now.Before(invitation.ExpiresAt) {
		return team.Member{}, team.Invitation{}, team.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO workspace_members (workspace_id,user_id,role,status,joined_at,updated_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,'active',?,?) ON DUPLICATE KEY UPDATE role=VALUES(role),status='active',updated_at=VALUES(updated_at)`, invitation.WorkspaceID, user.ID, invitation.Role, now, now); err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workspace_invitations SET status='accepted',accepted_at=? WHERE id=UUID_TO_BIN(?) AND status='pending'`, now, invitationID)
	if err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return team.Member{}, team.Invitation{}, team.ErrConflict
	}
	member, err := scanTeamMember(tx.QueryRowContext(ctx, memberSelect+` WHERE m.workspace_id=UUID_TO_BIN(?) AND m.user_id=UUID_TO_BIN(?)`, invitation.WorkspaceID, user.ID))
	if err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	if err = tx.Commit(); err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	invitation.Status = "accepted"
	invitation.AcceptedAt = &now
	return member, invitation, nil
}

const workspaceSelect = `SELECT BIN_TO_UUID(w.id),w.name,BIN_TO_UUID(w.owner_id),w.status,w.created_at,w.updated_at FROM workspaces w`
const memberSelect = `SELECT BIN_TO_UUID(m.workspace_id),BIN_TO_UUID(m.user_id),u.email,u.display_name,m.role,m.status,m.joined_at,m.updated_at FROM workspace_members m JOIN users u ON u.id=m.user_id`
const invitationSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(workspace_id),email,role,status,BIN_TO_UUID(invited_by),created_at,expires_at,accepted_at FROM workspace_invitations`

func scanWorkspace(row rowScanner) (team.Workspace, error) {
	var item team.Workspace
	if err := row.Scan(&item.ID, &item.Name, &item.OwnerID, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return team.Workspace{}, err
	}
	return item, nil
}

func scanTeamMember(row rowScanner) (team.Member, error) {
	var item team.Member
	if err := row.Scan(&item.WorkspaceID, &item.UserID, &item.Email, &item.DisplayName, &item.Role, &item.Status, &item.JoinedAt, &item.UpdatedAt); err != nil {
		return team.Member{}, err
	}
	return item, nil
}

func scanInvitation(row rowScanner) (team.Invitation, error) {
	var item team.Invitation
	var accepted sql.NullTime
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.Email, &item.Role, &item.Status, &item.InvitedBy, &item.CreatedAt, &item.ExpiresAt, &accepted); err != nil {
		return team.Invitation{}, err
	}
	if accepted.Valid {
		item.AcceptedAt = &accepted.Time
	}
	return item, nil
}
