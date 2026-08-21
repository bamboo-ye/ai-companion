package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/windcry1/ai-companion/internal/safety"
)

var _ safety.Store = (*Store)(nil)

func (s *Store) GetUserPolicy(ctx context.Context, userID string) (safety.UserPolicy, error) {
	item, err := scanUserSafetyPolicy(s.db.QueryRowContext(ctx, `
		SELECT user_id::text,minor_mode,guardian_email,risky_skills_allowed,created_at,updated_at
		FROM app.user_safety_policies
		WHERE user_id=$1`,
		userID,
	))
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
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.user_safety_policies
			(user_id,minor_mode,guardian_email,risky_skills_allowed,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (user_id) DO UPDATE SET
			minor_mode=EXCLUDED.minor_mode,
			guardian_email=EXCLUDED.guardian_email,
			risky_skills_allowed=EXCLUDED.risky_skills_allowed,
			updated_at=EXCLUDED.updated_at`,
		policy.UserID, policy.MinorMode, policy.GuardianEmail,
		policy.RiskySkillsAllowed, policy.CreatedAt, policy.UpdatedAt,
	); err != nil {
		return safety.UserPolicy{}, err
	}
	metadata, _ := json.Marshal(map[string]any{
		"minor_mode":           policy.MinorMode,
		"guardian_email_set":   policy.GuardianEmail != "",
		"risky_skills_allowed": policy.RiskySkillsAllowed,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs
			(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('user',$1,'safety.policy.update','user_safety_policy',$1,$2,$3)`,
		policy.UserID, metadata, policy.UpdatedAt,
	); err != nil {
		return safety.UserPolicy{}, err
	}
	if err = tx.Commit(); err != nil {
		return safety.UserPolicy{}, err
	}
	return s.GetUserPolicy(ctx, policy.UserID)
}

func scanUserSafetyPolicy(row rowScanner) (safety.UserPolicy, error) {
	var item safety.UserPolicy
	err := row.Scan(
		&item.UserID, &item.MinorMode, &item.GuardianEmail,
		&item.RiskySkillsAllowed, &item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}
