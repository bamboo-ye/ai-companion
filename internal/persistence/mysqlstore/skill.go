package mysqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/skill"
)

func (s *Store) ListSkillSettings(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT skill_name,enabled FROM user_skill_settings WHERE user_id=UUID_TO_BIN(?)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]bool)
	for rows.Next() {
		var name string
		var enabled bool
		if err = rows.Scan(&name, &enabled); err != nil {
			return nil, err
		}
		settings[name] = enabled
	}
	return settings, rows.Err()
}

func (s *Store) SetSkillEnabled(ctx context.Context, userID, skillName string, enabled bool, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO user_skill_settings (user_id,skill_name,enabled,created_at,updated_at) VALUES (UUID_TO_BIN(?),?,?,?,?) ON DUPLICATE KEY UPDATE enabled=VALUES(enabled),updated_at=VALUES(updated_at)`, userID, skillName, enabled, now, now); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{"skill_name": skillName, "enabled": enabled})
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,metadata,occurred_at) VALUES ('user',UUID_TO_BIN(?),'skill.settings.update','skill',?,?)`, userID, metadata, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecoverInterruptedSkillRuns(ctx context.Context, now time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE skill_runs SET status='failed',current_state='failed',error_code='execution_interrupted',error_message='Skill execution was interrupted by a service restart; retry is safe',revision=revision+1,updated_at=?,completed_at=? WHERE status='running' AND execution_mode='inline'`, now, now)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (s *Store) ClaimSkillRun(ctx context.Context, workerID string, now time.Time, lease time.Duration) (skill.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return skill.Run{}, err
	}
	defer tx.Rollback()
	var runID, userID string
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id) FROM skill_runs WHERE execution_mode='worker' AND available_at<=? AND (status='queued' OR (status='running' AND lease_expires_at<=?)) ORDER BY available_at,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, now, now).Scan(&runID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNoQueuedRun
	}
	if err != nil {
		return skill.Run{}, err
	}
	expires := now.Add(lease)
	result, err := tx.ExecContext(ctx, `UPDATE skill_runs SET status='running',current_state='execute',worker_id=?,lease_expires_at=?,revision=revision+1,updated_at=? WHERE id=UUID_TO_BIN(?) AND execution_mode='worker'`, workerID, expires, now, runID)
	if err != nil {
		return skill.Run{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return skill.Run{}, skill.ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return skill.Run{}, err
	}
	return s.GetSkillRun(ctx, userID, runID)
}

func (s *Store) ClaimSkillRunByID(ctx context.Context, runID, workerID string, now time.Time, lease time.Duration) (skill.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return skill.Run{}, err
	}
	defer tx.Rollback()
	var userID string
	err = tx.QueryRowContext(ctx, `SELECT BIN_TO_UUID(user_id) FROM skill_runs WHERE id=UUID_TO_BIN(?) AND execution_mode='worker' AND available_at<=? AND (status='queued' OR (status='running' AND lease_expires_at<=?)) FOR UPDATE`, runID, now, now).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNoQueuedRun
	}
	if err != nil {
		return skill.Run{}, err
	}
	expires := now.Add(lease)
	result, err := tx.ExecContext(ctx, `UPDATE skill_runs SET status='running',current_state='execute',worker_id=?,lease_expires_at=?,revision=revision+1,updated_at=? WHERE id=UUID_TO_BIN(?) AND execution_mode='worker'`, workerID, expires, now, runID)
	if err != nil {
		return skill.Run{}, err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return skill.Run{}, skill.ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return skill.Run{}, err
	}
	return s.GetSkillRun(ctx, userID, runID)
}

func (s *Store) RenewSkillRunLease(ctx context.Context, runID, workerID string, revision int, now time.Time, lease time.Duration) error {
	expires := now.Add(lease)
	result, err := s.db.ExecContext(ctx, `UPDATE skill_runs SET lease_expires_at=?,updated_at=? WHERE id=UUID_TO_BIN(?) AND execution_mode='worker' AND status='running' AND worker_id=? AND revision=? AND lease_expires_at>?`, expires, now, runID, workerID, revision, now)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return skill.ErrConflict
	}
	return nil
}

func (s *Store) CreateSkillRun(ctx context.Context, run skill.Run) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO skill_runs (id,user_id,skill_name,skill_version,execution_mode,status,current_state,risk_level,requires_confirmation,input_json,output_json,create_key,confirmation_key,last_action,last_action_key,attempt,max_steps,timeout_ms,max_input_bytes,max_cost_micros,error_code,error_message,revision,available_at,worker_id,lease_expires_at,created_at,updated_at,completed_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,?,?,?,NULLIF(?,''),?,NULLIF(?,''),?,NULLIF(?,''),?,?,?,?,?,?,?,?,?,NULLIF(?,''),?,?,?,?)`,
		run.ID, run.UserID, run.SkillName, run.SkillVersion, run.ExecutionMode, run.Status, run.CurrentState, run.RiskLevel, run.RequiresConfirmation, run.Input, run.Output, run.CreateKey, run.ConfirmationKey, run.LastAction, run.LastActionKey, run.Attempt, run.MaxSteps, run.TimeoutMS, run.MaxInputBytes, run.MaxCostMicros, run.ErrorCode, run.ErrorMessage, run.Revision, run.AvailableAt, run.WorkerID, run.LeaseExpiresAt, run.CreatedAt, run.UpdatedAt, run.CompletedAt)
	if isDuplicate(err) {
		return skill.ErrConflict
	}
	if err != nil {
		return err
	}
	for _, step := range run.Steps {
		if err = insertSkillStep(ctx, tx, run, step); err != nil {
			return err
		}
	}
	if err = appendSkillQueuedOutbox(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetSkillRun(ctx context.Context, userID, runID string) (skill.Run, error) {
	run, err := scanSkillRun(s.db.QueryRowContext(ctx, skillRunSelect+` WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?)`, runID, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNotFound
	}
	if err != nil {
		return skill.Run{}, err
	}
	if err = s.loadSkillChildren(ctx, &run); err != nil {
		return skill.Run{}, err
	}
	return run, nil
}

func (s *Store) FindSkillRunByCreateKey(ctx context.Context, userID, key string) (skill.Run, error) {
	run, err := scanSkillRun(s.db.QueryRowContext(ctx, skillRunSelect+` WHERE user_id=UUID_TO_BIN(?) AND create_key=?`, userID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNotFound
	}
	if err != nil {
		return skill.Run{}, err
	}
	if err = s.loadSkillChildren(ctx, &run); err != nil {
		return skill.Run{}, err
	}
	return run, nil
}

func (s *Store) ListSkillRuns(ctx context.Context, userID string, limit int) ([]skill.Run, error) {
	rows, err := s.db.QueryContext(ctx, skillRunSelect+` WHERE user_id=UUID_TO_BIN(?) ORDER BY created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	items := make([]skill.Run, 0)
	for rows.Next() {
		run, scanErr := scanSkillRun(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		items = append(items, run)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		if err = s.loadSkillChildren(ctx, &items[index]); err != nil {
			return nil, err
		}
	}
	return items, nil
}

func (s *Store) SaveSkillRun(ctx context.Context, run skill.Run, expectedRevision int, steps []skill.Step, files []skill.GeneratedFile) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE skill_runs SET status=?,current_state=?,output_json=NULLIF(?,''),confirmation_key=NULLIF(?,''),last_action=?,last_action_key=NULLIF(?,''),attempt=?,error_code=?,error_message=?,revision=?,available_at=?,worker_id=NULL,lease_expires_at=NULL,updated_at=?,completed_at=? WHERE id=UUID_TO_BIN(?) AND user_id=UUID_TO_BIN(?) AND revision=?`,
		run.Status, run.CurrentState, run.Output, run.ConfirmationKey, run.LastAction, run.LastActionKey, run.Attempt, run.ErrorCode, run.ErrorMessage, run.Revision, run.AvailableAt, run.UpdatedAt, run.CompletedAt, run.ID, run.UserID, expectedRevision)
	if isDuplicate(err) {
		return skill.ErrConflict
	}
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return skill.ErrConflict
	}
	if err = insertSkillActionKey(ctx, tx, run, steps); err != nil {
		return err
	}
	for _, step := range steps {
		if err = insertSkillStep(ctx, tx, run, step); err != nil {
			return err
		}
	}
	for _, file := range files {
		_, err = tx.ExecContext(ctx, `INSERT INTO generated_files (id,run_id,user_id,display_name,media_type,size_bytes,sha256,storage_key,status,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,'active',?)`, file.ID, run.ID, run.UserID, file.Name, file.MediaType, file.SizeBytes, file.SHA256, file.StorageKey, file.CreatedAt)
		if isDuplicate(err) {
			return skill.ErrConflict
		}
		if err != nil {
			return err
		}
	}
	if err = appendSkillAuditAndOutbox(ctx, tx, run, steps); err != nil {
		return err
	}
	if err = appendSkillQueuedOutbox(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func appendSkillQueuedOutbox(ctx context.Context, tx *sql.Tx, run skill.Run) error {
	if run.Status != "queued" || run.ExecutionMode != "worker" {
		return nil
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"run_id": run.ID, "user_id": run.UserID, "skill_name": run.SkillName, "skill_version": run.SkillVersion})
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'skill_run',UUID_TO_BIN(?),'skill.execute.v1',1,?,?)`, eventID, run.ID, payload, run.UpdatedAt)
	return err
}

func insertSkillActionKey(ctx context.Context, tx *sql.Tx, run skill.Run, steps []skill.Step) error {
	for _, step := range steps {
		action, key := "", ""
		switch step.State {
		case "confirm":
			action, key = "confirm", run.ConfirmationKey
		case "cancel", "retry":
			action, key = step.State, run.LastActionKey
		}
		if key == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO skill_run_action_keys (run_id,user_id,action,idempotency_key,created_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?)`, run.ID, run.UserID, action, key, step.StartedAt)
		if isDuplicate(err) {
			return skill.ErrConflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func insertSkillStep(ctx context.Context, tx *sql.Tx, run skill.Run, step skill.Step) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO skill_run_steps (id,run_id,sequence_no,state,status,tool_name,input_json,output_json,error_code,error_message,started_at,completed_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?)`, step.ID, run.ID, step.Sequence, step.State, step.Status, step.ToolName, step.Input, step.Output, step.ErrorCode, step.ErrorMessage, step.StartedAt, step.CompletedAt)
	if isDuplicate(err) {
		return skill.ErrConflict
	}
	if err != nil || step.ToolName == "" {
		return err
	}
	digest := sha256.Sum256(step.Input)
	_, err = tx.ExecContext(ctx, `INSERT INTO tool_executions (id,run_id,user_id,tool_name,risk_level,status,idempotency_key,input_sha256,output_json,error_code,error_message,started_at,completed_at) VALUES (UUID_TO_BIN(?),UUID_TO_BIN(?),UUID_TO_BIN(?),?,?,?,?,?,NULLIF(?,''),?,?,?,?)`, step.ID, run.ID, run.UserID, step.ToolName, run.RiskLevel, step.Status, nullableString(run.ConfirmationKey), hex.EncodeToString(digest[:]), step.Output, step.ErrorCode, step.ErrorMessage, step.StartedAt, step.CompletedAt)
	return err
}

func appendSkillAuditAndOutbox(ctx context.Context, tx *sql.Tx, run skill.Run, steps []skill.Step) error {
	for _, step := range steps {
		if step.State != "confirm" && step.State != "execute" && step.State != "cancel" {
			continue
		}
		metadata, _ := json.Marshal(map[string]any{"skill_name": run.SkillName, "skill_version": run.SkillVersion, "state": step.State, "status": step.Status, "tool_name": step.ToolName})
		action := "skill." + step.State
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at) VALUES ('user',UUID_TO_BIN(?),?,'skill_run',UUID_TO_BIN(?),?,?)`, run.UserID, action, run.ID, metadata, step.StartedAt); err != nil {
			return err
		}
	}
	if run.Status != "succeeded" {
		return nil
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"run_id": run.ID, "user_id": run.UserID, "skill_name": run.SkillName, "skill_version": run.SkillVersion})
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox_events (id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at) VALUES (UUID_TO_BIN(?),'skill_run',UUID_TO_BIN(?),'skill.run.succeeded.v1',1,?,?)`, eventID, run.ID, payload, run.UpdatedAt)
	return err
}

func (s *Store) loadSkillChildren(ctx context.Context, run *skill.Run) error {
	stepRows, err := s.db.QueryContext(ctx, skillStepSelect+` WHERE run_id=UUID_TO_BIN(?) ORDER BY sequence_no`, run.ID)
	if err != nil {
		return err
	}
	run.Steps = make([]skill.Step, 0)
	for stepRows.Next() {
		step, scanErr := scanSkillStep(stepRows)
		if scanErr != nil {
			stepRows.Close()
			return scanErr
		}
		run.Steps = append(run.Steps, step)
	}
	if err = stepRows.Close(); err != nil {
		return err
	}
	fileRows, err := s.db.QueryContext(ctx, skillFileSelect+` WHERE run_id=UUID_TO_BIN(?) AND status='active' ORDER BY created_at`, run.ID)
	if err != nil {
		return err
	}
	run.Files = make([]skill.GeneratedFile, 0)
	for fileRows.Next() {
		file, scanErr := scanSkillFile(fileRows)
		if scanErr != nil {
			fileRows.Close()
			return scanErr
		}
		run.Files = append(run.Files, file)
	}
	return fileRows.Close()
}

const skillRunSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(user_id),skill_name,skill_version,execution_mode,status,current_state,risk_level,requires_confirmation,input_json,output_json,create_key,COALESCE(confirmation_key,''),last_action,COALESCE(last_action_key,''),attempt,max_steps,timeout_ms,max_input_bytes,max_cost_micros,error_code,error_message,revision,available_at,COALESCE(worker_id,''),lease_expires_at,created_at,updated_at,completed_at FROM skill_runs`
const skillStepSelect = `SELECT BIN_TO_UUID(id),sequence_no,state,status,tool_name,input_json,output_json,error_code,error_message,started_at,completed_at FROM skill_run_steps`
const skillFileSelect = `SELECT BIN_TO_UUID(id),BIN_TO_UUID(run_id),display_name,media_type,size_bytes,sha256,storage_key,created_at FROM generated_files`

func scanSkillRun(row rowScanner) (skill.Run, error) {
	var run skill.Run
	var input, output []byte
	var completed, leaseExpires sql.NullTime
	err := row.Scan(&run.ID, &run.UserID, &run.SkillName, &run.SkillVersion, &run.ExecutionMode, &run.Status, &run.CurrentState, &run.RiskLevel, &run.RequiresConfirmation, &input, &output, &run.CreateKey, &run.ConfirmationKey, &run.LastAction, &run.LastActionKey, &run.Attempt, &run.MaxSteps, &run.TimeoutMS, &run.MaxInputBytes, &run.MaxCostMicros, &run.ErrorCode, &run.ErrorMessage, &run.Revision, &run.AvailableAt, &run.WorkerID, &leaseExpires, &run.CreatedAt, &run.UpdatedAt, &completed)
	run.Input, run.Output = append([]byte(nil), input...), append([]byte(nil), output...)
	if completed.Valid {
		run.CompletedAt = &completed.Time
	}
	if leaseExpires.Valid {
		run.LeaseExpiresAt = &leaseExpires.Time
	}
	return run, err
}

func scanSkillStep(row rowScanner) (skill.Step, error) {
	var step skill.Step
	var input, output []byte
	var completed sql.NullTime
	err := row.Scan(&step.ID, &step.Sequence, &step.State, &step.Status, &step.ToolName, &input, &output, &step.ErrorCode, &step.ErrorMessage, &step.StartedAt, &completed)
	step.Input, step.Output = append([]byte(nil), input...), append([]byte(nil), output...)
	if completed.Valid {
		step.CompletedAt = &completed.Time
	}
	return step, err
}

func scanSkillFile(row rowScanner) (skill.GeneratedFile, error) {
	var file skill.GeneratedFile
	err := row.Scan(&file.ID, &file.RunID, &file.Name, &file.MediaType, &file.SizeBytes, &file.SHA256, &file.StorageKey, &file.CreatedAt)
	return file, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
