package reliability

import (
	"testing"
	"time"
)

func TestControllerRequiresConsecutiveSamplesAndRecoversStepwise(t *testing.T) {
	controller := NewController(Config{EnterSamples: 2, RecoverSamples: 2, MinDwell: time.Minute})
	base := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	heavy := Sample{QueueLag: 600, OldestJobAge: 70 * time.Second}
	if got := controller.Observe(heavy, base); got.Level != "L0" || got.CandidateSamples != 1 {
		t.Fatalf("first heavy sample = %#v", got)
	}
	if got := controller.Observe(heavy, base.Add(time.Second)); got.Level != "L2" {
		t.Fatalf("entered level = %#v", got)
	}
	healthy := Sample{}
	if got := controller.Observe(healthy, base.Add(30*time.Second)); got.Level != "L2" || got.CandidateSamples != 0 {
		t.Fatalf("recovered before minimum dwell = %#v", got)
	}
	if got := controller.Observe(healthy, base.Add(62*time.Second)); got.Level != "L2" || got.CandidateLevel != "L1" {
		t.Fatalf("first recovery sample = %#v", got)
	}
	if got := controller.Observe(healthy, base.Add(63*time.Second)); got.Level != "L1" {
		t.Fatalf("first recovery step = %#v", got)
	}
	if got := controller.Observe(healthy, base.Add(124*time.Second)); got.Level != "L1" {
		t.Fatalf("second recovery candidate = %#v", got)
	}
	if got := controller.Observe(healthy, base.Add(125*time.Second)); got.Level != "L0" {
		t.Fatalf("fully recovered = %#v", got)
	}
}

func TestControllerResetsFlappingCandidateAndL3PreservesRequests(t *testing.T) {
	controller := NewController(Config{EnterSamples: 3, RecoverSamples: 2, MinDwell: time.Second})
	now := time.Now().UTC()
	controller.Observe(Sample{QueueLag: 120}, now)
	if got := controller.Observe(Sample{}, now.Add(time.Second)); got.CandidateSamples != 0 || got.Level != "L0" {
		t.Fatalf("flapping candidate = %#v", got)
	}
	critical := Sample{ModelErrorRate: 0.95}
	controller.Observe(critical, now.Add(2*time.Second))
	controller.Observe(critical, now.Add(3*time.Second))
	got := controller.Observe(critical, now.Add(4*time.Second))
	if got.Level != "L3" || !got.Policy.AcceptOnly || got.Policy.PreferredModelClass != "local_only" {
		t.Fatalf("L3 snapshot = %#v", got)
	}
}

func TestControllerPreservesAgentMetricsInSnapshot(t *testing.T) {
	controller := NewController(Config{})
	snapshot := controller.Observe(Sample{
		AgentRuns:    AgentRunMetrics{Running: 2, CancelRequested: 1, P95DurationMS: 800},
		AgentRetries: AgentRetryMetrics{ScheduledRecent: 3, RecoveredRecent: 2, ExhaustedRecent: 1, RecoveryRatio: 2.0 / 3.0},
		ModelUsage:   ModelUsageMetrics{Calls: 4, PromptTokens: 120, CompletionTokens: 30, CostMicros: 9},
		Repairs:      RepairMetrics{Attempts: 2, Succeeded: 1, Blocked: 1, ModelCalls: 1, ModelCostMicros: 5},
	}, time.Now())
	if snapshot.AgentRuns.Running != 2 ||
		snapshot.AgentRuns.CancelRequested != 1 ||
		snapshot.AgentRuns.P95DurationMS != 800 {
		t.Fatalf("agent metrics = %#v", snapshot.AgentRuns)
	}
	if snapshot.ModelUsage.Calls != 4 ||
		snapshot.ModelUsage.PromptTokens != 120 ||
		snapshot.ModelUsage.CompletionTokens != 30 ||
		snapshot.ModelUsage.CostMicros != 9 {
		t.Fatalf("model usage metrics = %#v", snapshot.ModelUsage)
	}
	if snapshot.Repairs.Attempts != 2 || snapshot.Repairs.Succeeded != 1 || snapshot.Repairs.ModelCostMicros != 5 {
		t.Fatalf("repair metrics = %#v", snapshot.Repairs)
	}
	if snapshot.AgentRetries.ScheduledRecent != 3 || snapshot.AgentRetries.RecoveredRecent != 2 ||
		snapshot.AgentRetries.ExhaustedRecent != 1 || snapshot.AgentRetries.RecoveryRatio != 2.0/3.0 {
		t.Fatalf("agent retry metrics = %#v", snapshot.AgentRetries)
	}
}
