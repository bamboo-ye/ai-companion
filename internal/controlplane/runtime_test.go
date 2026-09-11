package controlplane

import (
	"context"
	"testing"
	"time"
)

func TestRuntimeConvergenceTracksAppliedAndOfflineInstances(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, "development")
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	deployments, err := service.Active(context.Background(), KindModelProfile)
	if err != nil || len(deployments) == 0 {
		t.Fatalf("active deployments=%+v err=%v", deployments, err)
	}
	deployment := deployments[0]
	_, err = service.ReportRuntimeConfig(context.Background(), RuntimeConfigReport{
		InstanceID: "worker-1", Service: "agent-worker", Kind: deployment.Kind, Key: deployment.Key,
		VersionID: deployment.Version.ID, Revision: deployment.Revision,
		Fingerprint: deployment.Version.Fingerprint, Status: RuntimeStatusApplied, StartedAt: now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.RuntimeConvergence(context.Background(), 2*time.Minute)
	if err != nil || report.Summary["converged"] != 1 || len(report.Instances) != 1 || report.Instances[0].Convergence != "converged" {
		t.Fatalf("convergence=%+v err=%v", report, err)
	}
	service.now = func() time.Time { return now.Add(3 * time.Minute) }
	report, err = service.RuntimeConvergence(context.Background(), 2*time.Minute)
	if err != nil || report.Summary["offline"] != 1 || report.Instances[0].Convergence != "offline" {
		t.Fatalf("offline convergence=%+v err=%v", report, err)
	}
}

func TestRuntimeConvergenceSurfacesLoadError(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, "development")
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	_, err := service.ReportRuntimeConfig(context.Background(), RuntimeConfigReport{
		InstanceID: "worker-2", Service: "agent-worker", Kind: KindModelProfile,
		Key: DefaultModelProfileKey, Status: RuntimeStatusError, LastError: "credential unavailable", StartedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.RuntimeConvergence(context.Background(), time.Minute)
	if err != nil || report.Summary["error"] != 1 || report.Instances[0].LastError == "" {
		t.Fatalf("error convergence=%+v err=%v", report, err)
	}
}
