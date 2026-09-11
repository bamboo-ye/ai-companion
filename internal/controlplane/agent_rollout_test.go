package controlplane

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestAgentRolloutRoutesStableCohortsAndUnlocksPromotion(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, "test")
	baseline := createPublishedAgentVersion(t, ctx, service, 0, "rollout-assistant", "author-a", "approver-b")
	candidate := createSubmittedAgentVersion(t, ctx, service, 1, "rollout-assistant", "author-a")
	if _, _, err := service.Publish(ctx, candidate.ID, "approver-b"); !errors.Is(err, ErrRollout) {
		t.Fatalf("Publish(without rollout) error = %v", err)
	}
	policy := DefaultAgentRolloutPolicy()
	policy.MinimumSampleSize = 2
	policy.ObservationWindowSeconds = 60
	rollout, err := service.StartAgentRollout(ctx, candidate.ID, StartAgentRolloutInput{
		TrafficPercent: 25, Policy: policy, Actor: "approver-b",
	})
	if err != nil || rollout.Status != AgentRolloutStatusRunning || rollout.BaselineVersionID != baseline.ID {
		t.Fatalf("StartAgentRollout() = %#v, %v", rollout, err)
	}

	var candidateKey, baselineKey string
	for index := 0; index < 1000 && (candidateKey == "" || baselineKey == ""); index++ {
		key := fmt.Sprintf("user-%d", index)
		snapshot, routeErr := service.RoutedAgentRuntime(ctx, "work", key)
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		if snapshot.VersionID == candidate.ID {
			candidateKey = key
		} else if snapshot.VersionID == baseline.ID {
			baselineKey = key
		}
	}
	if candidateKey == "" || baselineKey == "" {
		t.Fatalf("stable cohorts not found: candidate=%q baseline=%q", candidateKey, baselineKey)
	}
	first, _ := service.RoutedAgentRuntime(ctx, "work", candidateKey)
	second, _ := service.RoutedAgentRuntime(ctx, "work", candidateKey)
	if first.VersionID != candidate.ID || second.VersionID != candidate.ID {
		t.Fatalf("candidate cohort changed: %#v %#v", first, second)
	}

	store.SetAgentRolloutMeasurements(rollout.ID, AgentRolloutMeasurements{
		Candidate: AgentRolloutMetrics{SampleSize: 2, Completed: 2, P95LatencyMS: 900, AverageCostMicros: 1200},
	})
	collecting, err := service.RefreshAgentRollout(ctx, rollout.ID, "support-a")
	if err != nil || collecting.Status != AgentRolloutStatusRunning || collecting.Decision != AgentRolloutDecisionCollecting {
		t.Fatalf("RefreshAgentRollout(without baseline sample) = %#v, %v", collecting, err)
	}
	store.SetAgentRolloutMeasurements(rollout.ID, AgentRolloutMeasurements{
		Candidate: AgentRolloutMetrics{SampleSize: 2, Completed: 2, P95LatencyMS: 900, AverageCostMicros: 1200},
		Baseline:  AgentRolloutMetrics{SampleSize: 2, Completed: 2, P95LatencyMS: 1000, AverageCostMicros: 1300},
	})
	ready, err := service.RefreshAgentRollout(ctx, rollout.ID, "support-a")
	if err != nil || ready.Status != AgentRolloutStatusReady || ready.Decision != AgentRolloutDecisionPass {
		t.Fatalf("RefreshAgentRollout(pass) = %#v, %v", ready, err)
	}
	published, deployment, err := service.Publish(ctx, candidate.ID, "approver-b")
	if err != nil || published.Status != StatusPublished || deployment.Version.ID != candidate.ID {
		t.Fatalf("Publish(after rollout) = %#v, %#v, %v", published, deployment, err)
	}
	completed, err := service.AgentRollout(ctx, rollout.ID)
	if err != nil || completed.Status != AgentRolloutStatusPromoted {
		t.Fatalf("AgentRollout(promoted) = %#v, %v", completed, err)
	}
}

func TestAgentRolloutAutomaticallyPausesOnGateViolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	service := NewService(store, "test")
	baseline := createPublishedAgentVersion(t, ctx, service, 0, "unsafe-rollout", "author-a", "approver-b")
	candidate := createSubmittedAgentVersion(t, ctx, service, 1, "unsafe-rollout", "author-a")
	policy := DefaultAgentRolloutPolicy()
	policy.MinimumSampleSize = 1
	policy.ObservationWindowSeconds = 60
	rollout, err := service.StartAgentRollout(ctx, candidate.ID, StartAgentRolloutInput{
		TrafficPercent: 10, Policy: policy, Actor: "approver-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.SetAgentRolloutMeasurements(rollout.ID, AgentRolloutMeasurements{
		Candidate: AgentRolloutMetrics{SampleSize: 1, Failed: 1, QualityFailures: 1, ErrorRate: 1, QualityFailureRate: 1, P95LatencyMS: 300000, AverageCostMicros: 9_000_000},
		Baseline:  AgentRolloutMetrics{SampleSize: 1, Completed: 1, P95LatencyMS: 1000, AverageCostMicros: 1000},
	})
	paused, err := service.RefreshAgentRollout(ctx, rollout.ID, "support-a")
	if err != nil || paused.Status != AgentRolloutStatusPaused || paused.Decision != AgentRolloutDecisionFail || len(paused.Violations) < 4 {
		t.Fatalf("RefreshAgentRollout(fail) = %#v, %v", paused, err)
	}
	if _, _, err = service.Publish(ctx, candidate.ID, "approver-b"); !errors.Is(err, ErrRollout) {
		t.Fatalf("Publish(paused rollout) error = %v", err)
	}
	snapshot, err := service.RoutedAgentRuntime(ctx, "work", "any-user")
	if err != nil || snapshot.VersionID != baseline.ID {
		t.Fatalf("RoutedAgentRuntime(after pause) = %#v, %v", snapshot, err)
	}
}

func createPublishedAgentVersion(t *testing.T, ctx context.Context, service *Service, baseVersion int, key, author, approver string) Version {
	t.Helper()
	item := createSubmittedAgentVersion(t, ctx, service, baseVersion, key, author)
	published, _, err := service.Publish(ctx, item.ID, approver)
	if err != nil {
		t.Fatalf("Publish(baseline) = %v", err)
	}
	return published
}

func createSubmittedAgentVersion(t *testing.T, ctx context.Context, service *Service, baseVersion int, key, author string) Version {
	t.Helper()
	item, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: key, BaseVersion: baseVersion,
		Payload: validAgentDefinitionPayload(t), Actor: author, Reason: "exercise controlled rollout",
	})
	if err != nil {
		t.Fatal(err)
	}
	return validateAndSubmit(t, ctx, service, item, author)
}
