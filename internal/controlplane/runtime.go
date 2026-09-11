package controlplane

import (
	"context"
	"strings"
	"time"
)

const (
	RuntimeStatusApplied = "applied"
	RuntimeStatusError   = "error"
)

type RuntimeConfigReport struct {
	InstanceID  string    `json:"instance_id"`
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	Kind        string    `json:"kind"`
	Key         string    `json:"key"`
	VersionID   string    `json:"version_id,omitempty"`
	Revision    int       `json:"revision,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Status      string    `json:"status"`
	LastError   string    `json:"last_error,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

type RuntimeConfigState struct {
	RuntimeConfigReport
	DesiredVersionID   string `json:"desired_version_id,omitempty"`
	DesiredRevision    int    `json:"desired_revision,omitempty"`
	DesiredFingerprint string `json:"desired_fingerprint,omitempty"`
	Convergence        string `json:"convergence"`
}

type RuntimeConvergence struct {
	Environment string               `json:"environment"`
	GeneratedAt time.Time            `json:"generated_at"`
	StaleAfter  int                  `json:"stale_after_seconds"`
	Summary     map[string]int       `json:"summary"`
	Instances   []RuntimeConfigState `json:"instances"`
}

type RuntimeConfigStore interface {
	UpsertRuntimeConfigReport(context.Context, RuntimeConfigReport) (RuntimeConfigReport, error)
	ListRuntimeConfigReports(context.Context, string, time.Time, int) ([]RuntimeConfigReport, error)
}

func (s *Service) ReportRuntimeConfig(ctx context.Context, report RuntimeConfigReport) (RuntimeConfigReport, error) {
	report.InstanceID = strings.TrimSpace(report.InstanceID)
	report.Service = strings.ToLower(strings.TrimSpace(report.Service))
	report.Environment = s.environment
	report.Kind = normalizeKind(report.Kind)
	report.Key = normalizeKey(report.Key)
	report.VersionID = strings.TrimSpace(report.VersionID)
	report.Fingerprint = strings.ToLower(strings.TrimSpace(report.Fingerprint))
	report.Status = strings.ToLower(strings.TrimSpace(report.Status))
	report.LastError = strings.TrimSpace(report.LastError)
	if report.InstanceID == "" || len(report.InstanceID) > 128 || report.Service == "" || len(report.Service) > 64 ||
		report.Kind == "" || !validKey(report.Key) || (report.Status != RuntimeStatusApplied && report.Status != RuntimeStatusError) ||
		(report.Status == RuntimeStatusApplied && (report.VersionID == "" || report.Revision <= 0 || len(report.Fingerprint) != 64)) || len(report.LastError) > 1024 {
		return RuntimeConfigReport{}, ErrValidation
	}
	store, ok := s.store.(RuntimeConfigStore)
	if !ok {
		return RuntimeConfigReport{}, ErrNotFound
	}
	now := s.now().UTC()
	if report.StartedAt.IsZero() {
		report.StartedAt = now
	}
	report.LastSeenAt = now
	return store.UpsertRuntimeConfigReport(ctx, report)
}

func (s *Service) RuntimeConvergence(ctx context.Context, staleAfter time.Duration) (RuntimeConvergence, error) {
	if staleAfter < 15*time.Second || staleAfter > time.Hour {
		staleAfter = 2 * time.Minute
	}
	store, ok := s.store.(RuntimeConfigStore)
	if !ok {
		return RuntimeConvergence{}, ErrNotFound
	}
	now := s.now().UTC()
	reports, err := store.ListRuntimeConfigReports(ctx, s.environment, now.Add(-24*time.Hour), 1000)
	if err != nil {
		return RuntimeConvergence{}, err
	}
	desired := map[string]Deployment{}
	for _, kind := range []string{KindBillingPlan, KindModelProfile, KindAgentDefinition, KindPrompt} {
		deployments, listErr := s.Active(ctx, kind)
		if listErr != nil {
			return RuntimeConvergence{}, listErr
		}
		for _, deployment := range deployments {
			desired[kind+"\x00"+deployment.Key] = deployment
		}
	}
	states := make([]RuntimeConfigState, 0, len(reports))
	summary := map[string]int{"converged": 0, "outdated": 0, "error": 0, "offline": 0}
	for _, report := range reports {
		state := RuntimeConfigState{RuntimeConfigReport: report, Convergence: "outdated"}
		deployment, exists := desired[report.Kind+"\x00"+report.Key]
		if exists {
			state.DesiredVersionID = deployment.Version.ID
			state.DesiredRevision = deployment.Revision
			state.DesiredFingerprint = deployment.Version.Fingerprint
		}
		switch {
		case now.Sub(report.LastSeenAt) > staleAfter:
			state.Convergence = "offline"
		case report.Status == RuntimeStatusError:
			state.Convergence = "error"
		case exists && report.VersionID == deployment.Version.ID && report.Revision == deployment.Revision && report.Fingerprint == deployment.Version.Fingerprint:
			state.Convergence = "converged"
		}
		summary[state.Convergence]++
		states = append(states, state)
	}
	return RuntimeConvergence{
		Environment: s.environment, GeneratedAt: now, StaleAfter: int(staleAfter.Seconds()),
		Summary: summary, Instances: states,
	}, nil
}
