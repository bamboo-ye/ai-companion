package skill

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	registry := NewRegistry()
	if err := RegisterBuiltins(registry); err != nil {
		t.Fatal(err)
	}
	return NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
}

func TestNoSideEffectSkillRunsWithoutChangingRuntime(t *testing.T) {
	service := newTestService(t)
	if manifests, err := service.Skills(context.Background(), "u1"); err != nil || len(manifests) != 3 || manifests[0].Name != "office.email_draft" || len(manifests[0].AllowedTools) != 1 || len(manifests[0].StateGraph) != 6 || manifests[0].MaxInputBytes != 64<<10 {
		t.Fatalf("manifests = %#v", manifests)
	}
	run, created, err := service.Start(context.Background(), "u1", "office.translate", "translate-1", map[string]any{"text": "项目已经完成", "target_language": "English", "tone": "formal"})
	if err != nil || !created || run.Status != "succeeded" || len(run.Steps) != 5 {
		t.Fatalf("run = %#v created=%v err=%v", run, created, err)
	}
	if !strings.Contains(string(run.Output), `"translated_text":"The project has been completed."`) {
		t.Fatalf("output = %s", run.Output)
	}
	replayed, created, err := service.Start(context.Background(), "u1", "office.translate", "translate-1", map[string]any{"text": "ignored", "target_language": "English"})
	if err != nil || created || replayed.ID != run.ID || len(replayed.Steps) != len(run.Steps) {
		t.Fatalf("replay = %#v created=%v err=%v", replayed, created, err)
	}
}

func TestUserCanDisableSkillWithoutAffectingOtherUsers(t *testing.T) {
	service := newTestService(t)
	manifest, err := service.SetEnabled(context.Background(), "u1", "office.translate", false)
	if err != nil || manifest.Enabled {
		t.Fatalf("disabled manifest = %#v err=%v", manifest, err)
	}
	if _, _, err = service.Start(context.Background(), "u1", "office.translate", "disabled-user", map[string]any{"text": "你好", "target_language": "English"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled user start error = %v", err)
	}
	if run, _, startErr := service.Start(context.Background(), "u2", "office.translate", "enabled-user", map[string]any{"text": "你好", "target_language": "English"}); startErr != nil || run.Status != "succeeded" {
		t.Fatalf("other user run = %#v err=%v", run, startErr)
	}
	items, err := service.Skills(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Name == "office.translate" && item.Enabled {
			t.Fatal("disabled skill remained enabled in user catalog")
		}
	}
}

func TestRecoverInterruptedSkillRunMakesRetrySafe(t *testing.T) {
	store := NewMemoryStore()
	registry := NewRegistry()
	if err := RegisterBuiltins(registry); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, NewMemoryFileStore(), registry)
	run, _, err := service.Start(context.Background(), "u1", "office.markdown_document", "recover-doc", map[string]any{"title": "恢复", "content": "内容"})
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	interrupted := store.runs[run.ID]
	interrupted.Status, interrupted.CurrentState = "running", "execute"
	store.runs[run.ID] = interrupted
	store.mu.Unlock()
	count, err := service.Recover(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	recovered, err := service.Get(context.Background(), "u1", run.ID)
	if err != nil || recovered.Status != "failed" || recovered.ErrorCode != "execution_interrupted" || recovered.CompletedAt == nil {
		t.Fatalf("recovered = %#v err=%v", recovered, err)
	}
	retried, err := service.Retry(context.Background(), "u1", run.ID, "recover-retry")
	if err != nil || retried.Status != "waiting_confirmation" || retried.Attempt != 2 {
		t.Fatalf("retried = %#v err=%v", retried, err)
	}
}

func TestMemoryStorePreservesEmptyCollectionsForAPIContracts(t *testing.T) {
	run := cloneRun(Run{Steps: []Step{}, Files: []GeneratedFile{}})
	if run.Steps == nil || run.Files == nil {
		t.Fatalf("empty collections must remain JSON arrays: steps=%#v files=%#v", run.Steps, run.Files)
	}
}

func TestDeterministicTranslationMatchesPhraseWithSentencePunctuation(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		target string
		want   string
	}{
		{name: "Chinese full stop", text: "项目已经完成。", target: "English", want: "The project has been completed."},
		{name: "ASCII full stop", text: "项目已经完成.", target: "en", want: "The project has been completed."},
		{name: "English full stop", text: "The project has been completed.", target: "中文", want: "项目已经完成。"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deterministicTranslation(test.text, test.target); got != test.want {
				t.Fatalf("deterministicTranslation() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSkillContractRejectsUnknownAndOverBudgetInput(t *testing.T) {
	service := newTestService(t)
	if _, _, err := service.Start(context.Background(), "u1", "office.translate", "unknown-field", map[string]any{"text": "你好", "target_language": "English", "surprise": true}); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, _, err := service.Start(context.Background(), "u1", "office.translate", "too-large", map[string]any{"text": strings.Repeat("大", 70<<10), "target_language": "English"}); !errors.Is(err, ErrValidation) {
		t.Fatalf("over budget error = %v", err)
	}
}

func TestRegistryCanDisableSkillWithoutChangingHandlers(t *testing.T) {
	registry := NewRegistry()
	if err := RegisterBuiltins(registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetEnabled("office.translate", false); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	if _, _, err := service.Start(context.Background(), "u1", "office.translate", "disabled", map[string]any{"text": "你好", "target_language": "English"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled skill error = %v", err)
	}
}

func TestEmailSkillOnlyCreatesDraft(t *testing.T) {
	service := newTestService(t)
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-1", map[string]any{
		"to": []any{"client@example.com"}, "subject": "项目进展", "purpose": "项目已经完成，请查收。", "tone": "friendly",
	})
	if err != nil || run.Status != "succeeded" || !strings.Contains(string(run.Output), `"send_status":"draft_only"`) || strings.Contains(string(run.Output), "provider_message_id\":\"sent") {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
}

func TestConfirmedFileSkillIsIdempotentAndNeverOverwritesSource(t *testing.T) {
	service := newTestService(t)
	run, _, err := service.Start(context.Background(), "u1", "office.markdown_document", "doc-1", map[string]any{"title": "周报", "content": "本周完成 M4 骨架。"})
	if err != nil || run.Status != "waiting_confirmation" || len(run.Files) != 0 {
		t.Fatalf("candidate = %#v err=%v", run, err)
	}
	confirmed, err := service.Confirm(context.Background(), "u1", run.ID, "confirm-doc-1")
	if err != nil || confirmed.Status != "succeeded" || len(confirmed.Files) != 1 || !strings.Contains(string(confirmed.Output), `"source_overwritten":false`) {
		t.Fatalf("confirmed = %#v err=%v", confirmed, err)
	}
	replayed, err := service.Confirm(context.Background(), "u1", run.ID, "confirm-doc-1")
	if err != nil || replayed.ID != confirmed.ID || len(replayed.Files) != 1 {
		t.Fatalf("replayed = %#v err=%v", replayed, err)
	}
	if _, err = service.Confirm(context.Background(), "u1", run.ID, "different-key"); !errors.Is(err, ErrConflict) {
		t.Fatalf("different confirmation key error = %v", err)
	}
	metadata, data, err := service.Download(context.Background(), "u1", run.ID, confirmed.Files[0].ID)
	if err != nil || metadata.Name != "周报.md" || string(data) != "# 周报\n\n本周完成 M4 骨架。\n" {
		t.Fatalf("download = %#v %q err=%v", metadata, data, err)
	}
}

func TestCancelRetryAndFailedStepRemainQueryable(t *testing.T) {
	registry := NewRegistry()
	attempts := 0
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.flaky", Version: "1.0.0", DisplayName: "Flaky", Category: "test", RiskLevel: "none", Enabled: true,
		ToolName: "test.flaky", TimeoutMS: 1000, MaxSteps: 8,
		InputSchema: objectSchema([]string{"value"}, map[string]any{"value": map[string]any{"type": "string"}}),
	}, Handler: HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		attempts++
		if attempts == 1 {
			return ToolResult{}, errors.New("temporary failure")
		}
		return ToolResult{Output: map[string]any{"value": input["value"]}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	failed, _, err := service.Start(context.Background(), "u1", "test.flaky", "flaky-1", map[string]any{"value": "ok"})
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "tool_failed" || len(failed.Steps) != 4 {
		t.Fatalf("failed = %#v err=%v", failed, err)
	}
	retried, err := service.Retry(context.Background(), "u1", failed.ID, "retry-1")
	if err != nil || retried.Status != "succeeded" || retried.Attempt != 2 {
		t.Fatalf("retried = %#v err=%v", retried, err)
	}
	replayed, err := service.Retry(context.Background(), "u1", failed.ID, "retry-1")
	if err != nil || replayed.Revision != retried.Revision || attempts != 2 {
		t.Fatalf("retry replay = %#v attempts=%d err=%v", replayed, attempts, err)
	}
}

func TestWaitingRunCanBeCancelledAndRetriedSafely(t *testing.T) {
	service := newTestService(t)
	run, _, _ := service.Start(context.Background(), "u1", "office.markdown_document", "doc-cancel", map[string]any{"title": "草稿", "content": "内容"})
	cancelled, err := service.Cancel(context.Background(), "u1", run.ID, "cancel-1")
	if err != nil || cancelled.Status != "cancelled" || len(cancelled.Files) != 0 {
		t.Fatalf("cancelled = %#v err=%v", cancelled, err)
	}
	retried, err := service.Retry(context.Background(), "u1", run.ID, "retry-cancelled")
	if err != nil || retried.Status != "waiting_confirmation" || retried.Attempt != 2 {
		t.Fatalf("retried = %#v err=%v", retried, err)
	}
	if _, err = service.Cancel(context.Background(), "u1", run.ID, "cancel-1"); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical action key reuse error = %v", err)
	}
}
