package agent

import (
	"context"
	"testing"

	"github.com/windcry1/ai-companion/internal/skill"
)

type skillRunReaderFunc func(context.Context, string, string) (skill.Run, error)

func (f skillRunReaderFunc) GetSkillRun(ctx context.Context, userID, runID string) (skill.Run, error) {
	return f(ctx, userID, runID)
}

func TestSkillTaskReadinessProbeDistinguishesPendingAndTerminalStates(t *testing.T) {
	status := "queued"
	probe := SkillTaskReadinessProbe{Store: skillRunReaderFunc(func(_ context.Context, userID, runID string) (skill.Run, error) {
		if userID != "user-1" || runID != "task-1" {
			t.Fatalf("GetSkillRun identity = %q/%q", userID, runID)
		}
		return skill.Run{Status: status}, nil
	})}
	ready, err := probe.Ready(context.Background(), "user-1", "task-1")
	if err != nil || ready {
		t.Fatalf("queued Ready() = %v, %v", ready, err)
	}
	status = "succeeded"
	ready, err = probe.Ready(context.Background(), "user-1", "task-1")
	if err != nil || !ready {
		t.Fatalf("succeeded Ready() = %v, %v", ready, err)
	}
}
