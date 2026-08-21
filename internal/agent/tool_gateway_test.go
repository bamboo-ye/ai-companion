package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/chattool"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/skill"
)

type staticRunReader struct {
	run Run
	err error
}

func (r staticRunReader) Get(context.Context, string) (Run, error) {
	return r.run, r.err
}

func newLedgerGateway(t *testing.T) (*ToolGateway, *ledger.Service) {
	t.Helper()
	input, err := json.Marshal(map[string]any{
		"message_id": "message-1",
		"text":       "我今天吃饭花了50元",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaseExpiresAt := time.Now().UTC().Add(time.Hour)
	run := Run{
		ID: "run-1", ThreadID: "run-1", UserID: "user-1",
		ConversationID: "conversation-1", CharacterID: "character-1",
		Module: "life", Status: "running", Input: input, Revision: 1,
		LeaseOwner: "worker-1", LeaseExpiresAt: &leaseExpiresAt,
		DeadlineAt: leaseExpiresAt,
	}
	ledgerService := ledger.NewService(ledger.NewMemoryStore())
	plannerService := planner.NewService(planner.NewMemoryStore())
	executor := chattool.New(ledgerService, plannerService, nil, nil)
	return NewToolGateway(
		staticRunReader{run: run}, executor, ledgerService, plannerService, nil,
		"agent-confirmation-secret-for-tests", time.Minute,
	), ledgerService
}

func TestToolGatewayPrepareAndCommitLedgerWithReplay(t *testing.T) {
	gateway, ledgerService := newLedgerGateway(t)
	ctx := context.Background()
	prepared, err := gateway.Prepare(ctx, "run-1", ToolPrepareInput{
		CallKey: "run-1:prepare", ToolName: "life_prepare_ledger_entry",
		Arguments: map[string]any{"untrusted_user_id": "other-user"}, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "requires_confirmation" || prepared.ConfirmationToken == "" ||
		prepared.RiskLevel != "medium" || prepared.Summary == "" {
		t.Fatalf("Prepare() = %#v", prepared)
	}
	entries, err := ledgerService.List(ctx, "user-1", ledger.EntryFilter{Limit: 20})
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries before commit = %#v, %v", entries, err)
	}

	first, err := gateway.Commit(ctx, "run-1", ToolCommitInput{
		CallKey: "run-1:commit", ConfirmationToken: prepared.ConfirmationToken, Approved: true, ExpectedRevision: 1,
	})
	if err != nil || first.Response == "" {
		t.Fatalf("Commit() = %#v, %v", first, err)
	}
	second, err := gateway.Commit(ctx, "run-1", ToolCommitInput{
		CallKey: "run-1:commit", ConfirmationToken: prepared.ConfirmationToken, Approved: true, ExpectedRevision: 1,
	})
	if err != nil || second.Response == "" {
		t.Fatalf("replayed Commit() = %#v, %v", second, err)
	}
	entries, err = ledgerService.List(ctx, "user-1", ledger.EntryFilter{Limit: 20})
	if err != nil || len(entries) != 1 || entries[0].AmountMinor != 5000 {
		t.Fatalf("entries after replay = %#v, %v", entries, err)
	}
	otherEntries, err := ledgerService.List(ctx, "other-user", ledger.EntryFilter{Limit: 20})
	if err != nil || len(otherEntries) != 0 {
		t.Fatalf("untrusted user received entries = %#v, %v", otherEntries, err)
	}
}

func TestToolGatewayCarriesTrustedReminderClarificationHistory(t *testing.T) {
	gateway, _ := newLedgerGateway(t)
	reader := gateway.runs.(staticRunReader)
	input, err := json.Marshal(map[string]any{
		"message_id": "evening-follow-up",
		"text":       "今晚",
		"context": map[string]any{"history": []map[string]string{
			{"role": "system", "content": "不应传入工具"},
			{"role": "user", "content": "提醒我晚上查看邮件"},
			{"role": "assistant", "content": "还需要补充“提醒日期”，补充后我就能创建提醒。"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reader.run.Input = input
	gateway.runs = reader

	prepared, err := gateway.Prepare(context.Background(), "run-1", ToolPrepareInput{
		CallKey: "evening:prepare", ToolName: "life_prepare_reminder",
		Arguments: map[string]any{}, ExpectedRevision: 1,
	})
	if err != nil || prepared.Status != "requires_confirmation" || !strings.Contains(prepared.Summary, "事项：查看邮件") {
		t.Fatalf("Prepare() = %#v, %v", prepared, err)
	}
}

func TestToolGatewayCommitsConfirmedLifeTaskCompletion(t *testing.T) {
	gateway, _ := newLedgerGateway(t)
	ctx := context.Background()
	location, _ := time.LoadLocation("Asia/Shanghai")
	localDate := time.Now().In(location).Format("2006-01-02")
	item, err := gateway.planner.AddTodayItem(ctx, "user-1", localDate, "Asia/Shanghai", "整理合同", "web")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := gateway.Prepare(ctx, "run-1", ToolPrepareInput{
		CallKey: "complete:prepare", ToolName: "life_prepare_task_completion",
		Arguments: map[string]any{"title": "整理合同", "task_type": "today_plan"}, ExpectedRevision: 1,
	})
	if err != nil || prepared.Status != "requires_confirmation" || prepared.ConfirmationToken == "" {
		t.Fatalf("Prepare() = %#v, %v", prepared, err)
	}
	outcome, err := gateway.Commit(ctx, "run-1", ToolCommitInput{
		CallKey: "complete:commit", ConfirmationToken: prepared.ConfirmationToken,
		Approved: true, ExpectedRevision: 1,
	})
	if err != nil || !strings.Contains(outcome.Response, "已完成") {
		t.Fatalf("Commit() = %#v, %v", outcome, err)
	}
	today, err := gateway.planner.Today(ctx, "user-1", localDate, "Asia/Shanghai")
	if err != nil || len(today.Items) != 1 || today.Items[0].ID != item.ID || today.Items[0].Status != "completed" {
		t.Fatalf("completed item = %#v, %v", today.Items, err)
	}
}

func TestToolGatewayRejectsCancelledOrTimedOutRunBeforeToolWrite(t *testing.T) {
	for _, status := range []string{"cancel_requested", "cancelled", "timed_out"} {
		t.Run(status, func(t *testing.T) {
			gateway, _ := newLedgerGateway(t)
			reader := gateway.runs.(staticRunReader)
			reader.run.Status = status
			gateway.runs = reader
			if _, err := gateway.Prepare(
				context.Background(),
				"run-1",
				ToolPrepareInput{
					CallKey:          "blocked",
					ToolName:         "life_prepare_ledger_entry",
					Arguments:        map[string]any{"amount": "50"},
					ExpectedRevision: 1,
				},
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("Prepare() error = %v", err)
			}
		})
	}
}

func TestToolGatewayRejectsStaleRevisionExpiredLeaseAndDeadline(t *testing.T) {
	t.Run("stale_revision", func(t *testing.T) {
		gateway, _ := newLedgerGateway(t)
		if _, err := gateway.Prepare(context.Background(), "run-1", ToolPrepareInput{
			CallKey: "stale", ToolName: "life_prepare_ledger_entry", ExpectedRevision: 2,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("Prepare() error = %v", err)
		}
	})
	for _, field := range []string{"lease", "deadline"} {
		t.Run(field, func(t *testing.T) {
			gateway, _ := newLedgerGateway(t)
			reader := gateway.runs.(staticRunReader)
			expired := gateway.now().UTC().Add(-time.Second)
			if field == "lease" {
				reader.run.LeaseExpiresAt = &expired
			} else {
				reader.run.DeadlineAt = expired
			}
			gateway.runs = reader
			if _, err := gateway.Prepare(context.Background(), "run-1", ToolPrepareInput{
				CallKey: "expired", ToolName: "life_prepare_ledger_entry", ExpectedRevision: 1,
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("Prepare() error = %v", err)
			}
		})
	}
}

func TestToolGatewayRejectsTamperedConfirmationToken(t *testing.T) {
	gateway, _ := newLedgerGateway(t)
	prepared, err := gateway.Prepare(context.Background(), "run-1", ToolPrepareInput{
		CallKey: "run-1:prepare", ToolName: "life_prepare_ledger_entry", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := prepared.ConfirmationToken
	replacement := byte('A')
	if token[len(token)-1] == replacement {
		replacement = 'B'
	}
	token = token[:len(token)-1] + string(replacement)
	if _, err = gateway.Commit(context.Background(), "run-1", ToolCommitInput{
		CallKey: "run-1:commit", ConfirmationToken: token, Approved: true, ExpectedRevision: 1,
	}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Commit() error = %v, want ErrInvalidToken", err)
	}
}

func TestToolGatewayRejectsCrossModuleToolBeforeExecution(t *testing.T) {
	gateway, _ := newLedgerGateway(t)
	if _, err := gateway.Prepare(context.Background(), "run-1", ToolPrepareInput{
		CallKey: "run-1:prepare", ToolName: "work_list_documents", ExpectedRevision: 1,
	}); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("Prepare() error = %v, want ErrToolDenied", err)
	}
}

func TestToolGatewayRejectsExpiredTokenAndMalformedRunInput(t *testing.T) {
	gateway, _ := newLedgerGateway(t)
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	gateway.now = func() time.Time { return now }
	prepared, err := gateway.Prepare(context.Background(), "run-1", ToolPrepareInput{
		CallKey: "run-1:prepare", ToolName: "life_prepare_ledger_entry", ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err = gateway.Commit(context.Background(), "run-1", ToolCommitInput{
		CallKey: "run-1:commit", ConfirmationToken: prepared.ConfirmationToken, Approved: true, ExpectedRevision: 1,
	}); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired Commit() error = %v, want ErrInvalidToken", err)
	}

	leaseExpiresAt := now.Add(time.Hour)
	gateway.runs = staticRunReader{run: Run{
		ID: "run-2", UserID: "user-1", ConversationID: "conversation-1",
		CharacterID: "character-1", Module: "life", Status: "running",
		Input: json.RawMessage(`{"message_id":"message-2"}`), Revision: 1,
		LeaseOwner: "worker-1", LeaseExpiresAt: &leaseExpiresAt, DeadlineAt: leaseExpiresAt,
	}}
	if _, err = gateway.Prepare(context.Background(), "run-2", ToolPrepareInput{
		CallKey: "run-2:prepare", ToolName: "life_query_today_plan", ExpectedRevision: 1,
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("malformed Prepare() error = %v, want ErrValidation", err)
	}
}

func TestPublicSkillRunOmitsPrivateInputAndDeliversGeneratedFiles(t *testing.T) {
	item := skill.Run{
		ID:        "skill-run-1",
		SkillName: "office.pptx_generate",
		Status:    "succeeded",
		Attempt:   3,
		Input:     json.RawMessage(`{"source_base64":"private-payload"}`),
		Output:    json.RawMessage(`{"title":"课程介绍"}`),
		Files: []skill.GeneratedFile{{
			ID: "file-1", RunID: "skill-run-1", Name: "课程介绍.pptx",
			MediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		}},
	}
	public := publicSkillRun(item)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-payload") ||
		!strings.Contains(string(encoded), "课程介绍") ||
		public["attempt"] != 3 {
		t.Fatalf("public skill run = %s", encoded)
	}
	outcome := skillRunOutcome(item, false)
	if !strings.Contains(outcome.Response, "ai-generated-file") ||
		!strings.Contains(outcome.Response, "课程介绍.pptx") {
		t.Fatalf("skill outcome = %#v", outcome)
	}
}

func TestPublicSkillRunExposesTrustedFailureContract(t *testing.T) {
	item := skill.Run{
		ID: "skill-run-failed", SkillName: "office.pptx_generate", Status: "failed", Attempt: 1,
		ErrorCode: "invalid_output_filename", ErrorMessage: "输出文件名无效。",
		Output: json.RawMessage(`{"failure":{"contract_version":"tool-failure-v1","code":"invalid_output_filename","category":"argument_validation","phase":"pre_execution","repairable":true,"side_effect_state":"none","field_paths":["/filename"],"allowed_repairs":["filename.safe_basename"]}}`),
	}
	public := publicSkillRun(item)
	failure, ok := public["failure"].(map[string]any)
	if !ok || failure["code"] != "invalid_output_filename" || failure["repairable"] != true {
		t.Fatalf("public failure = %#v", public)
	}
}
