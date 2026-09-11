package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

func (s *Store) CreateAgentRollout(ctx context.Context, rollout controlplane.AgentRollout) (controlplane.AgentRollout, error) {
	policy, err := json.Marshal(rollout.Policy)
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	summary, err := json.Marshal(rollout.Summary)
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "agent-rollout:"+rollout.Environment); err != nil {
		return controlplane.AgentRollout{}, err
	}
	err = scanAgentRollout(tx.QueryRowContext(ctx, `
		INSERT INTO ops.agent_rollouts (
			id,environment,agent_key,agent_version_id,agent_version,agent_fingerprint,
			baseline_version_id,baseline_version,baseline_fingerprint,traffic_percent,
			routing_revision,policy,status,decision,summary,violations,revision,
			created_by,updated_by,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'[]'::jsonb,1,$16,$16,$17,$17)
		RETURNING `+agentRolloutColumns,
		rollout.ID, rollout.Environment, rollout.AgentKey, rollout.AgentVersionID,
		rollout.AgentVersion, rollout.AgentFingerprint, rollout.BaselineVersionID,
		rollout.BaselineVersion, rollout.BaselineFingerprint, rollout.TrafficPercent,
		rollout.RoutingRevision, policy, rollout.Status, rollout.Decision, summary,
		rollout.CreatedBy, rollout.CreatedAt,
	), &rollout)
	if err != nil {
		if isUniqueViolation(err) {
			return controlplane.AgentRollout{}, controlplane.ErrConflict
		}
		return controlplane.AgentRollout{}, err
	}
	if err = insertAgentRolloutAudit(ctx, tx, "agent.rollout.start", rollout, rollout.CreatedBy, "start controlled Agent rollout", rollout.CreatedAt); err != nil {
		return controlplane.AgentRollout{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.AgentRollout{}, err
	}
	return rollout, nil
}

func (s *Store) GetAgentRollout(ctx context.Context, rolloutID string) (controlplane.AgentRollout, error) {
	var rollout controlplane.AgentRollout
	err := scanAgentRollout(s.db.QueryRowContext(ctx, `SELECT `+agentRolloutColumns+` FROM ops.agent_rollouts WHERE id=$1`, rolloutID), &rollout)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentRollout{}, controlplane.ErrNotFound
	}
	return rollout, err
}

func (s *Store) ListAgentRollouts(ctx context.Context, agentKey string, limit int) ([]controlplane.AgentRollout, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentRolloutColumns+`
		FROM ops.agent_rollouts WHERE agent_key=$1 ORDER BY created_at DESC LIMIT $2`, agentKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.AgentRollout, 0)
	for rows.Next() {
		var rollout controlplane.AgentRollout
		if err = scanAgentRollout(rows, &rollout); err != nil {
			return nil, err
		}
		items = append(items, rollout)
	}
	return items, rows.Err()
}

func (s *Store) ActiveAgentRollout(ctx context.Context, environment string) (controlplane.AgentRollout, error) {
	var rollout controlplane.AgentRollout
	err := scanAgentRollout(s.db.QueryRowContext(ctx, `SELECT `+agentRolloutColumns+`
		FROM ops.agent_rollouts WHERE environment=$1 AND status IN ('running','ready')
		ORDER BY created_at DESC LIMIT 1`, environment), &rollout)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentRollout{}, controlplane.ErrNotFound
	}
	return rollout, err
}

func (s *Store) LatestAgentRolloutForVersion(ctx context.Context, versionID, fingerprint string) (controlplane.AgentRollout, error) {
	var rollout controlplane.AgentRollout
	err := scanAgentRollout(s.db.QueryRowContext(ctx, `SELECT `+agentRolloutColumns+`
		FROM ops.agent_rollouts WHERE agent_version_id=$1 AND agent_fingerprint=$2
		ORDER BY created_at DESC LIMIT 1`, versionID, fingerprint), &rollout)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentRollout{}, controlplane.ErrNotFound
	}
	return rollout, err
}

func (s *Store) MeasureAgentRollout(
	ctx context.Context,
	rollout controlplane.AgentRollout,
	since, until time.Time,
) (controlplane.AgentRolloutMeasurements, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH scoped AS (
			SELECT agent_definition_version_id::text AS version_id,status,COALESCE(error_code,'') AS error_code,
				COALESCE(output->>'outcome','') AS outcome,
				GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(completed_at,updated_at)-created_at))*1000)::float8 AS duration_ms,
				COALESCE((
					SELECT SUM(CASE
						WHEN jsonb_typeof(call->'cost_micros')='number' THEN (call->>'cost_micros')::numeric
						WHEN (call->>'cost_micros') ~ '^[0-9]+$' THEN (call->>'cost_micros')::numeric
						ELSE 0 END)
					FROM jsonb_array_elements(CASE WHEN jsonb_typeof(output #> '{model,calls}')='array'
						THEN output #> '{model,calls}' ELSE '[]'::jsonb END) call
				),0)::float8 AS cost_micros
			FROM agent.runs
			WHERE agent_definition_version_id IN ($1::uuid,$2::uuid)
				AND created_at >= $3 AND created_at <= $4
		), aggregate AS (
			SELECT version_id,
				COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled'))::int AS sample_size,
				COUNT(*) FILTER (WHERE status='completed')::int AS completed,
				COUNT(*) FILTER (WHERE status IN ('failed','cancelled'))::int AS failed,
				COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled') AND (
					error_code IN ('response_quality_failed','artifact_quality_failed','email_quality_failed') OR
					outcome IN ('response_quality_failed','artifact_quality_failed','email_quality_failed')
				))::int AS quality_failures,
				COALESCE(percentile_cont(.95) WITHIN GROUP (ORDER BY duration_ms)
					FILTER (WHERE status IN ('completed','failed','cancelled')),0)::float8 AS p95_latency_ms,
				COALESCE(AVG(cost_micros) FILTER (WHERE status IN ('completed','failed','cancelled')),0)::float8 AS average_cost_micros
			FROM scoped GROUP BY version_id
		)
		SELECT version_id,sample_size,completed,failed,quality_failures,p95_latency_ms,average_cost_micros
		FROM aggregate`, rollout.AgentVersionID, rollout.BaselineVersionID, since, until)
	if err != nil {
		return controlplane.AgentRolloutMeasurements{}, err
	}
	defer rows.Close()
	var result controlplane.AgentRolloutMeasurements
	for rows.Next() {
		var versionID string
		var metrics controlplane.AgentRolloutMetrics
		if err = rows.Scan(&versionID, &metrics.SampleSize, &metrics.Completed, &metrics.Failed,
			&metrics.QualityFailures, &metrics.P95LatencyMS, &metrics.AverageCostMicros); err != nil {
			return controlplane.AgentRolloutMeasurements{}, err
		}
		if metrics.SampleSize > 0 {
			metrics.ErrorRate = float64(metrics.Failed) / float64(metrics.SampleSize)
			metrics.QualityFailureRate = float64(metrics.QualityFailures) / float64(metrics.SampleSize)
		}
		if versionID == rollout.AgentVersionID {
			result.Candidate = metrics
		} else if versionID == rollout.BaselineVersionID {
			result.Baseline = metrics
		}
	}
	return result, rows.Err()
}

func (s *Store) UpdateAgentRollout(
	ctx context.Context,
	rolloutID string,
	revision int,
	status, decision string,
	summary controlplane.AgentRolloutSummary,
	violations []controlplane.AgentRolloutViolation,
	actor, reason string,
	now time.Time,
) (controlplane.AgentRollout, error) {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	violationsJSON, err := json.Marshal(violations)
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	defer tx.Rollback()
	var rollout controlplane.AgentRollout
	err = scanAgentRollout(tx.QueryRowContext(ctx, `
		UPDATE ops.agent_rollouts SET status=$3,decision=$4,summary=$5,violations=$6,
			revision=revision+1,updated_by=$7,updated_at=$8::timestamptz,evaluated_at=$8::timestamptz,
			completed_at=CASE WHEN $3 IN ('paused','promoted','aborted') THEN $8::timestamptz ELSE NULL END
		WHERE id=$1 AND revision=$2 AND status IN ('running','ready')
		RETURNING `+agentRolloutColumns,
		rolloutID, revision, status, decision, summaryJSON, violationsJSON, actor, now,
	), &rollout)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentRollout{}, controlplane.ErrConflict
	}
	if err != nil {
		return controlplane.AgentRollout{}, err
	}
	action := "agent.rollout.evaluate"
	if status == controlplane.AgentRolloutStatusPaused {
		action = "agent.rollout.auto_pause"
	} else if status == controlplane.AgentRolloutStatusAborted {
		action = "agent.rollout.abort"
	}
	if err = insertAgentRolloutAudit(ctx, tx, action, rollout, actor, reason, now); err != nil {
		return controlplane.AgentRollout{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.AgentRollout{}, err
	}
	return rollout, nil
}

func (s *Store) PromoteAgentRollout(
	ctx context.Context,
	rolloutID string,
	revision int,
	environment, actor string,
	now time.Time,
) (controlplane.Version, controlplane.Deployment, controlplane.AgentRollout, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	defer tx.Rollback()
	var rollout controlplane.AgentRollout
	err = scanAgentRollout(tx.QueryRowContext(ctx, `SELECT `+agentRolloutColumns+` FROM ops.agent_rollouts WHERE id=$1 FOR UPDATE`, rolloutID), &rollout)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	if rollout.Revision != revision || rollout.Environment != environment ||
		rollout.Status != controlplane.AgentRolloutStatusReady || rollout.Decision != controlplane.AgentRolloutDecisionPass {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, controlplane.ErrConflict
	}
	var item controlplane.Version
	err = scanConfigVersion(tx.QueryRowContext(ctx, `SELECT `+configVersionColumns+` FROM ops.config_versions WHERE id=$1 FOR UPDATE`, rollout.AgentVersionID), &item)
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	if item.Kind != controlplane.KindAgentDefinition || item.Key != rollout.AgentKey ||
		item.Fingerprint != rollout.AgentFingerprint || item.Status != controlplane.StatusSubmitted ||
		actor == item.CreatedBy || actor == item.SubmittedBy {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, controlplane.ErrConflict
	}
	var currentID string
	var currentRevision int
	exactDeployment := true
	err = tx.QueryRowContext(ctx, `SELECT version_id::text,revision FROM ops.config_deployments
		WHERE environment=$1 AND config_kind=$2 AND config_key=$3 FOR UPDATE`,
		environment, item.Kind, item.Key).Scan(&currentID, &currentRevision)
	if errors.Is(err, sql.ErrNoRows) && environment != "default" {
		exactDeployment = false
		err = tx.QueryRowContext(ctx, `SELECT version_id::text,revision FROM ops.config_deployments
			WHERE environment='default' AND config_kind=$1 AND config_key=$2 FOR UPDATE`,
			item.Kind, item.Key).Scan(&currentID, new(int))
	}
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	if currentID != rollout.BaselineVersionID {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, controlplane.ErrConflict
	}
	if exactDeployment {
		if _, err = tx.ExecContext(ctx, `UPDATE ops.config_versions SET status='superseded',updated_at=$2 WHERE id=$1 AND status='published'`, currentID, now); err != nil {
			return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
		}
	}
	err = scanConfigVersion(tx.QueryRowContext(ctx, `UPDATE ops.config_versions SET status='published',published_by=$2,published_at=$3,updated_at=$3
		WHERE id=$1 AND status='submitted' RETURNING `+configVersionColumns, item.ID, actor, now), &item)
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	currentRevision++
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO ops.config_deployments (environment,config_kind,config_key,version_id,revision,deployed_by,deployed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (environment,config_kind,config_key) DO UPDATE SET
			version_id=EXCLUDED.version_id,revision=EXCLUDED.revision,
			deployed_by=EXCLUDED.deployed_by,deployed_at=EXCLUDED.deployed_at`,
		environment, item.Kind, item.Key, item.ID, currentRevision, actor, now); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	err = scanAgentRollout(tx.QueryRowContext(ctx, `UPDATE ops.agent_rollouts SET status='promoted',decision='pass',
		revision=revision+1,updated_by=$3,updated_at=$4,completed_at=$4
		WHERE id=$1 AND revision=$2 AND status='ready' RETURNING `+agentRolloutColumns,
		rollout.ID, rollout.Revision, actor, now), &rollout)
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	if err = insertConfigAudit(ctx, tx, "config.version.publish", item, actor, item.Reason, now, map[string]any{
		"environment": environment, "deployment_revision": currentRevision,
		"previous_version_id": currentID, "agent_rollout_id": rollout.ID,
	}); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	if err = insertAgentRolloutAudit(ctx, tx, "agent.rollout.promote", rollout, actor, "online quality gate passed", now); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.AgentRollout{}, err
	}
	deployment := controlplane.Deployment{Environment: environment, Kind: item.Kind, Key: item.Key,
		Revision: currentRevision, DeployedBy: actor, DeployedAt: now, Version: item}
	return item, deployment, rollout, nil
}

const agentRolloutColumns = `
	id::text,environment,agent_key,agent_version_id::text,agent_version,agent_fingerprint,
	baseline_version_id::text,baseline_version,baseline_fingerprint,traffic_percent,routing_revision,
	policy,status,decision,summary,violations,revision,created_by,updated_by,
	created_at,updated_at,evaluated_at,completed_at`

func scanAgentRollout(row controlplaneScanner, rollout *controlplane.AgentRollout) error {
	var policy, summary, violations json.RawMessage
	if err := row.Scan(
		&rollout.ID, &rollout.Environment, &rollout.AgentKey, &rollout.AgentVersionID,
		&rollout.AgentVersion, &rollout.AgentFingerprint, &rollout.BaselineVersionID,
		&rollout.BaselineVersion, &rollout.BaselineFingerprint, &rollout.TrafficPercent,
		&rollout.RoutingRevision, &policy, &rollout.Status, &rollout.Decision,
		&summary, &violations, &rollout.Revision, &rollout.CreatedBy, &rollout.UpdatedBy,
		&rollout.CreatedAt, &rollout.UpdatedAt, &rollout.EvaluatedAt, &rollout.CompletedAt,
	); err != nil {
		return err
	}
	if err := json.Unmarshal(policy, &rollout.Policy); err != nil {
		return err
	}
	if err := json.Unmarshal(summary, &rollout.Summary); err != nil {
		return err
	}
	return json.Unmarshal(violations, &rollout.Violations)
}

func insertAgentRolloutAudit(ctx context.Context, tx *sql.Tx, action string, rollout controlplane.AgentRollout, actor, reason string, now time.Time) error {
	payload, _ := json.Marshal(map[string]any{
		"actor": actor, "reason": reason, "environment": rollout.Environment,
		"agent_key": rollout.AgentKey, "agent_version_id": rollout.AgentVersionID,
		"agent_version": rollout.AgentVersion, "agent_fingerprint": rollout.AgentFingerprint,
		"baseline_version_id": rollout.BaselineVersionID, "traffic_percent": rollout.TrafficPercent,
		"status": rollout.Status, "decision": rollout.Decision, "revision": rollout.Revision,
		"violations": rollout.Violations,
	})
	_, err := tx.ExecContext(ctx, `INSERT INTO eventing.audit_logs
		(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('operator',NULL,$1,'agent_rollout',$2,$3,$4)`, action, rollout.ID, payload, now)
	return err
}
