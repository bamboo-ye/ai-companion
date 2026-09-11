package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

var _ controlplane.Store = (*Store)(nil)

func (s *Store) ListConfigVersions(ctx context.Context, kind, key string, limit int) ([]controlplane.Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+configVersionColumns+`
		FROM ops.config_versions
		WHERE config_kind=$1 AND ($2='' OR config_key=$2)
		ORDER BY config_key,version DESC
		LIMIT $3`, kind, key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.Version, 0)
	for rows.Next() {
		var item controlplane.Version
		if err = scanConfigVersion(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetConfigVersion(ctx context.Context, id string) (controlplane.Version, error) {
	var item controlplane.Version
	err := scanConfigVersion(s.db.QueryRowContext(ctx, `SELECT `+configVersionColumns+` FROM ops.config_versions WHERE id=$1`, id), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.Version{}, controlplane.ErrNotFound
	}
	return item, err
}

func (s *Store) CreateConfigVersion(ctx context.Context, item controlplane.Version) (controlplane.Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.Version{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, item.Kind+":"+item.Key); err != nil {
		return controlplane.Version{}, err
	}
	var latest int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM ops.config_versions WHERE config_kind=$1 AND config_key=$2`, item.Kind, item.Key).Scan(&latest); err != nil {
		return controlplane.Version{}, err
	}
	if latest != item.BaseVersion {
		return controlplane.Version{}, controlplane.ErrConflict
	}
	item.Version = latest + 1
	err = scanConfigVersion(tx.QueryRowContext(ctx, `
		INSERT INTO ops.config_versions (
			id,config_kind,config_key,version,base_version,schema_version,status,payload,
			fingerprint,reason,created_by,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
		RETURNING `+configVersionColumns,
		item.ID, item.Kind, item.Key, item.Version, item.BaseVersion, item.SchemaVersion,
		item.Status, item.Payload, item.Fingerprint, item.Reason, item.CreatedBy, item.CreatedAt,
	), &item)
	if err != nil {
		return controlplane.Version{}, err
	}
	if err = insertConfigAudit(ctx, tx, "config.version.create", item, item.CreatedBy, item.Reason, item.CreatedAt, nil); err != nil {
		return controlplane.Version{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.Version{}, err
	}
	return item, nil
}

func (s *Store) TransitionConfigVersion(ctx context.Context, id, from, to, actor string, now time.Time) (controlplane.Version, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.Version{}, err
	}
	defer tx.Rollback()
	var item controlplane.Version
	err = scanConfigVersion(tx.QueryRowContext(ctx, `
		UPDATE ops.config_versions SET
			status=$3,updated_at=$5,
			validated_by=CASE WHEN $3='validated' THEN $4 ELSE validated_by END,
			validated_at=CASE WHEN $3='validated' THEN $5 ELSE validated_at END,
			submitted_by=CASE WHEN $3='submitted' THEN $4 ELSE submitted_by END,
			submitted_at=CASE WHEN $3='submitted' THEN $5 ELSE submitted_at END
		WHERE id=$1 AND status=$2
		RETURNING `+configVersionColumns, id, from, to, actor, now), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.Version{}, controlplane.ErrConflict
	}
	if err != nil {
		return controlplane.Version{}, err
	}
	if err = insertConfigAudit(ctx, tx, "config.version."+to, item, actor, item.Reason, now, map[string]any{"from_status": from, "to_status": to}); err != nil {
		return controlplane.Version{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.Version{}, err
	}
	return item, nil
}

func (s *Store) PublishConfigVersion(ctx context.Context, id, environment, actor string, now time.Time) (controlplane.Version, controlplane.Deployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	defer tx.Rollback()
	var item controlplane.Version
	err = scanConfigVersion(tx.QueryRowContext(ctx, `SELECT `+configVersionColumns+` FROM ops.config_versions WHERE id=$1 FOR UPDATE`, id), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	if item.Status != controlplane.StatusSubmitted || actor == item.CreatedBy || actor == item.SubmittedBy {
		return controlplane.Version{}, controlplane.Deployment{}, controlplane.ErrConflict
	}
	var currentID string
	var currentRevision int
	err = tx.QueryRowContext(ctx, `
		SELECT version_id::text,revision FROM ops.config_deployments
		WHERE environment=$1 AND config_kind=$2 AND config_key=$3 FOR UPDATE`,
		environment, item.Kind, item.Key).Scan(&currentID, &currentRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	if currentID != "" && currentID != item.ID {
		if _, err = tx.ExecContext(ctx, `UPDATE ops.config_versions SET status='superseded',updated_at=$2 WHERE id=$1 AND status='published'`, currentID, now); err != nil {
			return controlplane.Version{}, controlplane.Deployment{}, err
		}
	}
	err = scanConfigVersion(tx.QueryRowContext(ctx, `
		UPDATE ops.config_versions SET status='published',published_by=$2,published_at=$3,updated_at=$3
		WHERE id=$1 AND status='submitted'
		RETURNING `+configVersionColumns, item.ID, actor, now), &item)
	if err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	revision := currentRevision + 1
	if revision == 0 {
		revision = 1
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT INTO ops.config_deployments (environment,config_kind,config_key,version_id,revision,deployed_by,deployed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (environment,config_kind,config_key) DO UPDATE SET
			version_id=EXCLUDED.version_id,revision=EXCLUDED.revision,
			deployed_by=EXCLUDED.deployed_by,deployed_at=EXCLUDED.deployed_at`,
		environment, item.Kind, item.Key, item.ID, revision, actor, now); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	if err = insertConfigAudit(ctx, tx, "config.version.publish", item, actor, item.Reason, now, map[string]any{"environment": environment, "deployment_revision": revision, "previous_version_id": currentID}); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.Version{}, controlplane.Deployment{}, err
	}
	deployment := controlplane.Deployment{Environment: environment, Kind: item.Kind, Key: item.Key, Revision: revision, DeployedBy: actor, DeployedAt: now, Version: item}
	return item, deployment, nil
}

func (s *Store) RollbackConfigDeployment(ctx context.Context, kind, key string, targetVersion int, environment, actor, reason string, now time.Time) (controlplane.Deployment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.Deployment{}, err
	}
	defer tx.Rollback()
	var target controlplane.Version
	err = scanConfigVersion(tx.QueryRowContext(ctx, `SELECT `+configVersionColumns+`
		FROM ops.config_versions WHERE config_kind=$1 AND config_key=$2 AND version=$3 FOR UPDATE`,
		kind, key, targetVersion), &target)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.Deployment{}, controlplane.ErrNotFound
	}
	if err != nil {
		return controlplane.Deployment{}, err
	}
	if target.Status != controlplane.StatusPublished && target.Status != controlplane.StatusSuperseded {
		return controlplane.Deployment{}, controlplane.ErrConflict
	}
	var currentID string
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT version_id::text,revision FROM ops.config_deployments
		WHERE environment=$1 AND config_kind=$2 AND config_key=$3 FOR UPDATE`, environment, kind, key).Scan(&currentID, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return controlplane.Deployment{}, controlplane.ErrConflict
		}
		return controlplane.Deployment{}, err
	}
	if currentID == target.ID {
		return controlplane.Deployment{}, controlplane.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE ops.config_versions SET status='superseded',updated_at=$2 WHERE id=$1 AND status='published'`, currentID, now); err != nil {
		return controlplane.Deployment{}, err
	}
	err = scanConfigVersion(tx.QueryRowContext(ctx, `UPDATE ops.config_versions SET status='published',published_by=$2,published_at=$3,updated_at=$3 WHERE id=$1 RETURNING `+configVersionColumns, target.ID, actor, now), &target)
	if err != nil {
		return controlplane.Deployment{}, err
	}
	revision++
	if _, err = tx.ExecContext(ctx, `UPDATE ops.config_deployments SET version_id=$4,revision=$5,deployed_by=$6,deployed_at=$7 WHERE environment=$1 AND config_kind=$2 AND config_key=$3`, environment, kind, key, target.ID, revision, actor, now); err != nil {
		return controlplane.Deployment{}, err
	}
	if err = insertConfigAudit(ctx, tx, "config.deployment.rollback", target, actor, reason, now, map[string]any{"environment": environment, "deployment_revision": revision, "previous_version_id": currentID}); err != nil {
		return controlplane.Deployment{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.Deployment{}, err
	}
	return controlplane.Deployment{Environment: environment, Kind: kind, Key: key, Revision: revision, DeployedBy: actor, DeployedAt: now, Version: target}, nil
}

func (s *Store) ListActiveConfigDeployments(ctx context.Context, kind, environment string) ([]controlplane.Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.environment,d.config_kind,d.config_key,d.revision,d.deployed_by,d.deployed_at,`+configVersionColumnsWithAlias("v")+`
		FROM ops.config_deployments d
		JOIN ops.config_versions v ON v.id=d.version_id
		WHERE d.config_kind=$1 AND (
			d.environment=$2 OR (d.environment='default' AND NOT EXISTS (
				SELECT 1 FROM ops.config_deployments exact
				WHERE exact.environment=$2 AND exact.config_kind=d.config_kind AND exact.config_key=d.config_key
			)))
		ORDER BY d.config_key`, kind, environment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.Deployment, 0)
	for rows.Next() {
		var item controlplane.Deployment
		if err = scanConfigDeployment(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListProviderConnections(ctx context.Context) ([]controlplane.ProviderConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,provider_type,display_name,base_url,credential_ref,status,capabilities FROM ops.provider_connections ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.ProviderConnection, 0)
	for rows.Next() {
		var item controlplane.ProviderConnection
		if err = rows.Scan(&item.Key, &item.ProviderType, &item.DisplayName, &item.BaseURL, &item.CredentialRef, &item.Status, &item.Capabilities); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListModelCatalog(ctx context.Context) ([]controlplane.ModelCatalogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model_id,provider_key,display_name,model_class,context_window,max_output_tokens,prompt_price::float8,completion_price::float8,zero_price,status,capabilities FROM ops.model_catalog ORDER BY model_class,model_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.ModelCatalogEntry, 0)
	for rows.Next() {
		var item controlplane.ModelCatalogEntry
		if err = rows.Scan(&item.ModelID, &item.ProviderKey, &item.DisplayName, &item.ModelClass, &item.ContextWindow, &item.MaxOutputTokens, &item.PromptPrice, &item.CompletionPrice, &item.ZeroPrice, &item.Status, &item.Capabilities); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateAgentEvaluationRun(ctx context.Context, run controlplane.AgentEvaluationRun) (controlplane.AgentEvaluationRun, error) {
	suite, err := json.Marshal(run.Suite)
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	defer tx.Rollback()
	err = scanAgentEvaluationRun(tx.QueryRowContext(ctx, `
		INSERT INTO ops.evaluation_runs (
			id,agent_version_id,agent_key,agent_version,agent_fingerprint,
			suite_name,suite_version,suite_fingerprint,suite,baseline_run_id,
			status,decision,summary,results,created_by,started_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,$11,NULL,'{}'::jsonb,'[]'::jsonb,$12,$13)
		RETURNING `+agentEvaluationColumns,
		run.ID, run.AgentVersionID, run.AgentKey, run.AgentVersion, run.AgentFingerprint,
		run.Suite.Name, run.Suite.Version, run.SuiteFingerprint, suite, run.Suite.BaselineRunID,
		run.Status, run.CreatedBy, run.StartedAt,
	), &run)
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	if err = insertAgentEvaluationAudit(ctx, tx, "agent.evaluation.start", run, run.CreatedBy, run.StartedAt); err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	return run, nil
}

func (s *Store) CompleteAgentEvaluationRun(
	ctx context.Context,
	runID, fromStatus, decision string,
	summary controlplane.AgentEvaluationSummary,
	results []controlplane.AgentEvaluationCaseResult,
	now time.Time,
) (controlplane.AgentEvaluationRun, error) {
	summaryPayload, err := json.Marshal(summary)
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	resultsPayload, err := json.Marshal(results)
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	defer tx.Rollback()
	var run controlplane.AgentEvaluationRun
	err = scanAgentEvaluationRun(tx.QueryRowContext(ctx, `
		UPDATE ops.evaluation_runs SET
			status='completed',decision=$3,summary=$4,results=$5,completed_at=$6
		WHERE id=$1 AND status=$2
		RETURNING `+agentEvaluationColumns,
		runID, fromStatus, decision, summaryPayload, resultsPayload, now,
	), &run)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentEvaluationRun{}, controlplane.ErrConflict
	}
	if err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	if err = insertAgentEvaluationAudit(ctx, tx, "agent.evaluation.complete", run, run.CreatedBy, now); err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	if err = tx.Commit(); err != nil {
		return controlplane.AgentEvaluationRun{}, err
	}
	return run, nil
}

func (s *Store) GetAgentEvaluationRun(ctx context.Context, runID string) (controlplane.AgentEvaluationRun, error) {
	var run controlplane.AgentEvaluationRun
	err := scanAgentEvaluationRun(s.db.QueryRowContext(ctx, `SELECT `+agentEvaluationColumns+` FROM ops.evaluation_runs WHERE id=$1`, runID), &run)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentEvaluationRun{}, controlplane.ErrNotFound
	}
	return run, err
}

func (s *Store) ListAgentEvaluationRuns(ctx context.Context, versionID string, limit int) ([]controlplane.AgentEvaluationRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+agentEvaluationColumns+`
		FROM ops.evaluation_runs WHERE agent_version_id=$1 ORDER BY started_at DESC LIMIT $2`, versionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.AgentEvaluationRun, 0)
	for rows.Next() {
		var run controlplane.AgentEvaluationRun
		if err = scanAgentEvaluationRun(rows, &run); err != nil {
			return nil, err
		}
		items = append(items, run)
	}
	return items, rows.Err()
}

func (s *Store) LatestPassingAgentEvaluationRun(ctx context.Context, versionID, fingerprint string) (controlplane.AgentEvaluationRun, error) {
	var run controlplane.AgentEvaluationRun
	err := scanAgentEvaluationRun(s.db.QueryRowContext(ctx, `SELECT `+agentEvaluationColumns+`
		FROM ops.evaluation_runs
		WHERE agent_version_id=$1 AND agent_fingerprint=$2 AND status='completed' AND decision='pass'
		ORDER BY completed_at DESC LIMIT 1`, versionID, fingerprint), &run)
	if errors.Is(err, sql.ErrNoRows) {
		return controlplane.AgentEvaluationRun{}, controlplane.ErrNotFound
	}
	return run, err
}

const configVersionColumns = `
	id::text,config_kind,config_key,version,base_version,schema_version,status,payload,
	fingerprint,reason,created_by,COALESCE(validated_by,''),COALESCE(submitted_by,''),
	COALESCE(published_by,''),created_at,updated_at,validated_at,submitted_at,published_at`

func configVersionColumnsWithAlias(alias string) string {
	return alias + `.id::text,` + alias + `.config_kind,` + alias + `.config_key,` + alias + `.version,` + alias + `.base_version,` + alias + `.schema_version,` + alias + `.status,` + alias + `.payload,` + alias + `.fingerprint,` + alias + `.reason,` + alias + `.created_by,COALESCE(` + alias + `.validated_by,''),COALESCE(` + alias + `.submitted_by,''),COALESCE(` + alias + `.published_by,''),` + alias + `.created_at,` + alias + `.updated_at,` + alias + `.validated_at,` + alias + `.submitted_at,` + alias + `.published_at`
}

type controlplaneScanner interface{ Scan(...any) error }

const agentEvaluationColumns = `
	id::text,agent_version_id::text,agent_key,agent_version,agent_fingerprint,
	suite,suite_fingerprint,status,COALESCE(decision,''),summary,results,
	created_by,started_at,completed_at`

func scanConfigVersion(row controlplaneScanner, item *controlplane.Version) error {
	return row.Scan(&item.ID, &item.Kind, &item.Key, &item.Version, &item.BaseVersion,
		&item.SchemaVersion, &item.Status, &item.Payload, &item.Fingerprint, &item.Reason,
		&item.CreatedBy, &item.ValidatedBy, &item.SubmittedBy, &item.PublishedBy,
		&item.CreatedAt, &item.UpdatedAt, &item.ValidatedAt, &item.SubmittedAt, &item.PublishedAt)
}

func scanConfigDeployment(row controlplaneScanner, item *controlplane.Deployment) error {
	return row.Scan(&item.Environment, &item.Kind, &item.Key, &item.Revision,
		&item.DeployedBy, &item.DeployedAt, &item.Version.ID, &item.Version.Kind,
		&item.Version.Key, &item.Version.Version, &item.Version.BaseVersion,
		&item.Version.SchemaVersion, &item.Version.Status, &item.Version.Payload,
		&item.Version.Fingerprint, &item.Version.Reason, &item.Version.CreatedBy,
		&item.Version.ValidatedBy, &item.Version.SubmittedBy, &item.Version.PublishedBy,
		&item.Version.CreatedAt, &item.Version.UpdatedAt, &item.Version.ValidatedAt,
		&item.Version.SubmittedAt, &item.Version.PublishedAt)
}

func scanAgentEvaluationRun(row controlplaneScanner, run *controlplane.AgentEvaluationRun) error {
	var suite, summary, results json.RawMessage
	if err := row.Scan(
		&run.ID, &run.AgentVersionID, &run.AgentKey, &run.AgentVersion, &run.AgentFingerprint,
		&suite, &run.SuiteFingerprint, &run.Status, &run.Decision, &summary, &results,
		&run.CreatedBy, &run.StartedAt, &run.CompletedAt,
	); err != nil {
		return err
	}
	if err := json.Unmarshal(suite, &run.Suite); err != nil {
		return err
	}
	if err := json.Unmarshal(summary, &run.Summary); err != nil {
		return err
	}
	return json.Unmarshal(results, &run.Results)
}

func insertConfigAudit(ctx context.Context, tx *sql.Tx, action string, item controlplane.Version, actor, reason string, now time.Time, extra map[string]any) error {
	metadata := map[string]any{
		"actor": actor, "reason": reason, "config_kind": item.Kind,
		"config_key": item.Key, "version": item.Version, "fingerprint": item.Fingerprint,
	}
	for key, value := range extra {
		metadata[key] = value
	}
	payload, _ := json.Marshal(metadata)
	_, err := tx.ExecContext(ctx, `INSERT INTO eventing.audit_logs
		(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('operator',NULL,$1,'config_version',$2,$3,$4)`, action, item.ID, payload, now)
	return err
}

func insertAgentEvaluationAudit(ctx context.Context, tx *sql.Tx, action string, run controlplane.AgentEvaluationRun, actor string, now time.Time) error {
	payload, _ := json.Marshal(map[string]any{
		"actor": actor, "agent_key": run.AgentKey, "agent_version": run.AgentVersion,
		"agent_version_id": run.AgentVersionID, "agent_fingerprint": run.AgentFingerprint,
		"suite_name": run.Suite.Name, "suite_version": run.Suite.Version,
		"suite_fingerprint": run.SuiteFingerprint, "status": run.Status, "decision": run.Decision,
	})
	_, err := tx.ExecContext(ctx, `INSERT INTO eventing.audit_logs
		(actor_type,actor_id,action,resource_type,resource_id,metadata,occurred_at)
		VALUES ('operator',NULL,$1,'agent_evaluation_run',$2,$3,$4)`, action, run.ID, payload, now)
	return err
}
