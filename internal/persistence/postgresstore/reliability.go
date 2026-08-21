package postgresstore

import (
	"context"
	"time"

	"github.com/windcry1/ai-companion/internal/reliability"
)

func (s *Store) ReliabilitySample(ctx context.Context, now time.Time) (reliability.Sample, error) {
	now = now.UTC()
	var skillLag, documentLag, cleanupLag, chatLag, agentLag, ledgerLag, notificationLag, outboxLag int
	var skillAgeMicros, documentAgeMicros, cleanupAgeMicros, chatAgeMicros, agentAgeMicros, ledgerAgeMicros, notificationAgeMicros, outboxAgeMicros int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM app.skill_runs
		WHERE execution_mode='worker'
			AND (status='queued' OR (status='running' AND lease_expires_at<=$1))`,
		now,
	).Scan(&skillLag, &skillAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM app.document_ingest_jobs
		WHERE status='queued' OR (status='processing' AND lease_expires_at<=$1)`,
		now,
	).Scan(&documentLag, &documentAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM app.document_cleanup_jobs
		WHERE available_at<=$1
			AND (status='queued' OR (status='processing' AND lease_expires_at<=$1))`,
		now,
	).Scan(&cleanupLag, &cleanupAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM app.generation_jobs
		WHERE available_at<=$1
			AND (status='accepted' OR (status='running' AND lease_expires_at<=$1))`,
		now,
	).Scan(&chatLag, &chatAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM agent.runs
		WHERE available_at<=$1
			AND (
				status IN ('accepted','queued')
				OR (status='running' AND lease_expires_at<=$1)
			)`,
		now,
	).Scan(&agentLag, &agentAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM app.ledger_exports
		WHERE available_at<=$1
			AND (status='queued' OR (status='processing' AND lease_expires_at<=$1))`,
		now,
	).Scan(&ledgerLag, &ledgerAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-scheduled_at))*1000000)),0)::bigint
		FROM app.notification_deliveries
		WHERE status='queued' AND scheduled_at<=$1`,
		now,
	).Scan(&notificationLag, &notificationAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
			COALESCE(MAX(GREATEST(0,EXTRACT(EPOCH FROM ($1-available_at))*1000000)),0)::bigint
		FROM eventing.outbox_events
		WHERE status='pending' OR (status='publishing' AND lease_expires_at<=$1)`,
		now,
	).Scan(&outboxLag, &outboxAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	windowStart := now.Add(-5 * time.Minute)
	var total, failed int
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),COUNT(*) FILTER (WHERE status IN ('failed','timed_out'))
		FROM app.generation_jobs
		WHERE created_at>=$1 AND status IN ('completed','failed','timed_out')`,
		windowStart,
	).Scan(&total, &failed); err != nil {
		return reliability.Sample{}, err
	}
	var agentTotal, agentFailed int
	var agentRuns reliability.AgentRunMetrics
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status='accepted'),
			COUNT(*) FILTER (WHERE status='queued'),
			COUNT(*) FILTER (WHERE status='running'),
			COUNT(*) FILTER (WHERE status='waiting_approval'),
			COUNT(*) FILTER (WHERE status='cancel_requested'),
			COUNT(*) FILTER (WHERE status='completed' AND completed_at>=$1),
			COUNT(*) FILTER (WHERE status='failed' AND completed_at>=$1),
			COUNT(*) FILTER (WHERE status='cancelled' AND completed_at>=$1),
			COUNT(*) FILTER (WHERE status='timed_out' AND completed_at>=$1),
			COUNT(*) FILTER (
				WHERE status IN ('completed','failed','timed_out') AND completed_at>=$1
			),
			COUNT(*) FILTER (
				WHERE status IN ('failed','timed_out') AND completed_at>=$1
			),
			COALESCE(
				percentile_cont(0.95) WITHIN GROUP (
					ORDER BY EXTRACT(EPOCH FROM (completed_at-created_at))*1000
				) FILTER (
					WHERE status IN ('completed','failed','cancelled','timed_out')
						AND completed_at>=$1
				),
				0
			)::bigint
		FROM agent.runs`,
		windowStart,
	).Scan(
		&agentRuns.Accepted,
		&agentRuns.Queued,
		&agentRuns.Running,
		&agentRuns.WaitingApproval,
		&agentRuns.CancelRequested,
		&agentRuns.CompletedRecent,
		&agentRuns.FailedRecent,
		&agentRuns.CancelledRecent,
		&agentRuns.TimedOutRecent,
		&agentTotal,
		&agentFailed,
		&agentRuns.P95DurationMS,
	); err != nil {
		return reliability.Sample{}, err
	}
	var agentRetries reliability.AgentRetryMetrics
	if err := s.db.QueryRowContext(ctx, `
		WITH retrying_runs AS (
			SELECT r.id,r.status,r.error_code,r.completed_at
			FROM agent.runs r
			WHERE r.completed_at>=$1
				AND (
					r.error_code IN (
						'execution_retry_budget_exhausted',
						'execution_retry_deadline_exhausted'
					) OR EXISTS (
						SELECT 1
						FROM agent.run_events e
						WHERE e.run_id=r.id AND e.event_type='queued'
							AND (
								e.payload->>'retry_kind'='execution'
								OR e.payload ? 'reason'
							)
					)
				)
		)
		SELECT
			(
				SELECT COUNT(*)
				FROM agent.run_events e
				WHERE e.event_type='queued' AND e.created_at>=$1
					AND (
						e.payload->>'retry_kind'='execution'
						OR e.payload ? 'reason'
					)
			),
			COUNT(*) FILTER (WHERE status='completed' AND completed_at>=$1),
			COUNT(*) FILTER (
				WHERE status='failed' AND completed_at>=$1
					AND error_code='execution_retry_budget_exhausted'
			),
			COUNT(*) FILTER (
				WHERE status='timed_out' AND completed_at>=$1
			)
		FROM retrying_runs`,
		windowStart,
	).Scan(
		&agentRetries.ScheduledRecent,
		&agentRetries.RecoveredRecent,
		&agentRetries.ExhaustedRecent,
		&agentRetries.DeadlineExhaustedRecent,
	); err != nil {
		return reliability.Sample{}, err
	}
	settledRetries := agentRetries.RecoveredRecent + agentRetries.ExhaustedRecent + agentRetries.DeadlineExhaustedRecent
	if settledRetries > 0 {
		agentRetries.RecoveryRatio = float64(agentRetries.RecoveredRecent) / float64(settledRetries)
	}
	var repairs reliability.RepairMetrics
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM((
				SELECT COUNT(*) FROM jsonb_array_elements(
					CASE WHEN jsonb_typeof(output->'observability'->'repair_history')='array'
					THEN output->'observability'->'repair_history' ELSE '[]'::jsonb END
				) AS repair WHERE repair ? 'attempt'
			)),0)::bigint,
			COALESCE(SUM((
				SELECT COUNT(*) FROM jsonb_array_elements(
					CASE WHEN jsonb_typeof(output->'observability'->'repair_history')='array'
					THEN output->'observability'->'repair_history' ELSE '[]'::jsonb END
				) AS repair WHERE repair->>'result'='succeeded'
			)),0)::bigint,
			COALESCE(SUM((
				SELECT COUNT(*) FROM jsonb_array_elements(
					CASE WHEN jsonb_typeof(output->'observability'->'node_trace')='array'
					THEN output->'observability'->'node_trace' ELSE '[]'::jsonb END
				) AS event
				WHERE event->>'status'='blocked'
					AND event->>'node' IN ('classify_tool_failure','validate_repair','reconcile_side_effect')
			)),0)::bigint,
			COALESCE(SUM(COALESCE((output#>>'{budget,usage,repair_model_calls}')::bigint,0)),0)::bigint,
			COALESCE(SUM(COALESCE((output#>>'{budget,usage,repair_cost_micros}')::bigint,0)),0)::bigint
		FROM agent.runs
		WHERE updated_at>=$1 AND output IS NOT NULL`,
		windowStart,
	).Scan(
		&repairs.Attempts,
		&repairs.Succeeded,
		&repairs.Blocked,
		&repairs.ModelCalls,
		&repairs.ModelCostMicros,
	); err != nil {
		return reliability.Sample{}, err
	}
	var p95LatencyMS int64
	var modelUsage reliability.ModelUsageMetrics
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*)::bigint,
			COALESCE(SUM(prompt_tokens),0)::bigint,
			COALESCE(SUM(completion_tokens),0)::bigint,
			COALESCE(SUM(cost_micros),0)::bigint,
			COALESCE(
				percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms),
				0
			)::bigint
		FROM app.model_usage_observations
		WHERE observed_at>=$1`,
		windowStart,
	).Scan(
		&modelUsage.Calls,
		&modelUsage.PromptTokens,
		&modelUsage.CompletionTokens,
		&modelUsage.CostMicros,
		&p95LatencyMS,
	); err != nil {
		return reliability.Sample{}, err
	}
	errorRate := 0.0
	if total+agentTotal > 0 {
		errorRate = float64(failed+agentFailed) / float64(total+agentTotal)
	}
	oldestMicros := int64(0)
	for _, age := range []int64{
		skillAgeMicros, documentAgeMicros, cleanupAgeMicros, chatAgeMicros, agentAgeMicros,
		ledgerAgeMicros, notificationAgeMicros, outboxAgeMicros,
	} {
		if age > oldestMicros {
			oldestMicros = age
		}
	}
	return reliability.Sample{
		QueueLag:       skillLag + documentLag + cleanupLag + chatLag + agentLag + ledgerLag + notificationLag + outboxLag,
		OldestJobAge:   time.Duration(oldestMicros) * time.Microsecond,
		ModelErrorRate: errorRate,
		P95Latency:     time.Duration(p95LatencyMS) * time.Millisecond,
		AgentRuns:      agentRuns,
		AgentRetries:   agentRetries,
		ModelUsage:     modelUsage,
		Repairs:        repairs,
	}, nil
}
