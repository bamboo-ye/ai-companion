package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

var _ agent.Store = (*Store)(nil)

func (s *Store) CreateAgentRun(ctx context.Context, item agent.Run) (agent.Run, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent.runs (
			id,thread_id,user_id,conversation_id,character_id,module_key,
			graph_name,graph_version,agent_definition_key,agent_definition_version_id,
			agent_definition_version,agent_definition_revision,agent_definition_fingerprint,
			agent_definition_model_profile,agent_definition_snapshot,
			model_profile_key,model_profile_version_id,
			model_profile_revision,model_profile_config_version,model_profile_fingerprint,
			model_profile_snapshot,status,idempotency_key,input,available_at,
			deadline_at,revision,created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,'')::uuid,
			NULLIF($11,0),NULLIF($12,0),NULLIF($13,''),NULLIF($14,''),$15,
			NULLIF($16,''),NULLIF($17,'')::uuid,NULLIF($18,0),NULLIF($19,''),
			NULLIF($20,''),$21,$22,NULLIF($23,''),$24,$25,$26,$27,$28,$29
		)
		ON CONFLICT DO NOTHING`,
		item.ID, item.ThreadID, item.UserID, item.ConversationID, item.CharacterID, item.Module,
		item.GraphName, item.GraphVersion, item.AgentDefinitionKey, item.AgentDefinitionVersionID,
		item.AgentDefinitionVersion, item.AgentDefinitionRevision, item.AgentDefinitionFingerprint,
		item.AgentDefinitionModelProfile, item.AgentDefinitionSnapshot,
		item.ModelProfileKey, item.ModelProfileVersionID,
		item.ModelProfileRevision, item.ModelProfileConfigVersion, item.ModelProfileFingerprint,
		item.ModelProfileSnapshot, item.Status, item.IdempotencyKey, item.Input,
		item.AvailableAt, item.DeadlineAt, item.Revision, item.CreatedAt, item.UpdatedAt,
	)
	if err != nil {
		return agent.Run{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return agent.Run{}, false, err
	}
	if affected == 0 {
		var existing agent.Run
		query := `SELECT ` + agentRunColumns + ` FROM agent.runs WHERE id=$1`
		args := []any{item.ID}
		if item.IdempotencyKey != "" {
			query = `SELECT ` + agentRunColumns + `
				FROM agent.runs WHERE user_id=$1 AND idempotency_key=$2`
			args = []any{item.UserID, item.IdempotencyKey}
		}
		if err = scanAgentRun(tx.QueryRowContext(ctx, query, args...), &existing); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return agent.Run{}, false, agent.ErrConflict
			}
			return agent.Run{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return agent.Run{}, false, err
		}
		return existing, false, nil
	}
	if _, err = appendAgentEvent(ctx, tx, item.ID, "accepted", map[string]any{
		"graph_name": item.GraphName, "graph_version": item.GraphVersion,
		"agent_definition_version_id":  item.AgentDefinitionVersionID,
		"agent_definition_fingerprint": item.AgentDefinitionFingerprint,
		"model_profile_version_id":     item.ModelProfileVersionID,
		"model_profile_fingerprint":    item.ModelProfileFingerprint,
	}); err != nil {
		return agent.Run{}, false, err
	}
	if err = appendAgentRunRequested(ctx, tx, item); err != nil {
		return agent.Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, false, err
	}
	return item, true, nil
}

func (s *Store) GetAgentRun(ctx context.Context, runID string) (agent.Run, error) {
	var item agent.Run
	err := scanAgentRun(s.db.QueryRowContext(ctx,
		`SELECT `+agentRunColumns+` FROM agent.runs WHERE id=$1`,
		runID,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrNotFound
	}
	return item, err
}

func (s *Store) GetActiveAgentRun(ctx context.Context, userID, conversationID string) (agent.Run, error) {
	var item agent.Run
	err := scanAgentRun(s.db.QueryRowContext(ctx, `
		SELECT `+agentRunColumns+`
		FROM agent.runs
		WHERE user_id=$1 AND conversation_id=$2
			AND status IN ('accepted','queued','running','waiting_approval','waiting_tool','cancel_requested')
		ORDER BY created_at DESC
		LIMIT 1`,
		userID, conversationID,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrNotFound
	}
	return item, err
}

func (s *Store) ClaimAgentRun(ctx context.Context, runID, owner string, now time.Time, lease time.Duration) (agent.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	leaseExpiresAt := now.Add(lease)
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		UPDATE agent.runs
		SET status='running',lease_owner=$2,lease_expires_at=$3,
			revision=revision+1,updated_at=$4,error_code=NULL,error_message=NULL
		WHERE id=$1 AND available_at<=$4 AND deadline_at>$4
			AND (
				status IN ('accepted','queued','waiting_tool')
				OR (status='running' AND lease_expires_at<=$4)
			)
		RETURNING `+agentRunColumns,
		runID, owner, leaseExpiresAt, now,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrConflict
	}
	if err != nil {
		return agent.Run{}, err
	}
	if _, err = appendAgentEvent(ctx, tx, runID, "running", map[string]any{
		"lease_owner": owner, "revision": item.Revision,
	}); err != nil {
		return agent.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return item, nil
}

func (s *Store) ClaimNextAgentRun(ctx context.Context, owner string, now time.Time, lease time.Duration) (agent.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	leaseExpiresAt := now.Add(lease)
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM agent.runs
			WHERE available_at<=$3 AND deadline_at>$3
				AND (
					status IN ('accepted','queued','waiting_tool')
					OR (status='running' AND lease_expires_at<=$3)
				)
			ORDER BY available_at,created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE agent.runs AS r
		SET status='running',lease_owner=$1,lease_expires_at=$2,
			revision=r.revision+1,updated_at=$3,error_code=NULL,error_message=NULL
		WHERE r.id=(SELECT id FROM candidate)
		RETURNING `+agentRunColumns,
		owner, leaseExpiresAt, now,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrNotFound
	}
	if err != nil {
		return agent.Run{}, err
	}
	if _, err = appendAgentEvent(ctx, tx, item.ID, "running", map[string]any{
		"lease_owner": owner, "revision": item.Revision, "source": "reconciler",
	}); err != nil {
		return agent.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return item, nil
}

func (s *Store) DeferAgentRun(ctx context.Context, runID, owner string, revision int, availableAt time.Time, reason string, now time.Time) (agent.Run, error) {
	return s.transitionAgentRun(ctx, runID, owner, revision, "queued", now, map[string]any{
		"available_at": availableAt, "reason": reason, "retry_kind": "execution",
	}, `
		UPDATE agent.runs
		SET status='queued',available_at=$4,lease_owner=NULL,lease_expires_at=NULL,
			revision=revision+1,updated_at=$5,error_code=NULL,error_message=NULL
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND revision=$3
			AND lease_expires_at>$5 AND deadline_at>$5
		RETURNING `+agentRunColumns,
		availableAt, now,
	)
}

func (s *Store) CompleteAgentRun(ctx context.Context, runID, owner string, revision int, output json.RawMessage, now time.Time) (agent.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		UPDATE agent.runs
		SET status='completed',output=$4,resume_resolution=NULL,
			lease_owner=NULL,lease_expires_at=NULL,
			revision=revision+1,updated_at=$5,completed_at=$5,
			error_code=NULL,error_message=NULL
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND revision=$3
			AND lease_expires_at>$5 AND deadline_at>$5
		RETURNING `+agentRunColumns,
		runID, owner, revision, output, now,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrConflict
	}
	if err != nil {
		return agent.Run{}, err
	}
	if _, err = appendAgentEvent(ctx, tx, runID, "completed", map[string]any{
		"output": json.RawMessage(output), "revision": item.Revision, "at": now,
	}); err != nil {
		return agent.Run{}, err
	}
	if err = appendAgentAssistantMessage(ctx, tx, item, output, now); err != nil {
		return agent.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return item, nil
}

func (s *Store) PauseAgentRun(ctx context.Context, runID, owner string, revision int, output json.RawMessage, now time.Time) (agent.Run, error) {
	interruptRequest, err := approvalInterrupt(output)
	if err != nil {
		return agent.Run{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		UPDATE agent.runs
		SET status='waiting_approval',output=$4,
			lease_owner=NULL,lease_expires_at=NULL,
			revision=revision+1,updated_at=$5,error_code=NULL,error_message=NULL
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND revision=$3
			AND lease_expires_at>$5 AND deadline_at>$5
		RETURNING `+agentRunColumns,
		runID, owner, revision, output, now,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrConflict
	}
	if err != nil {
		return agent.Run{}, err
	}
	interruptID, err := id.New()
	if err != nil {
		return agent.Run{}, err
	}
	interruptKey := fmt.Sprintf("tool_approval:%d", item.Revision)
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO agent.interrupts (
			id,run_id,interrupt_key,interrupt_type,status,request,created_at
		) VALUES ($1,$2,$3,'approval','pending',$4,$5)`,
		interruptID, runID, interruptKey, interruptRequest, now,
	); err != nil {
		return agent.Run{}, err
	}
	if _, err = appendAgentEvent(ctx, tx, runID, "waiting_approval", map[string]any{
		"interrupt_key": interruptKey, "request": json.RawMessage(interruptRequest),
		"revision": item.Revision, "at": now,
	}); err != nil {
		return agent.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return item, nil
}

func (s *Store) SuspendAgentRunForTool(ctx context.Context, runID, owner string, revision int, output json.RawMessage, availableAt, now time.Time) (agent.Run, error) {
	request, metadata, err := toolWaitInterrupt(output)
	if err != nil {
		return agent.Run{}, err
	}
	resolution, err := json.Marshal(map[string]string{
		"type":         "tool_poll",
		"task_id":      metadata.TaskID,
		"interrupt_id": metadata.InterruptID,
	})
	if err != nil {
		return agent.Run{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		UPDATE agent.runs
		SET status='waiting_tool',output=$4,resume_resolution=$5,available_at=$6,
			lease_owner=NULL,lease_expires_at=NULL,
			revision=revision+1,updated_at=$7,error_code=NULL,error_message=NULL
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND revision=$3
			AND lease_expires_at>$7 AND deadline_at>$7
		RETURNING `+agentRunColumns,
		runID, owner, revision, output, resolution, availableAt, now,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrConflict
	}
	if err != nil {
		return agent.Run{}, err
	}
	if _, err = appendAgentEvent(ctx, tx, runID, "waiting_tool", map[string]any{
		"request": json.RawMessage(request), "task_id": metadata.TaskID,
		"interrupt_id": metadata.InterruptID, "available_at": availableAt,
		"revision": item.Revision, "at": now,
	}); err != nil {
		return agent.Run{}, err
	}
	if err = appendAgentResumeRequested(ctx, tx, item, availableAt); err != nil {
		return agent.Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return item, nil
}

func (s *Store) WakeAgentRunsForTool(ctx context.Context, taskID, userID string, now time.Time, limit int) ([]agent.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id::text
		FROM agent.runs
		WHERE user_id=$1
			AND status='waiting_tool'
			AND deadline_at>$2
			AND resume_resolution->>'type'='tool_poll'
			AND resume_resolution->>'task_id'=$3
		ORDER BY updated_at,id
		FOR UPDATE SKIP LOCKED
		LIMIT $4`,
		userID, now, taskID, limit,
	)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0)
	for rows.Next() {
		var runID string
		if err = rows.Scan(&runID); err != nil {
			rows.Close()
			return nil, err
		}
		runIDs = append(runIDs, runID)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	items := make([]agent.Run, 0, len(runIDs))
	for _, runID := range runIDs {
		var item agent.Run
		err = scanAgentRun(tx.QueryRowContext(ctx, `
			UPDATE agent.runs
			SET status='queued',available_at=$3,
				lease_owner=NULL,lease_expires_at=NULL,
				revision=revision+1,updated_at=$3,
				error_code=NULL,error_message=NULL
			WHERE id=$1 AND user_id=$2 AND status='waiting_tool'
			RETURNING `+agentRunColumns,
			runID, userID, now,
		), &item)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if _, err = appendAgentEvent(ctx, tx, item.ID, "tool_ready", map[string]any{
			"task_id": taskID, "revision": item.Revision, "at": now,
		}); err != nil {
			return nil, err
		}
		if err = appendAgentResumeRequested(ctx, tx, item, now); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) ResolveAgentRun(ctx context.Context, runID, userID string, approved bool, resolutionKey string, now time.Time) (agent.Run, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, false, err
	}
	defer tx.Rollback()
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		SELECT `+agentRunColumns+`
		FROM agent.runs
		WHERE id=$1 AND user_id=$2
		FOR UPDATE`,
		runID, userID,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, false, agent.ErrNotFound
	}
	if err != nil {
		return agent.Run{}, false, err
	}
	if item.Status != "waiting_approval" {
		var priorApproved bool
		err = tx.QueryRowContext(ctx, `
			SELECT COALESCE((resolution->>'approved')::boolean,false)
			FROM agent.interrupts
			WHERE run_id=$1 AND resolution_key=$2 AND status='resolved'
			ORDER BY resolved_at DESC
			LIMIT 1`,
			runID, resolutionKey,
		).Scan(&priorApproved)
		if err == nil && priorApproved == approved {
			if commitErr := tx.Commit(); commitErr != nil {
				return agent.Run{}, false, commitErr
			}
			return item, false, nil
		}
		return agent.Run{}, false, agent.ErrConflict
	}
	var interruptID string
	var interruptRequest json.RawMessage
	err = tx.QueryRowContext(ctx, `
		SELECT id::text,request
		FROM agent.interrupts
		WHERE run_id=$1 AND status='pending' AND interrupt_type='approval'
		ORDER BY created_at DESC
		FOR UPDATE
		LIMIT 1`,
		runID,
	).Scan(&interruptID, &interruptRequest)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, false, agent.ErrConflict
	}
	if err != nil {
		return agent.Run{}, false, err
	}
	var requestMetadata struct {
		InterruptID string `json:"interrupt_id"`
	}
	if err = json.Unmarshal(interruptRequest, &requestMetadata); err != nil {
		return agent.Run{}, false, err
	}
	resolutionValue := map[string]any{
		"type": "tool_approval", "approved": approved,
	}
	if requestMetadata.InterruptID != "" {
		resolutionValue["interrupt_id"] = requestMetadata.InterruptID
	}
	resolution, err := json.Marshal(resolutionValue)
	if err != nil {
		return agent.Run{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE agent.interrupts
		SET status='resolved',resolution=$1,resolution_key=$2,resolved_at=$3
		WHERE id=$4 AND status='pending'`,
		resolution, resolutionKey, now, interruptID,
	); err != nil {
		return agent.Run{}, false, err
	}
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		UPDATE agent.runs
		SET status='queued',resume_resolution=$3,available_at=$4,
			lease_owner=NULL,lease_expires_at=NULL,revision=revision+1,
			updated_at=$4,error_code=NULL,error_message=NULL
		WHERE id=$1 AND user_id=$2 AND status='waiting_approval'
			AND deadline_at>$4
		RETURNING `+agentRunColumns,
		runID, userID, resolution, now,
	), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, false, agent.ErrConflict
	}
	if err != nil {
		return agent.Run{}, false, err
	}
	if _, err = appendAgentEvent(ctx, tx, runID, "approval_resolved", map[string]any{
		"approved": approved, "revision": item.Revision, "at": now,
	}); err != nil {
		return agent.Run{}, false, err
	}
	if err = appendAgentResumeRequested(ctx, tx, item, now); err != nil {
		return agent.Run{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, false, err
	}
	return item, true, nil
}

func (s *Store) CancelAgentRun(ctx context.Context, runID, userID string, now time.Time) (agent.Run, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, false, err
	}
	defer tx.Rollback()
	var current agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		SELECT `+agentRunColumns+`
		FROM agent.runs
		WHERE id=$1 AND user_id=$2
		FOR UPDATE`,
		runID, userID,
	), &current)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, false, agent.ErrNotFound
	}
	if err != nil {
		return agent.Run{}, false, err
	}
	if agent.IsTerminalStatus(current.Status) || current.Status == "cancel_requested" {
		if err = tx.Commit(); err != nil {
			return agent.Run{}, false, err
		}
		return current, false, nil
	}

	nextStatus := "cancelled"
	if current.Status == "running" {
		nextStatus = "cancel_requested"
	}
	var item agent.Run
	err = scanAgentRun(tx.QueryRowContext(ctx, `
		UPDATE agent.runs
		SET status=$3,
			lease_owner=CASE WHEN $3='cancel_requested' THEN lease_owner ELSE NULL END,
			lease_expires_at=CASE WHEN $3='cancel_requested' THEN lease_expires_at ELSE NULL END,
			revision=revision+1,updated_at=$4,
			completed_at=CASE WHEN $3='cancelled' THEN $4 ELSE completed_at END,
			error_code=CASE WHEN $3='cancelled' THEN 'cancelled' ELSE 'cancel_requested' END,
			error_message='Agent run cancelled by user'
		WHERE id=$1 AND user_id=$2
		RETURNING `+agentRunColumns,
		runID, userID, nextStatus, now,
	), &item)
	if err != nil {
		return agent.Run{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		UPDATE agent.interrupts
		SET status='cancelled',resolved_at=$2
		WHERE run_id=$1 AND status='pending'`,
		runID, now,
	); err != nil {
		return agent.Run{}, false, err
	}
	if _, err = appendAgentEvent(ctx, tx, runID, nextStatus, map[string]any{
		"source": "user", "revision": item.Revision, "at": now,
	}); err != nil {
		return agent.Run{}, false, err
	}
	if nextStatus == "cancelled" {
		if err = appendAgentTerminalMessage(ctx, tx, item, now); err != nil {
			return agent.Run{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, false, err
	}
	return item, true, nil
}

func (s *Store) FinalizeAgentRunCancellation(ctx context.Context, runID, owner string, revision int, now time.Time) (agent.Run, error) {
	return s.transitionAgentRun(ctx, runID, owner, revision, "cancelled", now, map[string]any{
		"source": "worker",
	}, `
		UPDATE agent.runs
		SET status='cancelled',lease_owner=NULL,lease_expires_at=NULL,
			revision=revision+1,updated_at=$4,completed_at=$4,
			error_code='cancelled',error_message='Agent run cancelled by user'
		WHERE id=$1 AND status='cancel_requested'
			AND lease_owner=$2 AND revision=$3
		RETURNING `+agentRunColumns,
		now,
	)
}

func (s *Store) TimeoutAgentRun(ctx context.Context, runID, owner string, revision int, code, message string, now time.Time) (agent.Run, error) {
	return s.transitionAgentRun(ctx, runID, owner, revision, "timed_out", now, map[string]any{
		"source": "worker", "error_code": code,
	}, `
		UPDATE agent.runs
		SET status='timed_out',lease_owner=NULL,lease_expires_at=NULL,
			revision=revision+1,updated_at=$6,completed_at=$6,
			error_code=$4,error_message=$5
		WHERE id=$1 AND status='running'
			AND lease_owner=$2 AND revision=$3
			AND (deadline_at<=$6 OR $4='execution_retry_deadline_exhausted')
		RETURNING `+agentRunColumns,
		code, message, now,
	)
}

func (s *Store) ExpireAgentRuns(ctx context.Context, now time.Time, limit int) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM agent.runs
			WHERE deadline_at<=$1
				AND status IN (
					'accepted','queued','running','waiting_approval',
					'waiting_tool','cancel_requested'
				)
			ORDER BY deadline_at,created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE agent.runs AS r
		SET status=CASE
				WHEN r.status='cancel_requested' THEN 'cancelled'
				ELSE 'timed_out'
			END,
			lease_owner=NULL,lease_expires_at=NULL,
			revision=r.revision+1,updated_at=$1,completed_at=$1,
			error_code=CASE
				WHEN r.status='cancel_requested' THEN 'cancelled'
				ELSE 'run_timeout'
			END,
			error_message=CASE
				WHEN r.status='cancel_requested' THEN 'Agent run cancelled by user'
				ELSE 'Agent run exceeded its total deadline'
			END
		WHERE r.id IN (SELECT id FROM candidates)
		RETURNING r.id::text,r.revision,r.status`,
		now, limit,
	)
	if err != nil {
		return 0, err
	}
	type expiredRun struct {
		id       string
		revision int
		status   string
	}
	expired := make([]expiredRun, 0)
	for rows.Next() {
		var item expiredRun
		if err = rows.Scan(&item.id, &item.revision, &item.status); err != nil {
			rows.Close()
			return 0, err
		}
		expired = append(expired, item)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, item := range expired {
		if _, err = tx.ExecContext(ctx, `
			UPDATE agent.interrupts
			SET status='expired',resolved_at=$2
			WHERE run_id=$1 AND status='pending'`,
			item.id, now,
		); err != nil {
			return 0, err
		}
		if _, err = appendAgentEvent(ctx, tx, item.id, item.status, map[string]any{
			"source": "reconciler", "revision": item.revision, "at": now,
		}); err != nil {
			return 0, err
		}
		var expiredItem agent.Run
		if err = scanAgentRun(tx.QueryRowContext(ctx, `
			SELECT `+agentRunColumns+`
			FROM agent.runs
			WHERE id=$1`, item.id), &expiredItem); err != nil {
			return 0, err
		}
		if err = appendAgentTerminalMessage(ctx, tx, expiredItem, now); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(expired), nil
}

func (s *Store) FailAgentRun(ctx context.Context, runID, owner string, revision int, code, message string, now time.Time) (agent.Run, error) {
	return s.transitionAgentRun(ctx, runID, owner, revision, "failed", now, map[string]any{
		"error_code": code,
	}, `
		UPDATE agent.runs
		SET status='failed',error_code=$4,error_message=$5,
			lease_owner=NULL,lease_expires_at=NULL,revision=revision+1,
			updated_at=$6,completed_at=$6
		WHERE id=$1 AND status='running' AND lease_owner=$2 AND revision=$3
			AND lease_expires_at>$6 AND deadline_at>$6
		RETURNING `+agentRunColumns,
		code, message, now,
	)
}

func (s *Store) ListAgentRunEvents(ctx context.Context, runID string, after int64, limit int) ([]agent.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,run_id::text,sequence_no,event_type,payload,created_at
		FROM agent.run_events
		WHERE run_id=$1 AND sequence_no>$2
		ORDER BY sequence_no
		LIMIT $3`,
		runID, after, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]agent.Event, 0)
	for rows.Next() {
		var item agent.Event
		if err = rows.Scan(&item.ID, &item.RunID, &item.Sequence, &item.Type, &item.Payload, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const agentRunColumns = `
	id::text,thread_id,user_id::text,conversation_id::text,character_id::text,
	module_key,graph_name,graph_version,COALESCE(agent_definition_key,''),
	COALESCE(agent_definition_version_id::text,''),COALESCE(agent_definition_version,0),
	COALESCE(agent_definition_revision,0),COALESCE(agent_definition_fingerprint,''),
	COALESCE(agent_definition_model_profile,''),
	COALESCE(agent_definition_snapshot,'null'::jsonb),COALESCE(model_profile_key,''),
	COALESCE(model_profile_version_id::text,''),COALESCE(model_profile_revision,0),
	COALESCE(model_profile_config_version,''),COALESCE(model_profile_fingerprint,''),
	COALESCE(model_profile_snapshot,'null'::jsonb),status,COALESCE(idempotency_key,''),
	input,COALESCE(output,'null'::jsonb),COALESCE(resume_resolution,'null'::jsonb),
	COALESCE(error_code,''),
	COALESCE(error_message,''),available_at,deadline_at,COALESCE(lease_owner,''),
	lease_expires_at,revision,created_at,updated_at,completed_at`

type agentRunScanner interface {
	Scan(...any) error
}

func scanAgentRun(row agentRunScanner, item *agent.Run) error {
	return row.Scan(
		&item.ID, &item.ThreadID, &item.UserID, &item.ConversationID, &item.CharacterID,
		&item.Module, &item.GraphName, &item.GraphVersion, &item.AgentDefinitionKey,
		&item.AgentDefinitionVersionID, &item.AgentDefinitionVersion,
		&item.AgentDefinitionRevision, &item.AgentDefinitionFingerprint,
		&item.AgentDefinitionModelProfile, &item.AgentDefinitionSnapshot, &item.ModelProfileKey,
		&item.ModelProfileVersionID, &item.ModelProfileRevision, &item.ModelProfileConfigVersion,
		&item.ModelProfileFingerprint, &item.ModelProfileSnapshot, &item.Status,
		&item.IdempotencyKey, &item.Input, &item.Output, &item.Resume, &item.ErrorCode,
		&item.ErrorMessage, &item.AvailableAt, &item.DeadlineAt, &item.LeaseOwner,
		&item.LeaseExpiresAt, &item.Revision, &item.CreatedAt, &item.UpdatedAt,
		&item.CompletedAt,
	)
}

func appendAgentEvent(ctx context.Context, tx *sql.Tx, runID, eventType string, payload any) (agent.Event, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return agent.Event{}, err
	}
	var item agent.Event
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent.run_events (run_id,sequence_no,event_type,payload)
		SELECT $1,COALESCE(MAX(sequence_no),0)+1,$2,$3
		FROM agent.run_events
		WHERE run_id=$1
		RETURNING id,run_id::text,sequence_no,event_type,payload,created_at`,
		runID, eventType, data,
	).Scan(&item.ID, &item.RunID, &item.Sequence, &item.Type, &item.Payload, &item.CreatedAt)
	if err != nil {
		return agent.Event{}, fmt.Errorf("append agent event: %w", err)
	}
	return item, nil
}

func appendAgentRunRequested(ctx context.Context, tx *sql.Tx, item agent.Run) error {
	return appendAgentDispatchEvent(ctx, tx, item.ID, item.ThreadID, "agent.run.requested.v1", item.CreatedAt)
}

func appendAgentResumeRequested(ctx context.Context, tx *sql.Tx, item agent.Run, occurredAt time.Time) error {
	return appendAgentDispatchEvent(ctx, tx, item.ID, item.ThreadID, "agent.run.resume.requested.v1", occurredAt)
}

func appendAgentDispatchEvent(ctx context.Context, tx *sql.Tx, runID, threadID, eventType string, occurredAt time.Time) error {
	eventID, err := id.New()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{
		"run_id": runID, "thread_id": threadID,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO eventing.outbox_events (
			id,aggregate_type,aggregate_id,event_type,event_version,payload,
			occurred_at,available_at,trace_id,traceparent,tracestate
		) VALUES ($1,'agent_run',$2,$3,1,$4,$5,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''))`,
		eventID, runID, eventType, payload, occurredAt, outboxTraceID(ctx), outboxTraceParent(ctx), outboxTraceState(ctx),
	)
	return err
}

func approvalInterrupt(output json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Interrupts []json.RawMessage `json:"interrupts"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil || len(envelope.Interrupts) != 1 {
		return nil, fmt.Errorf("%w: waiting approval requires exactly one interrupt", agent.ErrValidation)
	}
	var metadata struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(envelope.Interrupts[0], &metadata); err != nil ||
		metadata.Type != "tool_approval" {
		return nil, fmt.Errorf("%w: unsupported approval interrupt", agent.ErrValidation)
	}
	return envelope.Interrupts[0], nil
}

type toolWaitMetadata struct {
	TaskID      string `json:"task_id"`
	InterruptID string `json:"interrupt_id"`
}

func toolWaitInterrupt(output json.RawMessage) (json.RawMessage, toolWaitMetadata, error) {
	var envelope struct {
		Interrupts []json.RawMessage `json:"interrupts"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil || len(envelope.Interrupts) != 1 {
		return nil, toolWaitMetadata{}, fmt.Errorf("%w: waiting tool requires exactly one interrupt", agent.ErrValidation)
	}
	var metadata struct {
		Type        string `json:"type"`
		TaskID      string `json:"task_id"`
		InterruptID string `json:"interrupt_id"`
	}
	if err := json.Unmarshal(envelope.Interrupts[0], &metadata); err != nil ||
		metadata.Type != "tool_wait" || strings.TrimSpace(metadata.TaskID) == "" ||
		strings.TrimSpace(metadata.InterruptID) == "" {
		return nil, toolWaitMetadata{}, fmt.Errorf("%w: unsupported tool wait interrupt", agent.ErrValidation)
	}
	return envelope.Interrupts[0], toolWaitMetadata{
		TaskID: strings.TrimSpace(metadata.TaskID), InterruptID: strings.TrimSpace(metadata.InterruptID),
	}, nil
}

func appendAgentAssistantMessage(ctx context.Context, tx *sql.Tx, item agent.Run, output json.RawMessage, now time.Time) error {
	var result struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("decode Agent output: %w", err)
	}
	result.Response = strings.TrimSpace(result.Response)
	if result.Response == "" {
		return fmt.Errorf("%w: completed Agent output requires a response", agent.ErrValidation)
	}
	return appendAgentConversationMessage(
		ctx, tx, item,
		result.Response+"\n"+agentRunMarker(item.ID, "completed", item.Revision),
		now,
	)
}

func appendAgentTerminalMessage(ctx context.Context, tx *sql.Tx, item agent.Run, now time.Time) error {
	response := "这次处理没有完成，原对话已保留。你可以点击“重试”再次执行。"
	switch item.Status {
	case "cancelled":
		response = "这次操作已取消，原对话已保留。需要时可以点击“重试”。"
	case "timed_out":
		response = "这次处理超时，原对话已保留。你可以点击“重试”再次执行。"
	case "failed":
	default:
		return nil
	}
	return appendAgentConversationMessage(
		ctx, tx, item,
		response+"\n"+agentRunMarker(item.ID, item.Status, item.Revision),
		now,
	)
}

func appendAgentConversationMessage(ctx context.Context, tx *sql.Tx, item agent.Run, content string, now time.Time) error {
	var input struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(item.Input, &input); err != nil || strings.TrimSpace(input.MessageID) == "" {
		return fmt.Errorf("%w: Agent input requires message_id", agent.ErrValidation)
	}
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `
		SELECT sequence_no
		FROM app.messages
		WHERE id=$1 AND conversation_id=$2 AND user_id=$3 AND role='user'
		FOR UPDATE`,
		input.MessageID, item.ConversationID, item.UserID,
	).Scan(&sequence); err != nil {
		return fmt.Errorf("load Agent user message: %w", err)
	}
	var bubble int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(bubble_no),0)+1
		FROM app.messages
		WHERE conversation_id=$1 AND sequence_no=$2`,
		item.ConversationID, sequence+1,
	).Scan(&bubble); err != nil {
		return fmt.Errorf("select Agent assistant bubble: %w", err)
	}
	messageID, err := id.New()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO app.messages (
			id,conversation_id,user_id,role,sequence_no,bubble_no,content,status,
			reply_to_id,created_at,completed_at
		) VALUES ($1,$2,$3,'assistant',$4,$5,$6,'completed',$7,$8,$8)`,
		messageID, item.ConversationID, item.UserID, sequence+1,
		bubble, strings.TrimSpace(content), input.MessageID, now,
	); err != nil {
		return fmt.Errorf("append Agent assistant message: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE app.conversations
		SET last_message_at=$1,updated_at=$1
		WHERE id=$2 AND user_id=$3 AND status='active'`,
		now, item.ConversationID, item.UserID,
	)
	return err
}

func agentRunMarker(runID, status string, revision int) string {
	return fmt.Sprintf("<!--ai-agent-run:%s|%s|%d-->", runID, status, revision)
}

func (s *Store) transitionAgentRun(ctx context.Context, runID, owner string, revision int, eventType string, now time.Time, eventPayload map[string]any, query string, args ...any) (agent.Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agent.Run{}, err
	}
	defer tx.Rollback()
	queryArgs := []any{runID, owner, revision}
	queryArgs = append(queryArgs, args...)
	var item agent.Run
	if err = scanAgentRun(tx.QueryRowContext(ctx, query, queryArgs...), &item); errors.Is(err, sql.ErrNoRows) {
		return agent.Run{}, agent.ErrConflict
	} else if err != nil {
		return agent.Run{}, err
	}
	eventPayload["revision"] = item.Revision
	eventPayload["at"] = now
	if _, err = appendAgentEvent(ctx, tx, runID, eventType, eventPayload); err != nil {
		return agent.Run{}, err
	}
	if eventType == "failed" || eventType == "timed_out" || eventType == "cancelled" {
		if err = appendAgentTerminalMessage(ctx, tx, item, now); err != nil {
			return agent.Run{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return agent.Run{}, err
	}
	return item, nil
}
