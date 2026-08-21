package postgresstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/team"
)

var _ team.Store = (*Store)(nil)

func (s *Store) CreateWorkspace(ctx context.Context, workspace team.Workspace, owner team.Member) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.workspaces (id,name,owner_id,status,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		workspace.ID, workspace.Name, workspace.OwnerID, workspace.Status,
		workspace.CreatedAt, workspace.UpdatedAt,
	); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.workspace_members
			(workspace_id,user_id,role,status,joined_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		owner.WorkspaceID, owner.UserID, owner.Role, owner.Status,
		owner.JoinedAt, owner.UpdatedAt,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListWorkspacesForUser(ctx context.Context, userID string) ([]team.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, workspaceSelect+`
		JOIN app.workspace_members m ON m.workspace_id=w.id
		WHERE m.user_id=$1 AND m.status='active' AND w.status='active'
		ORDER BY w.created_at`, userID)
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
	item, err := scanWorkspace(s.db.QueryRowContext(ctx, workspaceSelect+`
		WHERE w.id=$1 AND w.status='active'`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Workspace{}, team.ErrNotFound
	}
	return item, err
}

func (s *Store) GetMember(ctx context.Context, workspaceID, userID string) (team.Member, error) {
	item, err := scanTeamMember(s.db.QueryRowContext(ctx, memberSelect+`
		WHERE m.workspace_id=$1 AND m.user_id=$2`, workspaceID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Member{}, team.ErrNotFound
	}
	return item, err
}

func (s *Store) ListMembers(ctx context.Context, workspaceID string) ([]team.Member, error) {
	rows, err := s.db.QueryContext(ctx, memberSelect+`
		WHERE m.workspace_id=$1 AND m.status='active'
		ORDER BY CASE m.role WHEN 'owner' THEN 1 WHEN 'admin' THEN 2 ELSE 3 END,m.joined_at`,
		workspaceID,
	)
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app.workspace_invitations
			(id,workspace_id,email,role,status,invited_by,created_at,expires_at,accepted_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		invitation.ID, invitation.WorkspaceID, invitation.Email, invitation.Role,
		invitation.Status, invitation.InvitedBy, invitation.CreatedAt,
		invitation.ExpiresAt, invitation.AcceptedAt,
	)
	if isUniqueViolation(err) {
		return team.ErrConflict
	}
	return err
}

func (s *Store) ListInvitations(ctx context.Context, workspaceID string) ([]team.Invitation, error) {
	rows, err := s.db.QueryContext(ctx, invitationSelect+`
		WHERE workspace_id=$1 ORDER BY created_at DESC`, workspaceID)
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
	item, err := scanInvitation(s.db.QueryRowContext(ctx, invitationSelect+` WHERE id=$1`, invitationID))
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
	invitation, err := scanInvitation(tx.QueryRowContext(ctx, invitationSelect+`
		WHERE id=$1 FOR UPDATE`, invitationID))
	if errors.Is(err, sql.ErrNoRows) {
		return team.Member{}, team.Invitation{}, team.ErrNotFound
	}
	if err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	if invitation.Status != "pending" || !now.Before(invitation.ExpiresAt) {
		return team.Member{}, team.Invitation{}, team.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.workspace_members
			(workspace_id,user_id,role,status,joined_at,updated_at)
		VALUES ($1,$2,$3,'active',$4,$4)
		ON CONFLICT (workspace_id,user_id) DO UPDATE SET
			role=EXCLUDED.role,status='active',updated_at=EXCLUDED.updated_at`,
		invitation.WorkspaceID, user.ID, invitation.Role, now,
	); err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE app.workspace_invitations
		SET status='accepted',accepted_at=$1
		WHERE id=$2 AND status='pending'`,
		now, invitationID,
	)
	if err != nil {
		return team.Member{}, team.Invitation{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return team.Member{}, team.Invitation{}, team.ErrConflict
	}
	member, err := scanTeamMember(tx.QueryRowContext(ctx, memberSelect+`
		WHERE m.workspace_id=$1 AND m.user_id=$2`, invitation.WorkspaceID, user.ID))
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

const workspaceSelect = `
	SELECT w.id::text,w.name,w.owner_id::text,w.status,w.created_at,w.updated_at
	FROM app.workspaces w`

const memberSelect = `
	SELECT m.workspace_id::text,m.user_id::text,u.email,u.display_name,
		m.role,m.status,m.joined_at,m.updated_at
	FROM app.workspace_members m
	JOIN app.users u ON u.id=m.user_id`

const invitationSelect = `
	SELECT id::text,workspace_id::text,email,role,status,invited_by::text,
		created_at,expires_at,accepted_at
	FROM app.workspace_invitations`

func scanWorkspace(row rowScanner) (team.Workspace, error) {
	var item team.Workspace
	err := row.Scan(&item.ID, &item.Name, &item.OwnerID, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func scanTeamMember(row rowScanner) (team.Member, error) {
	var item team.Member
	err := row.Scan(
		&item.WorkspaceID, &item.UserID, &item.Email, &item.DisplayName,
		&item.Role, &item.Status, &item.JoinedAt, &item.UpdatedAt,
	)
	return item, err
}

func scanInvitation(row rowScanner) (team.Invitation, error) {
	var item team.Invitation
	var accepted sql.NullTime
	err := row.Scan(
		&item.ID, &item.WorkspaceID, &item.Email, &item.Role, &item.Status,
		&item.InvitedBy, &item.CreatedAt, &item.ExpiresAt, &accepted,
	)
	if accepted.Valid {
		value := accepted.Time
		item.AcceptedAt = &value
	}
	return item, err
}
