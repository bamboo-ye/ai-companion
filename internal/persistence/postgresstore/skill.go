package postgresstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/skill"
)

var _ skill.Store = (*Store)(nil)

func (s *Store) ListSkillSettings(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT skill_name,enabled
		FROM app.user_skill_settings
		WHERE user_id=$1`,
		userID,
	)
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
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.user_skill_settings (
			user_id,skill_name,enabled,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$4)
		ON CONFLICT (user_id,skill_name) DO UPDATE SET
			enabled=EXCLUDED.enabled,
			updated_at=EXCLUDED.updated_at`,
		userID, skillName, enabled, now,
	); err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]any{
		"skill_name": skillName,
		"enabled":    enabled,
	})
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.audit_logs (
			actor_type,actor_id,action,resource_type,metadata,occurred_at
		) VALUES ('user',$1,'skill.settings.update','skill',$2,$3)`,
		userID, metadata, now,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecoverInterruptedSkillRuns(ctx context.Context, now time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.skill_runs SET
			status='failed',
			current_state='failed',
			error_code='execution_interrupted',
			error_message='Skill execution was interrupted by a service restart; retry is safe',
			worker_id=NULL,
			lease_expires_at=NULL,
			revision=revision+1,
			updated_at=$1,
			completed_at=$1
		WHERE status='running'
			AND (execution_mode='inline' OR worker_id IS NULL)`,
		now,
	)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

func (s *Store) ClaimSkillRun(ctx context.Context, workerID string, now time.Time, lease time.Duration) (skill.Run, error) {
	return s.claimSkillRun(ctx, "", workerID, now, lease)
}

func (s *Store) ClaimSkillRunByID(ctx context.Context, runID, workerID string, now time.Time, lease time.Duration) (skill.Run, error) {
	return s.claimSkillRun(ctx, runID, workerID, now, lease)
}

func (s *Store) claimSkillRun(ctx context.Context, runID, workerID string, now time.Time, lease time.Duration) (skill.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return skill.Run{}, err
	}
	defer tx.Rollback()
	var selectedID, userID string
	err = tx.QueryRowContext(ctx, `
		SELECT id::text,user_id::text
		FROM app.skill_runs
		WHERE ($1='' OR id=$1::uuid)
			AND execution_mode='worker'
			AND available_at<=$2
			AND (
				status='queued'
				OR (status='running' AND lease_expires_at<=$2)
			)
		ORDER BY available_at,created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`,
		runID, now,
	).Scan(&selectedID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNoQueuedRun
	}
	if err != nil {
		return skill.Run{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE app.skill_runs SET
			status='running',current_state='execute',worker_id=$1,
			lease_expires_at=$2,revision=revision+1,updated_at=$3
		WHERE id=$4 AND execution_mode='worker'`,
		workerID, now.Add(lease), now, selectedID,
	)
	if err != nil {
		return skill.Run{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return skill.Run{}, skill.ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return skill.Run{}, err
	}
	return s.GetSkillRun(ctx, userID, selectedID)
}

func (s *Store) RenewSkillRunLease(ctx context.Context, runID, workerID string, revision int, now time.Time, lease time.Duration) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE app.skill_runs SET lease_expires_at=$1,updated_at=$2
		WHERE id=$3
			AND execution_mode='worker'
			AND status='running'
			AND worker_id=$4
			AND revision=$5
			AND lease_expires_at>$2`,
		now.Add(lease), now, runID, workerID, revision,
	)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
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
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.skill_runs (
			id,user_id,skill_name,skill_version,execution_mode,status,current_state,
			risk_level,requires_confirmation,input_json,output_json,
			conversation_id,origin_message_id,create_key,
			confirmation_key,last_action,last_action_key,attempt,max_steps,timeout_ms,
			max_input_bytes,max_cost_micros,error_code,error_message,revision,
			available_at,worker_id,lease_expires_at,created_at,updated_at,completed_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,NULLIF($11,'')::jsonb,
			NULLIF($12,'')::uuid,NULLIF($13,'')::uuid,$14,
			NULLIF($15,''),$16,NULLIF($17,''),$18,$19,$20,$21,$22,$23,$24,$25,
			$26,NULLIF($27,''),$28,$29,$30,$31
		)`,
		run.ID, run.UserID, run.SkillName, run.SkillVersion, run.ExecutionMode,
		run.Status, run.CurrentState, run.RiskLevel, run.RequiresConfirmation,
		string(run.Input), string(run.Output), run.ConversationID, run.OriginMessageID,
		run.CreateKey, run.ConfirmationKey, run.LastAction, run.LastActionKey,
		run.Attempt, run.MaxSteps, run.TimeoutMS,
		run.MaxInputBytes, run.MaxCostMicros, run.ErrorCode, run.ErrorMessage,
		run.Revision, run.AvailableAt, run.WorkerID, run.LeaseExpiresAt,
		run.CreatedAt, run.UpdatedAt, run.CompletedAt,
	)
	if isUniqueViolation(err) {
		return skill.ErrConflict
	}
	if err != nil {
		return err
	}
	for _, step := range run.Steps {
		if err = insertPostgresSkillStep(ctx, tx, run, step); err != nil {
			return err
		}
	}
	if err = appendPostgresSkillQueuedOutbox(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetSkillRun(ctx context.Context, userID, runID string) (skill.Run, error) {
	run, err := scanPostgresSkillRun(s.db.QueryRowContext(
		ctx, skillRunSelect+` WHERE sr.id=$1 AND sr.user_id=$2`, runID, userID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNotFound
	}
	if err != nil {
		return skill.Run{}, err
	}
	if err = s.loadPostgresSkillChildren(ctx, &run); err != nil {
		return skill.Run{}, err
	}
	return run, nil
}

func (s *Store) FindSkillRunByCreateKey(ctx context.Context, userID, key string) (skill.Run, error) {
	run, err := scanPostgresSkillRun(s.db.QueryRowContext(
		ctx, skillRunSelect+` WHERE sr.user_id=$1 AND sr.create_key=$2`, userID, key,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Run{}, skill.ErrNotFound
	}
	if err != nil {
		return skill.Run{}, err
	}
	if err = s.loadPostgresSkillChildren(ctx, &run); err != nil {
		return skill.Run{}, err
	}
	return run, nil
}

func (s *Store) ListSkillRuns(ctx context.Context, userID string, limit int) ([]skill.Run, error) {
	rows, err := s.db.QueryContext(ctx, skillRunSelect+`
		WHERE sr.user_id=$1
		ORDER BY sr.created_at DESC
		LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		return nil, err
	}
	items := make([]skill.Run, 0)
	for rows.Next() {
		run, scanErr := scanPostgresSkillRun(rows)
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
		if err = s.loadPostgresSkillChildren(ctx, &items[index]); err != nil {
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
	result, err := tx.ExecContext(ctx, `
		UPDATE app.skill_runs SET
			status=$1,
			current_state=$2,
			input_json=$3::jsonb,
			output_json=NULLIF($4,'')::jsonb,
			confirmation_key=NULLIF($5,''),
			last_action=$6,
			last_action_key=NULLIF($7,''),
			attempt=$8,
			error_code=$9,
			error_message=$10,
			revision=$11,
			available_at=$12,
			worker_id=NULL,
			lease_expires_at=NULL,
			updated_at=$13,
			completed_at=$14
		WHERE id=$15 AND user_id=$16 AND revision=$17`,
		run.Status, run.CurrentState, string(run.Input), string(run.Output), run.ConfirmationKey,
		run.LastAction, run.LastActionKey, run.Attempt, run.ErrorCode,
		run.ErrorMessage, run.Revision, run.AvailableAt, run.UpdatedAt,
		run.CompletedAt, run.ID, run.UserID, expectedRevision,
	)
	if isUniqueViolation(err) {
		return skill.ErrConflict
	}
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return skill.ErrConflict
	}
	if err = insertPostgresSkillActionKey(ctx, tx, run, steps); err != nil {
		return err
	}
	for _, step := range steps {
		if err = insertPostgresSkillStep(ctx, tx, run, step); err != nil {
			return err
		}
	}
	for _, file := range files {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO app.generated_files (
				id,run_id,user_id,display_name,media_type,size_bytes,sha256,
				storage_key,status,created_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active',$9)`,
			file.ID, run.ID, run.UserID, file.Name, file.MediaType,
			file.SizeBytes, file.SHA256, file.StorageKey, file.CreatedAt,
		)
		if isUniqueViolation(err) {
			return skill.ErrConflict
		}
		if err != nil {
			return err
		}
	}
	if err = appendPostgresSkillRetryDelivery(ctx, tx, run); err != nil {
		return err
	}
	if err = appendPostgresSkillAuditAndOutbox(ctx, tx, run, steps); err != nil {
		return err
	}
	if err = appendPostgresSkillQueuedOutbox(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ShareGeneratedFileWithWorkspace(ctx context.Context, userID, workspaceID, runID, fileID string, now time.Time) error {
	if _, err := s.GetSkillRun(ctx, userID, runID); err != nil {
		return err
	}
	var exists bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM app.generated_files
			WHERE id=$1 AND run_id=$2 AND user_id=$3 AND status='active'
		)`,
		fileID, runID, userID,
	).Scan(&exists); err != nil {
		return err
	} else if !exists {
		return skill.ErrNotFound
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO app.workspace_generated_file_shares (
			workspace_id,file_id,shared_by,created_at
		) VALUES ($1,$2,$3,$4)
		ON CONFLICT (workspace_id,file_id) DO NOTHING`,
		workspaceID, fileID, userID, now,
	)
	return err
}

func (s *Store) ListWorkspaceGeneratedFiles(ctx context.Context, workspaceID string, limit int) ([]skill.GeneratedFile, error) {
	rows, err := s.db.QueryContext(ctx, skillFileSelect+`
		JOIN app.workspace_generated_file_shares wgfs ON wgfs.file_id=gf.id
		WHERE wgfs.workspace_id=$1 AND gf.status='active'
		ORDER BY wgfs.created_at DESC,gf.created_at DESC
		LIMIT $2`,
		workspaceID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]skill.GeneratedFile, 0)
	for rows.Next() {
		item, scanErr := scanPostgresSkillFile(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetWorkspaceGeneratedFile(ctx context.Context, workspaceID, fileID string) (skill.GeneratedFile, error) {
	item, err := scanPostgresSkillFile(s.db.QueryRowContext(ctx, skillFileSelect+`
		JOIN app.workspace_generated_file_shares wgfs ON wgfs.file_id=gf.id
		WHERE wgfs.workspace_id=$1 AND gf.id=$2 AND gf.status='active'`,
		workspaceID, fileID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.GeneratedFile{}, skill.ErrNotFound
	}
	return item, err
}

func appendPostgresSkillQueuedOutbox(ctx context.Context, tx *sql.Tx, run skill.Run) error {
	if run.Status != "queued" || run.ExecutionMode != "worker" {
		return nil
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{
		"run_id":        run.ID,
		"user_id":       run.UserID,
		"skill_name":    run.SkillName,
		"skill_version": run.SkillVersion,
	})
	_, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'skill_run',$2,'skill.execute.v1',1,$3,$4)`,
		eventID, run.ID, payload, run.UpdatedAt,
	)
	return err
}

func insertPostgresSkillActionKey(ctx context.Context, tx *sql.Tx, run skill.Run, steps []skill.Step) error {
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
		_, err := tx.ExecContext(ctx, `
			INSERT INTO app.skill_run_action_keys (
				run_id,user_id,action,idempotency_key,created_at
			) VALUES ($1,$2,$3,$4,$5)`,
			run.ID, run.UserID, action, key, step.StartedAt,
		)
		if isUniqueViolation(err) {
			return skill.ErrConflict
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func insertPostgresSkillStep(ctx context.Context, tx *sql.Tx, run skill.Run, step skill.Step) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO app.skill_run_steps (
			id,run_id,sequence_no,state,status,tool_name,input_json,output_json,
			error_code,error_message,started_at,completed_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,NULLIF($7,'')::jsonb,NULLIF($8,'')::jsonb,
			$9,$10,$11,$12
		)`,
		step.ID, run.ID, step.Sequence, step.State, step.Status, step.ToolName,
		string(step.Input), string(step.Output), step.ErrorCode, step.ErrorMessage,
		step.StartedAt, step.CompletedAt,
	)
	if isUniqueViolation(err) {
		return skill.ErrConflict
	}
	if err != nil || step.ToolName == "" {
		return err
	}
	digest := sha256.Sum256(step.Input)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.tool_executions (
			id,run_id,user_id,tool_name,risk_level,status,idempotency_key,
			input_sha256,output_json,error_code,error_message,started_at,completed_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,NULLIF($9,'')::jsonb,
			$10,$11,$12,$13
		)`,
		step.ID, run.ID, run.UserID, step.ToolName, run.RiskLevel, step.Status,
		run.ConfirmationKey, hex.EncodeToString(digest[:]), string(step.Output),
		step.ErrorCode, step.ErrorMessage, step.StartedAt, step.CompletedAt,
	)
	if isUniqueViolation(err) {
		return skill.ErrConflict
	}
	return err
}

func appendPostgresSkillAuditAndOutbox(ctx context.Context, tx *sql.Tx, run skill.Run, steps []skill.Step) error {
	for _, step := range steps {
		if step.State != "confirm" && step.State != "execute" && step.State != "cancel" {
			continue
		}
		metadata, _ := json.Marshal(map[string]any{
			"skill_name":    run.SkillName,
			"skill_version": run.SkillVersion,
			"state":         step.State,
			"status":        step.Status,
			"tool_name":     step.ToolName,
		})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO eventing.audit_logs (
				actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at
			) VALUES ('user',$1,$2,'skill_run',$3,$4,$5)`,
			run.UserID, "skill."+step.State, run.ID, metadata, step.StartedAt,
		); err != nil {
			return err
		}
	}
	eventType := ""
	switch run.Status {
	case "succeeded":
		eventType = "skill.run.succeeded.v1"
	case "failed":
		eventType = "skill.run.failed.v1"
	case "cancelled":
		eventType = "skill.run.cancelled.v1"
	}
	if eventType == "" {
		return nil
	}
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{
		"run_id":        run.ID,
		"user_id":       run.UserID,
		"skill_name":    run.SkillName,
		"skill_version": run.SkillVersion,
	})
	_, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,occurred_at
		) VALUES ($1,'skill_run',$2,$3,1,$4,$5)`,
		eventID, run.ID, eventType, payload, run.UpdatedAt,
	)
	return err
}

func (s *Store) loadPostgresSkillChildren(ctx context.Context, run *skill.Run) error {
	stepRows, err := s.db.QueryContext(ctx, skillStepSelect+`
		WHERE srs.run_id=$1
		ORDER BY srs.sequence_no`,
		run.ID,
	)
	if err != nil {
		return err
	}
	run.Steps = make([]skill.Step, 0)
	for stepRows.Next() {
		step, scanErr := scanPostgresSkillStep(stepRows)
		if scanErr != nil {
			stepRows.Close()
			return scanErr
		}
		run.Steps = append(run.Steps, step)
	}
	if err = stepRows.Close(); err != nil {
		return err
	}
	fileRows, err := s.db.QueryContext(ctx, skillFileSelect+`
		WHERE gf.run_id=$1 AND gf.status='active'
		ORDER BY gf.created_at`,
		run.ID,
	)
	if err != nil {
		return err
	}
	run.Files = make([]skill.GeneratedFile, 0)
	for fileRows.Next() {
		file, scanErr := scanPostgresSkillFile(fileRows)
		if scanErr != nil {
			fileRows.Close()
			return scanErr
		}
		run.Files = append(run.Files, file)
	}
	return fileRows.Close()
}

const skillRunSelect = `
	SELECT
		sr.id::text,sr.user_id::text,sr.skill_name,sr.skill_version,
		sr.execution_mode,sr.status,sr.current_state,sr.risk_level,
		sr.requires_confirmation,sr.input_json,sr.output_json,
		COALESCE(sr.conversation_id::text,''),COALESCE(sr.origin_message_id::text,''),sr.create_key,
		COALESCE(sr.confirmation_key,''),sr.last_action,
		COALESCE(sr.last_action_key,''),sr.attempt,sr.max_steps,sr.timeout_ms,
		sr.max_input_bytes,sr.max_cost_micros,sr.error_code,sr.error_message,
		sr.revision,sr.available_at,COALESCE(sr.worker_id,''),
		sr.lease_expires_at,sr.created_at,sr.updated_at,sr.completed_at
	FROM app.skill_runs sr`

const skillStepSelect = `
	SELECT
		srs.id::text,srs.sequence_no,srs.state,srs.status,srs.tool_name,
		srs.input_json,srs.output_json,srs.error_code,srs.error_message,
		srs.started_at,srs.completed_at
	FROM app.skill_run_steps srs`

const skillFileSelect = `
	SELECT
		gf.id::text,gf.run_id::text,gf.display_name,gf.media_type,
		gf.size_bytes,gf.sha256,gf.storage_key,gf.created_at
	FROM app.generated_files gf`

func scanPostgresSkillRun(row rowScanner) (skill.Run, error) {
	var run skill.Run
	var input, output []byte
	var completed, leaseExpires sql.NullTime
	err := row.Scan(
		&run.ID, &run.UserID, &run.SkillName, &run.SkillVersion,
		&run.ExecutionMode, &run.Status, &run.CurrentState, &run.RiskLevel,
		&run.RequiresConfirmation, &input, &output, &run.ConversationID,
		&run.OriginMessageID, &run.CreateKey,
		&run.ConfirmationKey, &run.LastAction, &run.LastActionKey, &run.Attempt,
		&run.MaxSteps, &run.TimeoutMS, &run.MaxInputBytes, &run.MaxCostMicros,
		&run.ErrorCode, &run.ErrorMessage, &run.Revision, &run.AvailableAt,
		&run.WorkerID, &leaseExpires, &run.CreatedAt, &run.UpdatedAt, &completed,
	)
	run.Input = append([]byte(nil), input...)
	run.Output = append([]byte(nil), output...)
	if completed.Valid {
		run.CompletedAt = &completed.Time
	}
	if leaseExpires.Valid {
		run.LeaseExpiresAt = &leaseExpires.Time
	}
	return run, err
}

func appendPostgresSkillRetryDelivery(ctx context.Context, tx *sql.Tx, run skill.Run) error {
	if strings.TrimSpace(run.DeliveryContent) == "" || run.Attempt < 2 ||
		run.ConversationID == "" || run.OriginMessageID == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM app.skill_run_deliveries
			WHERE run_id=$1 AND attempt=$2
		)`, run.ID, run.Attempt).Scan(&exists); err != nil || exists {
		return err
	}
	var owner, conversationStatus string
	if err := tx.QueryRowContext(ctx, `
		SELECT user_id::text,status
		FROM app.conversations
		WHERE id=$1
		FOR UPDATE`, run.ConversationID).Scan(&owner, &conversationStatus); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if owner != run.UserID || conversationStatus != "active" {
		return nil
	}
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT sequence_no
		FROM app.messages
		WHERE id=$1 AND conversation_id=$2 AND user_id=$3 AND role='user'`,
		run.OriginMessageID, run.ConversationID, run.UserID,
	).Scan(&sequence); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var bubble int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(bubble_no),0)+1
		FROM app.messages
		WHERE conversation_id=$1 AND sequence_no=$2`,
		run.ConversationID, sequence+1,
	).Scan(&bubble); err != nil {
		return err
	}
	messageID, err := id.New()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO app.messages (
			id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,
			reply_to_id,created_at,completed_at
		) VALUES ($1,$2,$3,'assistant',$4,$5,$6,'completed',$7,$8,$8)`,
		messageID, run.ConversationID, run.UserID, sequence+1, bubble,
		run.DeliveryContent, run.OriginMessageID, run.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.skill_run_deliveries (run_id,attempt,message_id,created_at)
		VALUES ($1,$2,$3,$4)`, run.ID, run.Attempt, messageID, run.UpdatedAt); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE app.conversations SET last_message_at=$1,updated_at=$1
		WHERE id=$2 AND user_id=$3`, run.UpdatedAt, run.ConversationID, run.UserID)
	return err
}

func scanPostgresSkillStep(row rowScanner) (skill.Step, error) {
	var step skill.Step
	var input, output []byte
	var completed sql.NullTime
	err := row.Scan(
		&step.ID, &step.Sequence, &step.State, &step.Status, &step.ToolName,
		&input, &output, &step.ErrorCode, &step.ErrorMessage, &step.StartedAt,
		&completed,
	)
	step.Input = append([]byte(nil), input...)
	step.Output = append([]byte(nil), output...)
	if completed.Valid {
		step.CompletedAt = &completed.Time
	}
	return step, err
}

func scanPostgresSkillFile(row rowScanner) (skill.GeneratedFile, error) {
	var file skill.GeneratedFile
	err := row.Scan(
		&file.ID, &file.RunID, &file.Name, &file.MediaType, &file.SizeBytes,
		&file.SHA256, &file.StorageKey, &file.CreatedAt,
	)
	return file, err
}
