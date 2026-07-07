package mysqlstore

import (
	"context"
	"time"

	"github.com/windcry1/ai-companion/internal/reliability"
)

func (s *Store) ReliabilitySample(ctx context.Context, now time.Time) (reliability.Sample, error) {
	now = now.UTC()
	var skillLag, documentLag, cleanupLag, chatLag, ledgerLag, notificationLag, outboxLag int
	var skillAgeMicros, documentAgeMicros, cleanupAgeMicros, chatAgeMicros, ledgerAgeMicros, notificationAgeMicros, outboxAgeMicros int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,available_at,?))),0) FROM skill_runs WHERE execution_mode='worker' AND (status='queued' OR (status='running' AND lease_expires_at<=?))`, now, now).Scan(&skillLag, &skillAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,available_at,?))),0) FROM document_ingest_jobs WHERE status='queued' OR (status='processing' AND lease_expires_at<=?)`, now, now).Scan(&documentLag, &documentAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,available_at,?))),0) FROM document_cleanup_jobs WHERE available_at<=? AND (status='queued' OR (status='processing' AND lease_expires_at<=?))`, now, now, now).Scan(&cleanupLag, &cleanupAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,available_at,?))),0) FROM generation_jobs WHERE available_at<=? AND (status='accepted' OR (status='running' AND lease_expires_at<=?))`, now, now, now).Scan(&chatLag, &chatAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,available_at,?))),0) FROM ledger_exports WHERE available_at<=? AND (status='queued' OR (status='processing' AND lease_expires_at<=?))`, now, now, now).Scan(&ledgerLag, &ledgerAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,scheduled_at,?))),0) FROM notification_deliveries WHERE status='queued' AND scheduled_at<=?`, now, now).Scan(&notificationLag, &notificationAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(GREATEST(0,TIMESTAMPDIFF(MICROSECOND,available_at,?))),0) FROM outbox_events WHERE status='pending' OR (status='publishing' AND lease_expires_at<=?)`, now, now).Scan(&outboxLag, &outboxAgeMicros); err != nil {
		return reliability.Sample{}, err
	}
	windowStart := now.Add(-5 * time.Minute)
	var total, failed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(status IN ('failed','timed_out')),0) FROM generation_jobs WHERE created_at>=? AND status IN ('completed','failed','timed_out')`, windowStart).Scan(&total, &failed); err != nil {
		return reliability.Sample{}, err
	}
	var p95LatencyMS int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(latency_ms),0) FROM (SELECT latency_ms,CUME_DIST() OVER (ORDER BY latency_ms) AS percentile_rank FROM model_usage_records WHERE created_at>=?) ranked WHERE percentile_rank>=0.95`, windowStart).Scan(&p95LatencyMS); err != nil {
		return reliability.Sample{}, err
	}
	errorRate := 0.0
	if total > 0 {
		errorRate = float64(failed) / float64(total)
	}
	oldestMicros := int64(0)
	for _, age := range []int64{skillAgeMicros, documentAgeMicros, cleanupAgeMicros, chatAgeMicros, ledgerAgeMicros, notificationAgeMicros, outboxAgeMicros} {
		if age > oldestMicros {
			oldestMicros = age
		}
	}
	return reliability.Sample{
		QueueLag: skillLag + documentLag + cleanupLag + chatLag + ledgerLag + notificationLag + outboxLag, OldestJobAge: time.Duration(oldestMicros) * time.Microsecond,
		ModelErrorRate: errorRate, P95Latency: time.Duration(p95LatencyMS) * time.Millisecond,
	}, nil
}
