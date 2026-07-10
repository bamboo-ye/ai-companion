package mysqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/windcry1/ai-companion/internal/safety"
)

func (s *Store) GetUserPolicy(ctx context.Context, userID string) (safety.UserPolicy, error) {
	item, err := scanUserSafetyPolicy(s.db.QueryRowContext(ctx, `SELECT BIN_TO_UUID(user_id),minor_mode,guardian_email,risky_skills_allowed,created_at,updated_at FROM user_safety_policies WHERE user_id=UUID_TO_BIN(?)`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return safety.UserPolicy{}, safety.ErrNotFound
	}
	return item, err
}

func (s *Store) UpsertUserPolicy(ctx context.Context, policy safety.UserPolicy) (safety.UserPolicy, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safety.UserPolicy{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_safety_policies (user_id,minor_mode,guardian_email,risky_skills_allowed,created_at,updated_at) VALUES (UUID_TO_BIN(?),?,?,?,?,?) ON DUPLICATE KEY UPDATE minor_mode=VALUES(minor_mode),guardian_email=VALUES(guardian_email),risky_skills_allowed=VALUES(risky_skills_allowed),updated_at=VALUES(updated_at)`, policy.UserID, policy.MinorMode, policy.GuardianEmail, policy.RiskySkillsAllowed, policy.CreatedAt, policy.UpdatedAt); err != nil {
		return safety.UserPolicy{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"minor_mode": policy.MinorMode, "guardian_email_set": policy.GuardianEmail != "", "risky_skills_allowed": policy.RiskySkillsAllowed})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('user',UUID_TO_BIN(?),'safety.policy.update','user_safety_policy',UUID_TO_BIN(?),?,?)`, policy.UserID, policy.UserID, metadata, policy.UpdatedAt); err != nil {
		return safety.UserPolicy{}, err
	}
	if err = tx.Commit(); err != nil {
		return safety.UserPolicy{}, err
	}
	return s.GetUserPolicy(ctx, policy.UserID)
}

func scanUserSafetyPolicy(row rowScanner) (safety.UserPolicy, error) {
	var item safety.UserPolicy
	if err := row.Scan(&item.UserID, &item.MinorMode, &item.GuardianEmail, &item.RiskySkillsAllowed, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return safety.UserPolicy{}, err
	}
	return item, nil
}
