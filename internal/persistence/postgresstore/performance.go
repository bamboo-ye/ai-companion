package postgresstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/incident"
	"github.com/windcry1/ai-companion/internal/performance"
	"github.com/windcry1/ai-companion/internal/platform/id"
)

func (s *Store) ListVersionMetrics(ctx context.Context, filter performance.Filter) ([]performance.VersionMetrics, error) {
	identity := `
		COALESCE(NULLIF(agent_definition_version_id::text,''),'builtin:' || graph_name || ':' || graph_version) AS group_id,
		COALESCE(NULLIF(agent_definition_key,''),graph_name) AS item_key,
		COALESCE(NULLIF(agent_definition_version_id::text,''),'') AS version_id,
		COALESCE(agent_definition_version,0) AS item_version,
		COALESCE(agent_definition_revision,0) AS item_revision,
		graph_version AS config_version`
	if filter.Dimension == performance.DimensionModelProfile {
		identity = `
		COALESCE(NULLIF(model_profile_version_id::text,''),'builtin:' || COALESCE(NULLIF(model_profile_key,''),'default') || ':' || COALESCE(NULLIF(model_profile_config_version,''),'default')) AS group_id,
		COALESCE(NULLIF(model_profile_key,''),'default') AS item_key,
		COALESCE(NULLIF(model_profile_version_id::text,''),'') AS version_id,
		0 AS item_version,
		COALESCE(model_profile_revision,0) AS item_revision,
		COALESCE(NULLIF(model_profile_config_version,''),'default') AS config_version`
	}
	query := fmt.Sprintf(`
		WITH scoped AS (
			SELECT %s,status,COALESCE(error_code,'') AS error_code,
				COALESCE(output->>'outcome','') AS outcome,created_at,
				GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(completed_at,updated_at)-created_at))*1000)::float8 AS duration_ms,
				COALESCE(usage.model_calls,0)::bigint AS model_calls,
				COALESCE(usage.prompt_tokens,0)::bigint AS prompt_tokens,
				COALESCE(usage.completion_tokens,0)::bigint AS completion_tokens,
				COALESCE(usage.cost_micros,0)::bigint AS cost_micros
			FROM agent.runs
			LEFT JOIN LATERAL (
				SELECT COUNT(*)::bigint AS model_calls,
					COALESCE(SUM(CASE WHEN (call->>'prompt_tokens') ~ '^[0-9]+$' THEN (call->>'prompt_tokens')::numeric ELSE 0 END),0)::bigint AS prompt_tokens,
					COALESCE(SUM(CASE WHEN (call->>'completion_tokens') ~ '^[0-9]+$' THEN (call->>'completion_tokens')::numeric ELSE 0 END),0)::bigint AS completion_tokens,
					COALESCE(SUM(CASE WHEN (call->>'cost_micros') ~ '^[0-9]+$' THEN (call->>'cost_micros')::numeric ELSE 0 END),0)::bigint AS cost_micros
				FROM jsonb_array_elements(CASE WHEN jsonb_typeof(output #> '{model,calls}')='array' THEN output #> '{model,calls}' ELSE '[]'::jsonb END) call
			) usage ON true
			WHERE created_at >= $1 AND created_at < $2 AND ($3='' OR module_key=$3)
		), aggregate AS (
			SELECT group_id,item_key,version_id,item_version,item_revision,config_version,
				COUNT(*)::int AS runs,
				COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out'))::int AS sample_size,
				COUNT(*) FILTER (WHERE status='completed')::int AS completed,
				COUNT(*) FILTER (WHERE status IN ('failed','timed_out'))::int AS failed,
				COUNT(*) FILTER (WHERE status='cancelled')::int AS cancelled,
				COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out') AND (
					error_code IN ('response_quality_failed','artifact_quality_failed','email_quality_failed') OR
					outcome IN ('response_quality_failed','artifact_quality_failed','email_quality_failed')
				))::int AS quality_failures,
				COALESCE(SUM(model_calls),0)::bigint AS model_calls,
				COALESCE(SUM(prompt_tokens),0)::bigint AS prompt_tokens,
				COALESCE(SUM(completion_tokens),0)::bigint AS completion_tokens,
				COALESCE(SUM(cost_micros),0)::bigint AS cost_micros,
				COALESCE(AVG(duration_ms) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out')),0)::float8 AS average_duration_ms,
				COALESCE(percentile_cont(.5) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out')),0)::float8 AS p50_duration_ms,
				COALESCE(percentile_cont(.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out')),0)::float8 AS p95_duration_ms,
				MAX(created_at) AS last_seen
			FROM scoped
			GROUP BY group_id,item_key,version_id,item_version,item_revision,config_version
		)
		SELECT group_id,item_key,version_id,item_version,item_revision,config_version,
			runs,sample_size,completed,failed,cancelled,quality_failures,model_calls,prompt_tokens,
			completion_tokens,cost_micros,average_duration_ms,p50_duration_ms,p95_duration_ms
		FROM aggregate ORDER BY sample_size DESC,last_seen DESC LIMIT $4`, identity)
	rows, err := s.db.QueryContext(ctx, query, filter.From, filter.To, filter.Module, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]performance.VersionMetrics, 0)
	for rows.Next() {
		var item performance.VersionMetrics
		item.Dimension = filter.Dimension
		if err = rows.Scan(&item.GroupID, &item.Key, &item.VersionID, &item.Version, &item.Revision, &item.ConfigVersion,
			&item.Runs, &item.SampleSize, &item.Completed, &item.Failed, &item.Cancelled, &item.QualityFailures,
			&item.ModelCalls, &item.PromptTokens, &item.CompletionTokens, &item.CostMicros,
			&item.AverageDurationMS, &item.P50DurationMS, &item.P95DurationMS); err != nil {
			return nil, err
		}
		item.Label = versionMetricLabel(item)
		setVersionRates(&item)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListModelMetrics(ctx context.Context, filter performance.Filter) ([]performance.ModelMetrics, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(usage.provider,''),'unknown') AS provider,
			COALESCE(NULLIF(usage.returned_model,''),NULLIF(usage.requested_model,''),'unknown') AS model,
			COUNT(*)::int AS calls,
			COUNT(*) FILTER (WHERE usage.status IN ('succeeded','completed'))::int AS succeeded,
			COUNT(*) FILTER (WHERE usage.status NOT IN ('succeeded','completed'))::int AS failed,
			COALESCE(SUM(usage.prompt_tokens),0)::bigint,
			COALESCE(SUM(usage.completion_tokens),0)::bigint,
			COALESCE(SUM(usage.cached_tokens),0)::bigint,
			COALESCE(SUM(usage.cost_micros),0)::bigint,
			COALESCE(AVG(usage.latency_ms),0)::float8,
			COALESCE(percentile_cont(.95) WITHIN GROUP (ORDER BY usage.latency_ms),0)::float8
		FROM app.model_usage_observations usage
		JOIN agent.runs run ON run.id::text=usage.execution_id
		WHERE usage.source_type='agent_graph' AND usage.observed_at >= $1 AND usage.observed_at < $2
			AND ($3='' OR run.module_key=$3)
		GROUP BY 1,2 ORDER BY SUM(usage.cost_micros) DESC,COUNT(*) DESC LIMIT $4`,
		filter.From, filter.To, filter.Module, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]performance.ModelMetrics, 0)
	for rows.Next() {
		var item performance.ModelMetrics
		if err = rows.Scan(&item.Provider, &item.Model, &item.Calls, &item.Succeeded, &item.Failed,
			&item.PromptTokens, &item.CompletionTokens, &item.CachedTokens, &item.CostMicros,
			&item.AverageLatencyMS, &item.P95LatencyMS); err != nil {
			return nil, err
		}
		if item.Calls > 0 {
			item.ErrorRate = float64(item.Failed) / float64(item.Calls)
			item.AverageCostMicros = float64(item.CostMicros) / float64(item.Calls)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) MeasureWindow(ctx context.Context, filter performance.WindowFilter) (performance.WindowMetrics, error) {
	item := performance.WindowMetrics{From: filter.From, To: filter.To}
	err := s.db.QueryRowContext(ctx, `
		WITH scoped AS (
			SELECT status,COALESCE(error_code,'') AS error_code,COALESCE(output->>'outcome','') AS outcome,
				GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(completed_at,updated_at)-created_at))*1000)::float8 AS duration_ms,
				COALESCE(usage.model_calls,0)::bigint AS model_calls,COALESCE(usage.cost_micros,0)::bigint AS cost_micros
			FROM agent.runs
			LEFT JOIN LATERAL (
				SELECT COUNT(*)::bigint AS model_calls,
					COALESCE(SUM(CASE WHEN (call->>'cost_micros') ~ '^[0-9]+$' THEN (call->>'cost_micros')::numeric ELSE 0 END),0)::bigint AS cost_micros
				FROM jsonb_array_elements(CASE WHEN jsonb_typeof(output #> '{model,calls}')='array' THEN output #> '{model,calls}' ELSE '[]'::jsonb END) call
			) usage ON true
			WHERE created_at >= $1 AND created_at < $2 AND ($3='' OR module_key=$3)
		)
		SELECT COUNT(*)::int,
			COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out'))::int,
			COUNT(*) FILTER (WHERE status='completed')::int,
			COUNT(*) FILTER (WHERE status IN ('failed','timed_out'))::int,
			COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out') AND (
				error_code IN ('response_quality_failed','artifact_quality_failed','email_quality_failed') OR
				outcome IN ('response_quality_failed','artifact_quality_failed','email_quality_failed')
			))::int,
			COALESCE(SUM(model_calls),0)::bigint,COALESCE(SUM(cost_micros),0)::bigint,
			COALESCE(percentile_cont(.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out')),0)::float8
		FROM scoped`, filter.From, filter.To, filter.Module).Scan(
		&item.Runs, &item.SampleSize, &item.Completed, &item.Failed, &item.QualityFailures,
		&item.ModelCalls, &item.CostMicros, &item.P95DurationMS)
	if err != nil {
		return performance.WindowMetrics{}, err
	}
	if item.SampleSize > 0 {
		item.SuccessRate = float64(item.Completed) / float64(item.SampleSize)
		item.ErrorRate = float64(item.Failed) / float64(item.SampleSize)
		item.QualityFailureRate = float64(item.QualityFailures) / float64(item.SampleSize)
		item.AverageCostMicros = float64(item.CostMicros) / float64(item.SampleSize)
	}
	return item, nil
}

func (s *Store) ListTrend(ctx context.Context, filter performance.TrendFilter) ([]performance.TrendPoint, error) {
	step, trunc := "1 hour", "hour"
	if filter.Bucket == "day" {
		step, trunc = "1 day", "day"
	}
	query := fmt.Sprintf(`
		WITH buckets AS (
			SELECT point AS bucket_from,point + INTERVAL '%s' AS bucket_to
			FROM generate_series(date_trunc('%s',$1::timestamptz),$2::timestamptz - INTERVAL '1 microsecond',INTERVAL '%s') point
		), scoped AS (
			SELECT date_trunc('%s',run.created_at) AS bucket_from,run.status,
				GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(run.completed_at,run.updated_at)-run.created_at))*1000)::float8 AS duration_ms,
				COALESCE(usage.model_calls,0)::bigint AS model_calls,COALESCE(usage.cost_micros,0)::bigint AS cost_micros
			FROM agent.runs run
			LEFT JOIN LATERAL (
				SELECT COUNT(*)::bigint AS model_calls,
					COALESCE(SUM(CASE WHEN (call->>'cost_micros') ~ '^[0-9]+$' THEN (call->>'cost_micros')::numeric ELSE 0 END),0)::bigint AS cost_micros
				FROM jsonb_array_elements(CASE WHEN jsonb_typeof(run.output #> '{model,calls}')='array' THEN run.output #> '{model,calls}' ELSE '[]'::jsonb END) call
			) usage ON true
			WHERE run.created_at >= $1 AND run.created_at < $2 AND ($3='' OR run.module_key=$3)
		), aggregate AS (
			SELECT bucket_from,COUNT(*)::int AS runs,
				COUNT(*) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out'))::int AS sample_size,
				COUNT(*) FILTER (WHERE status='completed')::int AS completed,
				COUNT(*) FILTER (WHERE status IN ('failed','timed_out'))::int AS failed,
				COALESCE(SUM(model_calls),0)::bigint AS model_calls,COALESCE(SUM(cost_micros),0)::bigint AS cost_micros,
				COALESCE(percentile_cont(.95) WITHIN GROUP (ORDER BY duration_ms) FILTER (WHERE status IN ('completed','failed','cancelled','timed_out')),0)::float8 AS p95_duration_ms
			FROM scoped GROUP BY bucket_from
		)
		SELECT buckets.bucket_from,buckets.bucket_to,COALESCE(a.runs,0),COALESCE(a.sample_size,0),COALESCE(a.completed,0),COALESCE(a.failed,0),COALESCE(a.model_calls,0),COALESCE(a.cost_micros,0),COALESCE(a.p95_duration_ms,0)
		FROM buckets LEFT JOIN aggregate a ON a.bucket_from=buckets.bucket_from ORDER BY buckets.bucket_from`, step, trunc, step, trunc)
	rows, err := s.db.QueryContext(ctx, query, filter.From, filter.To, filter.Module)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]performance.TrendPoint, 0)
	for rows.Next() {
		var item performance.TrendPoint
		if err = rows.Scan(&item.From, &item.To, &item.Runs, &item.SampleSize, &item.Completed, &item.Failed, &item.ModelCalls, &item.CostMicros, &item.P95DurationMS); err != nil {
			return nil, err
		}
		if item.SampleSize > 0 {
			item.SuccessRate = float64(item.Completed) / float64(item.SampleSize)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListBudgets(ctx context.Context) ([]performance.Budget, error) {
	rows, err := s.db.QueryContext(ctx, performanceBudgetSelect+` ORDER BY period,module_key NULLS FIRST,created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]performance.Budget, 0)
	for rows.Next() {
		item, scanErr := scanPerformanceBudget(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetBudget(ctx context.Context, budgetID string) (performance.Budget, error) {
	item, err := scanPerformanceBudget(s.db.QueryRowContext(ctx, performanceBudgetSelect+` WHERE id=$1`, budgetID))
	if errors.Is(err, sql.ErrNoRows) {
		return performance.Budget{}, performance.ErrNotFound
	}
	return item, err
}

func (s *Store) ListBudgetForecastOutcomes(ctx context.Context, filter performance.ForecastHistoryFilter) ([]performance.BudgetForecastOutcome, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id::text,i.source_id::text,
			COALESCE(NULLIF(i.evidence->>'budget_name',''),i.title),
			COALESCE(NULLIF(i.evidence->>'module',''),NULLIF(i.service,'all'),''),
			COALESCE(NULLIF(i.evidence->>'period',''),CASE WHEN i.window_minutes > 1440 THEN 'monthly' ELSE 'daily' END),
			CASE WHEN escalated.occurred_at IS NOT NULL THEN 'hit' WHEN i.resolved_at IS NOT NULL THEN 'cleared' ELSE 'observing' END,
			opened.occurred_at,
			CASE WHEN escalated.occurred_at IS NOT NULL THEN escalated.occurred_at WHEN i.resolved_at IS NOT NULL THEN i.resolved_at END,
			CASE WHEN escalated.occurred_at IS NOT NULL THEN GREATEST(0,FLOOR(EXTRACT(EPOCH FROM (escalated.occurred_at-opened.occurred_at))/60))::bigint WHEN i.resolved_at IS NOT NULL THEN GREATEST(0,FLOOR(EXTRACT(EPOCH FROM (i.resolved_at-opened.occurred_at))/60))::bigint ELSE 0 END,
			COALESCE(NULLIF(opened.metadata->>'utilization','')::double precision,0),
			COALESCE(NULLIF(opened.metadata->>'projected_utilization','')::double precision,0),
			COALESCE(NULLIF(opened.metadata->>'projected_cost_micros','')::bigint,0),
			COALESCE(NULLIF(opened.metadata->>'forecast_sample_size','')::integer,0),
			NULLIF(opened.metadata->>'projected_limit_exceeded_at','')::timestamptz,
			COALESCE(NULLIF(opened.metadata->>'budget_revision','')::integer,0)
		FROM ops.incidents i
		JOIN LATERAL (
			SELECT occurred_at,metadata
			FROM eventing.audit_logs
			WHERE resource_type='incident' AND resource_id=i.id AND action='incident.opened' AND metadata->>'status'='projected_exceeded'
			ORDER BY occurred_at,id LIMIT 1
		) opened ON TRUE
		LEFT JOIN LATERAL (
			SELECT occurred_at
			FROM eventing.audit_logs
			WHERE resource_type='incident' AND resource_id=i.id AND action='incident.escalated'
			ORDER BY occurred_at,id LIMIT 1
		) escalated ON TRUE
		WHERE i.source_type='performance_budget' AND opened.occurred_at >= $1 AND opened.occurred_at < $2
			AND ($3='' OR i.source_id::text=$3)
		ORDER BY opened.occurred_at DESC,i.id DESC`, filter.From, filter.To, filter.BudgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]performance.BudgetForecastOutcome, 0)
	for rows.Next() {
		var item performance.BudgetForecastOutcome
		if err = rows.Scan(
			&item.IncidentID, &item.BudgetID, &item.BudgetName, &item.Module, &item.Period, &item.Outcome,
			&item.OpenedAt, &item.OutcomeAt, &item.DurationMinutes, &item.InitialUtilization,
			&item.ProjectedUtilization, &item.ProjectedCostMicros, &item.ForecastSampleSize, &item.PredictedLimitExceededAt,
			&item.BudgetRevision,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListBudgetPolicyDecisions(ctx context.Context, budgetID string) ([]performance.BudgetPolicyDecision, error) {
	rows, err := s.db.QueryContext(ctx, budgetPolicyDecisionSelect+` WHERE ($1='' OR budget_id=NULLIF($1,'')::uuid) ORDER BY decided_at DESC LIMIT 1000`, strings.TrimSpace(budgetID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]performance.BudgetPolicyDecision, 0)
	for rows.Next() {
		item, scanErr := scanBudgetPolicyDecision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SaveBudgetPolicyDecision(ctx context.Context, item performance.BudgetPolicyDecision) (_ performance.BudgetPolicyDecision, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return performance.BudgetPolicyDecision{}, err
	}
	defer func() { _ = tx.Rollback() }()
	saved, err := scanBudgetPolicyDecision(tx.QueryRowContext(ctx, `
		INSERT INTO ops.performance_budget_recommendation_decisions (
			id,budget_id,recommendation_key,action,decision,confidence,outcome_count,hit_rate,average_lead_time_minutes,
			current_lookback_days,current_min_samples,proposed_lookback_days,proposed_min_samples,recommendation_reason,operator_reason,decided_by,decided_at,recommendation_budget_revision
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (budget_id,recommendation_key) DO UPDATE SET
			decision=EXCLUDED.decision,operator_reason=EXCLUDED.operator_reason,decided_by=EXCLUDED.decided_by,decided_at=EXCLUDED.decided_at
		RETURNING id::text,budget_id::text,recommendation_key,action,decision,confidence,outcome_count,hit_rate,average_lead_time_minutes,
			current_lookback_days,current_min_samples,proposed_lookback_days,proposed_min_samples,recommendation_reason,operator_reason,decided_by,decided_at,recommendation_budget_revision,applied_at,COALESCE(applied_by,''),COALESCE(applied_budget_revision,0),COALESCE(effect_observation_days,0),COALESCE(effect_min_samples,0),
			COALESCE(effect_review_status,''),COALESCE(effect_disposition,''),COALESCE(effect_review_reason,''),COALESCE(effect_reviewed_by,''),effect_reviewed_at,rollback_applied_at,COALESCE(rollback_applied_by,''),COALESCE(rollback_budget_revision,0),COALESCE(effect_closed_reason,''),COALESCE(effect_closed_by,''),effect_closed_at`,
		item.ID, item.BudgetID, item.RecommendationKey, item.Action, item.Decision, item.Confidence, item.OutcomeCount, item.HitRate, item.AverageLeadTimeMinutes,
		item.CurrentLookbackDays, item.CurrentMinSamples, item.ProposedLookbackDays, item.ProposedMinSamples, item.RecommendationReason, item.Reason, item.DecidedBy, item.DecidedAt, item.BudgetRevision,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return performance.BudgetPolicyDecision{}, performance.ErrNotFound
	}
	if err != nil {
		return performance.BudgetPolicyDecision{}, err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", saved.DecidedBy, "performance_budget.recommendation."+saved.Decision, "performance_budget_recommendation", saved.ID, saved.Reason, saved.DecidedAt, map[string]any{
		"budget_id": saved.BudgetID, "recommendation_key": saved.RecommendationKey, "action": saved.Action,
		"outcome_count": saved.OutcomeCount, "hit_rate": saved.HitRate, "current_min_samples": saved.CurrentMinSamples, "proposed_min_samples": saved.ProposedMinSamples,
	}); err != nil {
		return performance.BudgetPolicyDecision{}, err
	}
	if err = tx.Commit(); err != nil {
		return performance.BudgetPolicyDecision{}, err
	}
	return saved, nil
}

func (s *Store) AcknowledgeBudgetPolicyEffect(ctx context.Context, effect performance.BudgetPolicyEffect, review performance.BudgetPolicyEffectReview) (_ performance.BudgetPolicyEffectReview, err error) {
	if effect.Review != nil {
		review.RollbackAppliedAt = effect.Review.RollbackAppliedAt
		review.RollbackAppliedBy = effect.Review.RollbackAppliedBy
		review.RollbackBudgetRevision = effect.Review.RollbackBudgetRevision
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE ops.performance_budget_recommendation_decisions SET
			effect_review_status='acknowledged',effect_disposition=$3,effect_review_reason=$4,effect_reviewed_by=$5,effect_reviewed_at=$6,
			effect_closed_reason=NULL,effect_closed_by=NULL,effect_closed_at=NULL
		WHERE id=$1 AND budget_id=$2 AND applied_at IS NOT NULL AND COALESCE(effect_review_status,'') <> 'closed'
			AND (rollback_applied_at IS NULL OR $3='rollback_planned')`,
		effect.DecisionID, effect.BudgetID, review.Disposition, review.Reason, review.ReviewedBy, review.ReviewedAt)
	if err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	if changed != 1 {
		return performance.BudgetPolicyEffectReview{}, performance.ErrEffectReviewConflict
	}
	if err = insertIncidentAudit(ctx, tx, "operator", review.ReviewedBy, "performance_budget.effect.acknowledged", "performance_budget_recommendation", effect.DecisionID, review.Reason, review.ReviewedAt, budgetPolicyEffectReviewMetadata(effect, review.Disposition)); err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	if err = tx.Commit(); err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	return review, nil
}

func (s *Store) CloseBudgetPolicyEffect(ctx context.Context, effect performance.BudgetPolicyEffect, actor, reason string, now time.Time) (_ performance.BudgetPolicyEffectReview, err error) {
	if effect.Review == nil || effect.Review.Status != "acknowledged" {
		return performance.BudgetPolicyEffectReview{}, performance.ErrEffectReviewConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE ops.performance_budget_recommendation_decisions SET
			effect_review_status='closed',effect_closed_reason=$3,effect_closed_by=$4,effect_closed_at=$5
		WHERE id=$1 AND budget_id=$2 AND effect_review_status='acknowledged'`, effect.DecisionID, effect.BudgetID, reason, actor, now)
	if err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	if changed != 1 {
		return performance.BudgetPolicyEffectReview{}, performance.ErrEffectReviewConflict
	}
	review := *effect.Review
	review.Status, review.ClosedReason, review.ClosedBy, review.ClosedAt = "closed", reason, actor, &now
	if err = insertIncidentAudit(ctx, tx, "operator", actor, "performance_budget.effect.closed", "performance_budget_recommendation", effect.DecisionID, reason, now, budgetPolicyEffectReviewMetadata(effect, review.Disposition)); err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	if err = tx.Commit(); err != nil {
		return performance.BudgetPolicyEffectReview{}, err
	}
	return review, nil
}

func budgetPolicyEffectReviewMetadata(effect performance.BudgetPolicyEffect, disposition string) map[string]any {
	metadata := map[string]any{
		"budget_id": effect.BudgetID, "decision_id": effect.DecisionID, "disposition": disposition,
		"applied_budget_revision": effect.AppliedBudgetRevision, "effect_status": effect.Status,
		"observation_ends_at": effect.ObservationEndsAt, "minimum_decided_samples": effect.MinimumDecidedSamples,
		"before_hit_rate": effect.Before.HitRate, "after_hit_rate": effect.After.HitRate, "after_decided": effect.After.Decided,
		"rollback_lookback_days": effect.RollbackLookbackDays, "rollback_min_samples": effect.RollbackMinSamples,
	}
	if effect.Review != nil && effect.Review.RollbackAppliedAt != nil {
		metadata["rollback_applied_at"] = effect.Review.RollbackAppliedAt
		metadata["rollback_applied_by"] = effect.Review.RollbackAppliedBy
		metadata["rollback_budget_revision"] = effect.Review.RollbackBudgetRevision
	}
	if verification := effect.RollbackVerification; verification != nil {
		metadata["rollback_verification_status"] = verification.Status
		metadata["rollback_observation_ends_at"] = verification.ObservationEndsAt
		metadata["rollback_observation_complete"] = verification.ObservationComplete
		metadata["rollback_after_decided"] = verification.After.Decided
		metadata["rollback_after_hit_rate"] = verification.After.HitRate
		metadata["rollback_after_average_lead_time_minutes"] = verification.After.AverageLeadTimeMinutes
	}
	return metadata
}

func (s *Store) CreateBudget(ctx context.Context, item performance.Budget, reason string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO ops.performance_budgets (id,name,module_key,period,cost_limit_micros,warning_ratio,enabled,forecast_alerts_enabled,forecast_lookback_days,forecast_min_samples,effect_observation_days,effect_min_samples,revision,created_by,updated_by,created_at,updated_at) VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`, item.ID, item.Name, item.Module, item.Period, item.CostLimitMicros, item.WarningRatio, item.Enabled, item.ForecastAlertsEnabled, item.ForecastLookbackDays, item.ForecastMinSamples, item.EffectObservationDays, item.EffectMinSamples, item.Revision, item.CreatedBy, item.UpdatedBy, item.CreatedAt, item.UpdatedAt)
	if isUniqueViolation(err) {
		return performance.ErrConflict
	}
	if err != nil {
		return err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", item.CreatedBy, "performance_budget.create", "performance_budget", item.ID, reason, item.CreatedAt, map[string]any{"module": item.Module, "period": item.Period, "cost_limit_micros": item.CostLimitMicros, "forecast_alerts_enabled": item.ForecastAlertsEnabled, "forecast_lookback_days": item.ForecastLookbackDays, "forecast_min_samples": item.ForecastMinSamples, "effect_observation_days": item.EffectObservationDays, "effect_min_samples": item.EffectMinSamples}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateBudget(ctx context.Context, item performance.Budget, expectedRevision int, reason string) (_ performance.Budget, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return performance.Budget{}, err
	}
	defer func() { _ = tx.Rollback() }()
	updated, err := scanPerformanceBudget(tx.QueryRowContext(ctx, `UPDATE ops.performance_budgets SET name=$2,module_key=NULLIF($3,''),period=$4,cost_limit_micros=$5,warning_ratio=$6,enabled=$7,forecast_alerts_enabled=$8,forecast_lookback_days=$9,forecast_min_samples=$10,effect_observation_days=$11,effect_min_samples=$12,revision=revision+1,updated_by=$13,updated_at=$14 WHERE id=$1 AND revision=$15 RETURNING id::text,name,COALESCE(module_key,''),period,cost_limit_micros,warning_ratio,enabled,forecast_alerts_enabled,forecast_lookback_days,forecast_min_samples,effect_observation_days,effect_min_samples,revision,created_by,updated_by,created_at,updated_at`, item.ID, item.Name, item.Module, item.Period, item.CostLimitMicros, item.WarningRatio, item.Enabled, item.ForecastAlertsEnabled, item.ForecastLookbackDays, item.ForecastMinSamples, item.EffectObservationDays, item.EffectMinSamples, item.UpdatedBy, item.UpdatedAt, expectedRevision))
	if isUniqueViolation(err) {
		return performance.Budget{}, performance.ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if existsErr := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM ops.performance_budgets WHERE id=$1)`, item.ID).Scan(&exists); existsErr != nil {
			return performance.Budget{}, existsErr
		}
		if exists {
			return performance.Budget{}, performance.ErrConflict
		}
		return performance.Budget{}, performance.ErrNotFound
	}
	if err != nil {
		return performance.Budget{}, err
	}
	if err = insertIncidentAudit(ctx, tx, "operator", item.UpdatedBy, "performance_budget.update", "performance_budget", item.ID, reason, item.UpdatedAt, map[string]any{"module": item.Module, "period": item.Period, "cost_limit_micros": item.CostLimitMicros, "enabled": item.Enabled, "forecast_alerts_enabled": item.ForecastAlertsEnabled, "forecast_lookback_days": item.ForecastLookbackDays, "forecast_min_samples": item.ForecastMinSamples, "effect_observation_days": item.EffectObservationDays, "effect_min_samples": item.EffectMinSamples, "revision": updated.Revision}); err != nil {
		return performance.Budget{}, err
	}
	if item.Enabled && item.ForecastAlertsEnabled {
		var decisionID, recommendationKey string
		applyErr := tx.QueryRowContext(ctx, `
			UPDATE ops.performance_budget_recommendation_decisions SET applied_at=$1,applied_by=$2,applied_budget_revision=$3,effect_observation_days=$4,effect_min_samples=$5
			WHERE id=(
				SELECT id FROM ops.performance_budget_recommendation_decisions
				WHERE budget_id=$6 AND decision='accepted' AND applied_at IS NULL AND recommendation_budget_revision=$7
					AND proposed_lookback_days=$8 AND proposed_min_samples=$9
				ORDER BY decided_at DESC LIMIT 1
			)
			RETURNING id::text,recommendation_key`, item.UpdatedAt, item.UpdatedBy, updated.Revision, item.EffectObservationDays, item.EffectMinSamples, item.ID, expectedRevision, item.ForecastLookbackDays, item.ForecastMinSamples).Scan(&decisionID, &recommendationKey)
		if applyErr != nil && !errors.Is(applyErr, sql.ErrNoRows) {
			return performance.Budget{}, applyErr
		}
		if applyErr == nil {
			if err = insertIncidentAudit(ctx, tx, "operator", item.UpdatedBy, "performance_budget.recommendation.applied", "performance_budget_recommendation", decisionID, reason, item.UpdatedAt, map[string]any{"budget_id": item.ID, "recommendation_key": recommendationKey, "budget_revision": updated.Revision, "effect_observation_days": item.EffectObservationDays, "effect_min_samples": item.EffectMinSamples}); err != nil {
				return performance.Budget{}, err
			}
		}
		var rollbackDecisionID, rollbackRecommendationKey string
		var rollbackAppliedRevision, originalLookbackDays, originalMinSamples, appliedLookbackDays, appliedMinSamples int
		rollbackErr := tx.QueryRowContext(ctx, `
			UPDATE ops.performance_budget_recommendation_decisions SET rollback_applied_at=$1,rollback_applied_by=$2,rollback_budget_revision=$3
			WHERE id=(
				SELECT id FROM ops.performance_budget_recommendation_decisions
				WHERE budget_id=$4 AND applied_budget_revision=$5
					AND current_lookback_days=$6 AND current_min_samples=$7
					AND effect_review_status='acknowledged' AND effect_disposition='rollback_planned'
					AND rollback_applied_at IS NULL
				ORDER BY effect_reviewed_at DESC LIMIT 1
			)
			RETURNING id::text,recommendation_key,applied_budget_revision,current_lookback_days,current_min_samples,proposed_lookback_days,proposed_min_samples`,
			item.UpdatedAt, item.UpdatedBy, updated.Revision, item.ID, expectedRevision, item.ForecastLookbackDays, item.ForecastMinSamples,
		).Scan(&rollbackDecisionID, &rollbackRecommendationKey, &rollbackAppliedRevision, &originalLookbackDays, &originalMinSamples, &appliedLookbackDays, &appliedMinSamples)
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrNoRows) {
			return performance.Budget{}, rollbackErr
		}
		if rollbackErr == nil {
			if err = insertIncidentAudit(ctx, tx, "operator", item.UpdatedBy, "performance_budget.effect.rollback.applied", "performance_budget_recommendation", rollbackDecisionID, reason, item.UpdatedAt, map[string]any{
				"budget_id": item.ID, "recommendation_key": rollbackRecommendationKey,
				"applied_budget_revision": rollbackAppliedRevision, "rollback_budget_revision": updated.Revision,
				"applied_lookback_days": appliedLookbackDays, "applied_min_samples": appliedMinSamples,
				"restored_lookback_days": originalLookbackDays, "restored_min_samples": originalMinSamples,
			}); err != nil {
				return performance.Budget{}, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return performance.Budget{}, err
	}
	return updated, nil
}

func (s *Store) ReconcileBudgetIncident(ctx context.Context, status performance.BudgetStatus, now time.Time) (_ string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var revision int
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM ops.performance_budgets WHERE id=$1 FOR UPDATE`, status.ID).Scan(&revision); errors.Is(err, sql.ErrNoRows) {
		return "", performance.ErrNotFound
	} else if err != nil {
		return "", err
	}
	if revision != status.Revision {
		return "", performance.ErrConflict
	}
	signal := status.AlertStatus
	if signal == "" {
		signal = status.Status
	}
	current, scanErr := scanIncident(tx.QueryRowContext(ctx, incidentSelect+` WHERE i.source_type='performance_budget' AND i.source_id=$1 AND i.status IN ('open','acknowledged') FOR UPDATE OF i`, status.ID))
	found := scanErr == nil
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return "", scanErr
	}
	if found && !current.WindowStartedAt.Equal(status.WindowFrom) {
		if err = resolveBudgetIncidentTx(ctx, tx, &current, status, "budget period rolled over", now); err != nil {
			return "", err
		}
		found = false
	}
	alerting := status.Enabled && (signal == "warning" || signal == "exceeded" || signal == "projected_exceeded")
	if !alerting {
		if !found {
			if err = tx.Commit(); err != nil {
				return "", err
			}
			return "steady", nil
		}
		reason := "budget usage returned below warning threshold"
		if !status.Enabled {
			reason = "budget disabled"
		} else if strings.HasSuffix(current.EventPrefix, ".projected_exceeded") {
			reason = "budget forecast returned below projected limit"
		}
		if err = resolveBudgetIncidentTx(ctx, tx, &current, status, reason, now); err != nil {
			return "", err
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return "resolved", nil
	}

	severity, level := "warning", "WARN"
	threshold := int(math.Round(status.WarningRatio * 100))
	if signal == "exceeded" {
		severity, level, threshold = "critical", "ERROR", 100
	}
	observed := budgetUtilizationPercent(status.Utilization)
	if signal == "projected_exceeded" && status.Forecast != nil {
		observed, threshold = budgetUtilizationPercent(status.Forecast.ProjectedUtilization), 100
	}
	module := status.Module
	if module == "" {
		module = "all"
	}
	title := "成本预算接近上限：" + status.Name
	if severity == "critical" {
		title = "成本预算已超限：" + status.Name
	}
	summary := fmt.Sprintf("%s · %s预算已使用 %s / %s（%d%%）", module, status.Period, formatBudgetCost(status.UsedCostMicros), formatBudgetCost(status.CostLimitMicros), observed)
	if signal == "projected_exceeded" && status.Forecast != nil {
		title = "成本预算预计将超限：" + status.Name
		summary = fmt.Sprintf("%s · 按近期消耗速度预计周期末使用 %s / %s（%d%%），需节省 %s", module, formatBudgetCost(status.Forecast.ProjectedCostMicros), formatBudgetCost(status.CostLimitMicros), observed, formatBudgetCost(status.Forecast.RequiredSavingsMicros))
	}
	evidence := budgetIncidentEvidence(status, signal)
	windowMinutes := budgetWindowMinutes(status)
	if found {
		previousSeverity := current.Severity
		_, err = tx.ExecContext(ctx, `UPDATE ops.incidents SET severity=$1,title=$2,summary=$3,service=$4,level=$5,event_prefix=$6,observed_value=$7,threshold=$8,window_minutes=$9,window_started_at=$10,window_ended_at=$11,evidence=$12,updated_at=$11 WHERE id=$13`, severity, title, summary, module, level, "performance.budget."+signal, observed, threshold, windowMinutes, status.WindowFrom, now, evidence, current.ID)
		if err != nil {
			return "", err
		}
		transition := "updated"
		if previousSeverity == "warning" && severity == "critical" {
			transition = "escalated"
			current.Severity, current.Title, current.Summary, current.Service, current.Level, current.EventPrefix = severity, title, summary, module, level, "performance.budget."+signal
			current.ObservedValue, current.Threshold, current.WindowMinutes, current.WindowStartedAt, current.WindowEndedAt, current.Evidence, current.UpdatedAt = observed, threshold, windowMinutes, status.WindowFrom, now, evidence, now
			if err = insertIncidentAudit(ctx, tx, "system", "budget-evaluator", "incident.escalated", "incident", current.ID, "budget limit exceeded", now, map[string]any{"budget_id": status.ID, "utilization": status.Utilization}); err != nil {
				return "", err
			}
			if _, err = queueIncidentNotificationsTx(ctx, tx, current, transition, now); err != nil {
				return "", err
			}
		}
		if err = tx.Commit(); err != nil {
			return "", err
		}
		return transition, nil
	}

	incidentID, idErr := id.New()
	if idErr != nil {
		return "", idErr
	}
	current = incident.Incident{
		ID: incidentID, RuleName: status.Name, SourceType: "performance_budget", SourceID: status.ID,
		Status: "open", Severity: severity, Title: title, Summary: summary, Service: module, Level: level,
		EventPrefix: "performance.budget." + signal, ObservedValue: observed, Threshold: threshold,
		WindowMinutes: windowMinutes, WindowStartedAt: status.WindowFrom, WindowEndedAt: now, Evidence: evidence, OpenedAt: now, UpdatedAt: now,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ops.incidents (id,rule_id,source_type,source_id,status,severity,title,summary,service,level,event_prefix,observed_value,threshold,window_minutes,window_started_at,window_ended_at,evidence,opened_at,updated_at) VALUES ($1,NULL,'performance_budget',$2,'open',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$13,$13)`, current.ID, status.ID, severity, title, summary, module, level, current.EventPrefix, observed, threshold, windowMinutes, status.WindowFrom, now, evidence)
	if err != nil {
		return "", err
	}
	openReason := "budget threshold reached"
	if signal == "projected_exceeded" {
		openReason = "budget forecast projects limit exceeded"
	}
	openMetadata := map[string]any{"budget_id": status.ID, "budget_revision": status.Revision, "status": signal, "utilization": status.Utilization, "forecast_lookback_days": status.ForecastLookbackDays, "forecast_min_samples": status.ForecastMinSamples}
	if signal == "projected_exceeded" && status.Forecast != nil {
		openMetadata["projected_utilization"] = status.Forecast.ProjectedUtilization
		openMetadata["projected_cost_micros"] = status.Forecast.ProjectedCostMicros
		openMetadata["forecast_sample_size"] = status.Forecast.SampleSize
		openMetadata["projected_limit_exceeded_at"] = status.Forecast.ProjectedLimitExceededAt
	}
	if err = insertIncidentAudit(ctx, tx, "system", "budget-evaluator", "incident.opened", "incident", current.ID, openReason, now, openMetadata); err != nil {
		return "", err
	}
	if _, err = queueIncidentNotificationsTx(ctx, tx, current, "opened", now); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return "opened", nil
}

func resolveBudgetIncidentTx(ctx context.Context, tx *sql.Tx, current *incident.Incident, status performance.BudgetStatus, reason string, now time.Time) error {
	signal := status.AlertStatus
	if signal == "" {
		signal = status.Status
	}
	evidence := budgetIncidentEvidence(status, signal)
	observed := budgetUtilizationPercent(status.Utilization)
	_, err := tx.ExecContext(ctx, `UPDATE ops.incidents SET status='resolved',observed_value=$1,window_ended_at=$2,evidence=$3,resolved_at=$2,resolved_by='system',resolution=$4,updated_at=$2 WHERE id=$5`, observed, now, evidence, reason, current.ID)
	if err != nil {
		return err
	}
	current.Status, current.ObservedValue, current.WindowEndedAt, current.Evidence, current.ResolvedAt, current.ResolvedBy, current.Resolution, current.UpdatedAt = "resolved", observed, now, evidence, &now, "system", reason, now
	if err = insertIncidentAudit(ctx, tx, "system", "budget-evaluator", "incident.auto_resolved", "incident", current.ID, reason, now, map[string]any{"budget_id": status.ID, "utilization": status.Utilization}); err != nil {
		return err
	}
	_, err = queueIncidentNotificationsTx(ctx, tx, *current, "resolved", now)
	return err
}

func budgetIncidentEvidence(status performance.BudgetStatus, signal string) []byte {
	payload := map[string]any{
		"source": "ops.performance_budgets", "signal_status": signal,
		"budget_id": status.ID, "budget_name": status.Name, "module": status.Module, "period": status.Period,
		"used_cost_micros": status.UsedCostMicros, "cost_limit_micros": status.CostLimitMicros,
		"warning_ratio": status.WarningRatio, "utilization": status.Utilization,
		"forecast_alerts_enabled": status.ForecastAlertsEnabled, "forecast_lookback_days": status.ForecastLookbackDays,
		"forecast_min_samples": status.ForecastMinSamples,
	}
	if status.Forecast != nil {
		payload["forecast_risk"] = status.Forecast.Risk
		payload["forecast_confidence"] = status.Forecast.Confidence
		payload["forecast_sample_size"] = status.Forecast.SampleSize
		payload["forecast_observed_hours"] = status.Forecast.ObservedHours
		payload["burn_rate_micros_per_hour"] = status.Forecast.BurnRateMicrosPerHour
		payload["projected_cost_micros"] = status.Forecast.ProjectedCostMicros
		payload["projected_utilization"] = status.Forecast.ProjectedUtilization
		payload["required_savings_micros"] = status.Forecast.RequiredSavingsMicros
		payload["period_ends_at"] = status.Forecast.PeriodEndsAt
		payload["projected_limit_exceeded_at"] = status.Forecast.ProjectedLimitExceededAt
	}
	evidence, _ := json.Marshal(payload)
	return evidence
}

func budgetUtilizationPercent(value float64) int {
	if value <= 0 || math.IsNaN(value) {
		return 0
	}
	return int(math.Min(math.Round(value*100), math.MaxInt32))
}

func budgetWindowMinutes(status performance.BudgetStatus) int {
	if status.Period == "daily" {
		return 24 * 60
	}
	next := status.WindowFrom.AddDate(0, 1, 0)
	return int(next.Sub(status.WindowFrom).Minutes())
}

func formatBudgetCost(micros int64) string {
	return fmt.Sprintf("$%.4f", float64(micros)/1_000_000)
}

const performanceBudgetSelect = `SELECT id::text,name,COALESCE(module_key,''),period,cost_limit_micros,warning_ratio,enabled,forecast_alerts_enabled,forecast_lookback_days,forecast_min_samples,effect_observation_days,effect_min_samples,revision,created_by,updated_by,created_at,updated_at FROM ops.performance_budgets`

const budgetPolicyDecisionSelect = `SELECT id::text,budget_id::text,recommendation_key,action,decision,confidence,outcome_count,hit_rate,average_lead_time_minutes,current_lookback_days,current_min_samples,proposed_lookback_days,proposed_min_samples,recommendation_reason,operator_reason,decided_by,decided_at,COALESCE(recommendation_budget_revision,0),applied_at,COALESCE(applied_by,''),COALESCE(applied_budget_revision,0),COALESCE(effect_observation_days,0),COALESCE(effect_min_samples,0),COALESCE(effect_review_status,''),COALESCE(effect_disposition,''),COALESCE(effect_review_reason,''),COALESCE(effect_reviewed_by,''),effect_reviewed_at,rollback_applied_at,COALESCE(rollback_applied_by,''),COALESCE(rollback_budget_revision,0),COALESCE(effect_closed_reason,''),COALESCE(effect_closed_by,''),effect_closed_at FROM ops.performance_budget_recommendation_decisions`

func scanPerformanceBudget(row rowScanner) (performance.Budget, error) {
	var item performance.Budget
	err := row.Scan(&item.ID, &item.Name, &item.Module, &item.Period, &item.CostLimitMicros, &item.WarningRatio, &item.Enabled, &item.ForecastAlertsEnabled, &item.ForecastLookbackDays, &item.ForecastMinSamples, &item.EffectObservationDays, &item.EffectMinSamples, &item.Revision, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func scanBudgetPolicyDecision(row rowScanner) (performance.BudgetPolicyDecision, error) {
	var item performance.BudgetPolicyDecision
	var review performance.BudgetPolicyEffectReview
	var reviewedAt, rollbackAppliedAt, closedAt sql.NullTime
	err := row.Scan(&item.ID, &item.BudgetID, &item.RecommendationKey, &item.Action, &item.Decision, &item.Confidence, &item.OutcomeCount, &item.HitRate, &item.AverageLeadTimeMinutes, &item.CurrentLookbackDays, &item.CurrentMinSamples, &item.ProposedLookbackDays, &item.ProposedMinSamples, &item.RecommendationReason, &item.Reason, &item.DecidedBy, &item.DecidedAt, &item.BudgetRevision, &item.AppliedAt, &item.AppliedBy, &item.AppliedBudgetRevision, &item.EffectObservationDays, &item.EffectMinSamples, &review.Status, &review.Disposition, &review.Reason, &review.ReviewedBy, &reviewedAt, &rollbackAppliedAt, &review.RollbackAppliedBy, &review.RollbackBudgetRevision, &review.ClosedReason, &review.ClosedBy, &closedAt)
	if err == nil && review.Status != "" && reviewedAt.Valid {
		review.ReviewedAt = reviewedAt.Time
		if rollbackAppliedAt.Valid {
			review.RollbackAppliedAt = &rollbackAppliedAt.Time
		}
		if closedAt.Valid {
			review.ClosedAt = &closedAt.Time
		}
		item.EffectReview = &review
	}
	return item, err
}

func setVersionRates(item *performance.VersionMetrics) {
	if item.SampleSize == 0 {
		return
	}
	item.SuccessRate = float64(item.Completed) / float64(item.SampleSize)
	item.ErrorRate = float64(item.Failed) / float64(item.SampleSize)
	item.QualityFailureRate = float64(item.QualityFailures) / float64(item.SampleSize)
	item.AverageCostMicros = float64(item.CostMicros) / float64(item.SampleSize)
}

func versionMetricLabel(item performance.VersionMetrics) string {
	if item.Dimension == performance.DimensionAgentVersion && item.Version > 0 {
		return fmt.Sprintf("%s · v%d", item.Key, item.Version)
	}
	if item.ConfigVersion != "" {
		return fmt.Sprintf("%s · %s", item.Key, item.ConfigVersion)
	}
	if item.Revision > 0 {
		return fmt.Sprintf("%s · r%d", item.Key, item.Revision)
	}
	return item.Key
}
