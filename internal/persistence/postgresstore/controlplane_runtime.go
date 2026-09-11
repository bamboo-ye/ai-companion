package postgresstore

import (
	"context"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

var _ controlplane.RuntimeConfigStore = (*Store)(nil)

func (s *Store) UpsertRuntimeConfigReport(ctx context.Context, item controlplane.RuntimeConfigReport) (controlplane.RuntimeConfigReport, error) {
	err := scanRuntimeConfigReport(s.db.QueryRowContext(ctx, `
		INSERT INTO ops.runtime_config_reports (
			instance_id,service,environment,config_kind,config_key,version_id,revision,
			fingerprint,status,last_error,started_at,last_seen_at
		) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7,NULLIF($8,''),$9,$10,$11,$12)
		ON CONFLICT (instance_id,config_kind,config_key) DO UPDATE SET
			service=EXCLUDED.service,environment=EXCLUDED.environment,version_id=EXCLUDED.version_id,
			revision=EXCLUDED.revision,fingerprint=EXCLUDED.fingerprint,status=EXCLUDED.status,
			last_error=EXCLUDED.last_error,last_seen_at=EXCLUDED.last_seen_at
		RETURNING instance_id,service,environment,config_kind,config_key,COALESCE(version_id::text,''),
			revision,COALESCE(fingerprint,''),status,last_error,started_at,last_seen_at`,
		item.InstanceID, item.Service, item.Environment, item.Kind, item.Key, item.VersionID,
		item.Revision, item.Fingerprint, item.Status, item.LastError, item.StartedAt, item.LastSeenAt,
	), &item)
	return item, err
}

func (s *Store) ListRuntimeConfigReports(ctx context.Context, environment string, since time.Time, limit int) ([]controlplane.RuntimeConfigReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id,service,environment,config_kind,config_key,
		COALESCE(version_id::text,''),revision,COALESCE(fingerprint,''),status,last_error,started_at,last_seen_at
		FROM ops.runtime_config_reports WHERE environment=$1 AND last_seen_at>=$2
		ORDER BY service,instance_id,config_kind,config_key LIMIT $3`, environment, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]controlplane.RuntimeConfigReport, 0)
	for rows.Next() {
		var item controlplane.RuntimeConfigReport
		if err = scanRuntimeConfigReport(rows, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRuntimeConfigReport(row rowScanner, item *controlplane.RuntimeConfigReport) error {
	return row.Scan(&item.InstanceID, &item.Service, &item.Environment, &item.Kind, &item.Key,
		&item.VersionID, &item.Revision, &item.Fingerprint, &item.Status, &item.LastError,
		&item.StartedAt, &item.LastSeenAt)
}
