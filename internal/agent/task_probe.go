package agent

import (
	"context"
	"strings"

	"github.com/windcry1/ai-companion/internal/skill"
)

type SkillRunReader interface {
	GetSkillRun(context.Context, string, string) (skill.Run, error)
}

type SkillTaskReadinessProbe struct {
	Store SkillRunReader
}

func (p SkillTaskReadinessProbe) Ready(ctx context.Context, userID, taskID string) (bool, error) {
	if p.Store == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(taskID) == "" {
		return false, ErrValidation
	}
	run, err := p.Store.GetSkillRun(ctx, strings.TrimSpace(userID), strings.TrimSpace(taskID))
	if err != nil {
		return false, err
	}
	switch run.Status {
	case "waiting_confirmation", "queued", "running":
		return false, nil
	case "succeeded", "failed", "cancelled":
		return true, nil
	default:
		// Let the normal Agent observation path report an unknown persisted state.
		return true, nil
	}
}
