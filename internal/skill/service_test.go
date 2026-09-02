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
	if manifests, err := service.Skills(context.Background(), "u1"); err != nil || len(manifests) != 3 || manifests[0].Name != "office.email_draft" || manifests[0].Version != "2.1.0" || len(manifests[0].AllowedTools) != 1 || len(manifests[0].StateGraph) != 6 || manifests[0].MaxInputBytes != 64<<10 || manifests[0].MaxOutputFileBytes != 10<<20 {
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

func TestGeneratedFileUsesPerSkillOutputBudget(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.output_budget", Version: "1.0.0", DisplayName: "Output budget", Category: "test",
		RiskLevel: "none", Enabled: true, ToolName: "file.output_budget", TimeoutMS: 1000, MaxSteps: 8,
		MaxOutputFileBytes: 4,
		InputSchema:        objectSchema(nil, map[string]any{"oversized": map[string]any{"type": "boolean"}}),
		OutputSchema:       objectSchema([]string{"ok"}, map[string]any{"ok": map[string]any{"type": "boolean"}}),
	}, Handler: HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		data := []byte("1234")
		if input["oversized"] == true {
			data = []byte("12345")
		}
		return ToolResult{Output: map[string]any{"ok": true}, Files: []FileOutput{{Name: "result.bin", MediaType: "application/octet-stream", Data: data}}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	succeeded, _, err := service.Start(context.Background(), "u1", "test.output_budget", "output-budget-ok", map[string]any{})
	if err != nil || succeeded.Status != "succeeded" || len(succeeded.Files) != 1 || succeeded.Files[0].SizeBytes != 4 {
		t.Fatalf("succeeded = %#v err=%v", succeeded, err)
	}
	failed, _, err := service.Start(context.Background(), "u1", "test.output_budget", "output-budget-large", map[string]any{"oversized": true})
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "output_file_too_large" {
		t.Fatalf("failed = %#v err=%v", failed, err)
	}
	if !strings.Contains(string(failed.Output), `"actual_bytes":5`) || !strings.Contains(string(failed.Output), `"limit_bytes":4`) {
		t.Fatalf("failure details = %s", failed.Output)
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

func TestModelBackedSkillEnforcesReportedCostBudget(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.model_budget", Version: "1.0.0", DisplayName: "Model budget", Category: "test", RiskLevel: "none", Enabled: true,
		ToolName: "test.model_budget", TimeoutMS: 1000, MaxSteps: 8, MaxCostMicros: 10,
		InputSchema:  objectSchema(nil, map[string]any{}),
		OutputSchema: objectSchema([]string{"model_usage"}, map[string]any{"model_usage": map[string]any{"type": "object"}}),
	}, Handler: HandlerFunc(func(_ context.Context, _ map[string]any) (ToolResult, error) {
		return ToolResult{Output: map[string]any{
			"model_usage": map[string]any{"cost_micros": 11},
		}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	run, _, err := service.Start(context.Background(), "u1", "test.model_budget", "cost-1", map[string]any{})
	if err != nil || run.Status != "failed" || run.ErrorCode != "model_cost_exceeded" {
		t.Fatalf("run = %#v err=%v", run, err)
	}
	if !strings.Contains(string(run.Output), `"cost_micros":11`) {
		t.Fatalf("failed run lost model usage: %s", run.Output)
	}
	if len(run.Steps) == 0 || !strings.Contains(string(run.Steps[len(run.Steps)-1].Output), `"cost_micros":11`) {
		t.Fatalf("failed step lost model usage: %#v", run.Steps)
	}
}

func TestModelBackedSkillTurnsStructuredOperationErrorIntoAuditedFailure(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.model_operation", Version: "1.0.0", DisplayName: "Model operation", Category: "test", RiskLevel: "none", Enabled: true,
		ToolName: "test.model_operation", TimeoutMS: 1000, MaxSteps: 8, MaxCostMicros: 100,
		InputSchema:  objectSchema(nil, map[string]any{}),
		OutputSchema: objectSchema([]string{"model_usage"}, map[string]any{"model_usage": map[string]any{"type": "object"}}),
	}, Handler: HandlerFunc(func(_ context.Context, _ map[string]any) (ToolResult, error) {
		return ToolResult{Output: map[string]any{
			"model_usage":     map[string]any{"cost_micros": 9, "requested_model": "openai/gpt-5-mini"},
			"operation_error": map[string]any{"code": "translation_model_truncated"},
		}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	run, _, err := service.Start(context.Background(), "u1", "test.model_operation", "operation-1", map[string]any{})
	if err != nil || run.Status != "failed" || run.ErrorCode != "model_operation_failed" || run.ErrorMessage != "translation_model_truncated" {
		t.Fatalf("run = %#v err=%v", run, err)
	}
	for _, expected := range []string{`"cost_micros":9`, `"operation_error"`, `"translation_model_truncated"`} {
		if !strings.Contains(string(run.Output), expected) {
			t.Fatalf("failed run output missing %s: %s", expected, run.Output)
		}
	}
	if len(run.Steps) == 0 || string(run.Steps[len(run.Steps)-1].Output) != string(run.Output) {
		t.Fatalf("failed step output = %s, run output = %s", run.Steps[len(run.Steps)-1].Output, run.Output)
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

func completeEnglishEmailInput() map[string]any {
	return map[string]any{
		"to":                  []any{},
		"subject":             "Question About Semester A Project Courses",
		"purpose":             "Ask whether a Project course may be taken in Semester A and whether prerequisites apply.",
		"output_language":     "en-US",
		"relationship":        "first_contact",
		"introduction_policy": "required",
		"sender_name":         "Alex Chen",
		"sender_role":         "Prospective student",
		"salutation":          "Dear Sir or Madam,",
		"introduction":        "My name is Alex Chen, and I am a prospective student.",
		"body_paragraphs": []any{
			"I am planning my Semester A schedule and am considering the Project course.",
		},
		"request_or_next_step": "Could you please let me know whether this is possible and whether any prerequisites apply?",
		"courtesy":             "Thank you for your time and assistance.",
		"closing":              "Kind regards,",
		"signature_lines":      []any{"Alex Chen", "Prospective student"},
		"tone":                 "formal",
	}
}

func TestEmailSkillOnlyCreatesDraft(t *testing.T) {
	service := newTestService(t)
	input := completeEnglishEmailInput()
	input["to"] = []any{"client@example.com"}
	input["tone"] = "friendly"
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-1", input)
	if err != nil || run.Status != "succeeded" || !strings.Contains(string(run.Output), `"send_status":"draft_only"`) || strings.Contains(string(run.Output), "provider_message_id\":\"sent") {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
}

func TestEmailSkillCreatesDraftWithoutRecipient(t *testing.T) {
	service := newTestService(t)
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-without-recipient", map[string]any{
		"to":                   []any{},
		"subject":              "关于 A 学期 Project 课程选课的咨询",
		"purpose":              "咨询 A 学期 Project 课程安排。",
		"output_language":      "zh-CN",
		"relationship":         "first_contact",
		"introduction_policy":  "required",
		"sender_name":          "小林",
		"salutation":           "尊敬的老师：",
		"introduction":         "您好，我是小林，目前正在了解 A 学期的课程安排。",
		"body_paragraphs":      []any{"我正在规划 A 学期的课程，并考虑选择 Project 课程。"},
		"request_or_next_step": "烦请您告知是否可以选修，以及是否有先修要求。",
		"courtesy":             "感谢您抽出时间阅读并回复。",
		"closing":              "此致\n敬礼！",
		"signature_lines":      []any{"小林"},
		"tone":                 "formal",
	})
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
	output := string(run.Output)
	for _, expected := range []string{`"to":[]`, `"send_status":"draft_only"`, `"output_language":"zh-CN"`, "A 学期", "您好", "烦请您", "感谢您", "此致", "敬礼", "小林"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output missing %q: %s", expected, output)
		}
	}
}

func TestEmailSkillRequiresCompleteVersionTwoContract(t *testing.T) {
	service := newTestService(t)
	_, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-incomplete-v1", map[string]any{
		"subject": "咨询 A 学期 Project 课选课事宜",
		"purpose": "询问A学期是否可以选project课",
	})
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "output_language") {
		t.Fatalf("incomplete v1 input error = %v", err)
	}
}

func TestEmailSkillCreatesCompleteEnglishDraftWithQualityEvidence(t *testing.T) {
	service := newTestService(t)
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-english-v2", completeEnglishEmailInput())
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
	output := string(run.Output)
	for _, expected := range []string{
		`"output_language":"en-US"`, `"email_policy_version":"` + EmailDraftPolicyVersion + `"`,
		`"passed":true`, "Dear Sir or Madam,", "My name is Alex Chen", "Thank you for your time", "Kind regards", "Alex Chen",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output missing %q: %s", expected, output)
		}
	}
	if hanCharacter.MatchString(output) {
		t.Fatalf("English draft contains Chinese text: %s", output)
	}
}

func TestEmailSkillRejectsRepeatedRequestOrCourtesyInBody(t *testing.T) {
	service := newTestService(t)
	input := completeEnglishEmailInput()
	input["body_paragraphs"] = []any{
		"Could you please let me know whether this is possible and whether any prerequisites apply?",
		"Thank you for your time and assistance.",
	}
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-repeated-sections", input)
	if err != nil || run.Status != "failed" {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
	for _, code := range []string{"request_content_in_body", "courtesy_content_in_body", "duplicate_email_content"} {
		if !strings.Contains(run.ErrorMessage, code) {
			t.Fatalf("failure missing %q: %s", code, run.ErrorMessage)
		}
	}
}

func TestEmailQualityRejectsSemanticallyRepeatedParagraphs(t *testing.T) {
	input := completeEnglishEmailInput()
	input["body_paragraphs"] = []any{
		"The Project course is part of my planned Semester A schedule.",
		"My planned Semester A schedule includes the Project course.",
	}
	violations := validateStructuredEmailDraft(input)
	if !strings.Contains(strings.Join(violations, ","), "duplicate_email_content") {
		t.Fatalf("violations = %#v", violations)
	}
}

func TestEmailRendererDropsExactDuplicateAsFinalDefense(t *testing.T) {
	input := completeEnglishEmailInput()
	request := emailText(input["request_or_next_step"])
	input["body_paragraphs"] = []any{request}
	body := renderStructuredEmailDraft(input)
	if count := strings.Count(body, request); count != 1 {
		t.Fatalf("request rendered %d times: %s", count, body)
	}
}

func TestEmailSkillRejectsMixedLanguageOrIncompleteEnglishDraft(t *testing.T) {
	service := newTestService(t)
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-english-invalid", map[string]any{
		"subject":              "Course Question",
		"purpose":              "Ask about a course.",
		"output_language":      "en-US",
		"relationship":         "first_contact",
		"introduction_policy":  "required",
		"sender_name":          "Alex Chen",
		"salutation":           "您好！",
		"introduction":         "",
		"body_paragraphs":      []any{"请问可以选 Project 课吗？"},
		"request_or_next_step": "Please reply.",
		"courtesy":             "Thanks.",
		"closing":              "Regards,",
		"signature_lines":      []any{"Alex Chen"},
		"tone":                 "formal",
	})
	if err != nil || run.Status != "failed" {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
	for _, code := range []string{"missing_introduction", "english_language_contamination", "english_salutation_not_polite"} {
		if !strings.Contains(run.ErrorMessage, code) {
			t.Fatalf("failure missing %q: %s", code, run.ErrorMessage)
		}
	}
}

func TestEmailSkillRejectsInvalidProvidedRecipient(t *testing.T) {
	service := newTestService(t)
	input := completeEnglishEmailInput()
	input["to"] = []any{"not-an-email"}
	run, _, err := service.Start(context.Background(), "u1", "office.email_draft", "draft-invalid-recipient", input)
	if err != nil || run.Status != "failed" || !strings.Contains(run.ErrorMessage, "invalid recipient") {
		t.Fatalf("draft = %#v err=%v", run, err)
	}
}

func TestRepairRetryKeepsLogicalRunAndInheritsSafeFilenameApproval(t *testing.T) {
	registry := NewRegistry()
	calls := 0
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.repairable_file", Version: "1.1.0", DisplayName: "Repairable file", Category: "test",
		RiskLevel: "medium", RequiresConfirmation: true, Enabled: true,
		ToolName: "file.test", TimeoutMS: 1000, MaxSteps: 12,
		InputSchema: objectSchema([]string{"title"}, map[string]any{
			"title": map[string]any{"type": "string"}, "filename": map[string]any{"type": "string"},
		}),
		OutputSchema:   objectSchema([]string{"ok"}, map[string]any{"ok": map[string]any{"type": "boolean"}}),
		RepairPolicies: filenameRepairPolicies(".pptx"),
	}, Handler: HandlerFunc(func(_ context.Context, input map[string]any) (ToolResult, error) {
		calls++
		if calls == 1 {
			return ToolResult{}, NewToolExecutionError(ToolFailure{
				Code: "invalid_output_filename", Category: "argument_validation", Phase: "pre_execution",
				Message: "输出文件名无效。", Repairable: true, SideEffectState: "none",
				FieldPaths: []string{"/filename"}, AllowedRepairs: []string{"remove_optional_filename", "filename.safe_basename"},
			}, errors.New("invalid_output_filename"))
		}
		if _, exists := input["filename"]; exists {
			t.Fatalf("repaired input still has filename: %#v", input)
		}
		return ToolResult{Output: map[string]any{"ok": true}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	candidate, _, err := service.StartForConversation(context.Background(), "u1", "test.repairable_file", "repair-create", map[string]any{
		"title": "Important Dates - Semester A 2026/27", "filename": "Important Dates - Semester A 2026/27.pptx",
	}, "conversation-1", "message-1")
	if err != nil || candidate.Status != "waiting_confirmation" {
		t.Fatalf("candidate = %#v err=%v", candidate, err)
	}
	failed, err := service.Confirm(context.Background(), "u1", candidate.ID, "repair-confirm")
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "invalid_output_filename" {
		t.Fatalf("failed = %#v err=%v", failed, err)
	}
	repaired, err := service.RepairRetry(context.Background(), "u1", failed.ID, "repair-attempt-2", "remove_optional_filename", map[string]any{
		"title": "Important Dates - Semester A 2026/27",
	})
	if err != nil || repaired.Status != "succeeded" || repaired.ID != failed.ID || repaired.Attempt != 2 {
		t.Fatalf("repaired = %#v err=%v", repaired, err)
	}
	if calls != 2 || repaired.ErrorCode != "" || !strings.Contains(string(repaired.Output), `"ok":true`) {
		t.Fatalf("calls=%d repaired=%#v", calls, repaired)
	}
	if !strings.Contains(repaired.DeliveryContent, "任务重试已完成") || !strings.Contains(repaired.DeliveryContent, "ai-skill-run") {
		t.Fatalf("retry result was not prepared for chat delivery: %q", repaired.DeliveryContent)
	}
	if len(repaired.Steps) < 2 || repaired.Steps[len(repaired.Steps)-3].State != "repair" {
		t.Fatalf("repair lineage missing: %#v", repaired.Steps)
	}
}

func TestRepairRetryAllowsTrustedSameInputTransientFailure(t *testing.T) {
	registry := NewRegistry()
	calls := 0
	err := registry.Register(Definition{Manifest: Manifest{
		Name: "test.transient_retry", Version: "1.0.0", DisplayName: "Transient retry", Category: "test",
		RiskLevel: "none", Enabled: true, ToolName: "test.transient_retry", TimeoutMS: 1000, MaxSteps: 10,
		InputSchema:  objectSchema([]string{"value"}, map[string]any{"value": map[string]any{"type": "string"}}),
		OutputSchema: objectSchema([]string{"ok"}, map[string]any{"ok": map[string]any{"type": "boolean"}}),
	}, Handler: HandlerFunc(func(_ context.Context, _ map[string]any) (ToolResult, error) {
		calls++
		if calls == 1 {
			return ToolResult{}, NewToolExecutionError(ToolFailure{
				Code: "upstream_busy", Category: "transient", Phase: "pre_execution",
				RetrySameInput: true, Repairable: false, SideEffectState: "none",
			}, errors.New("upstream busy"))
		}
		return ToolResult{Output: map[string]any{"ok": true}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	failed, _, err := service.Start(context.Background(), "u1", "test.transient_retry", "transient-create", map[string]any{"value": "same"})
	if err != nil || failed.Status != "failed" {
		t.Fatalf("failed = %#v err=%v", failed, err)
	}
	retried, err := service.RepairRetry(
		context.Background(), "u1", failed.ID, "transient-attempt-2",
		"transport.retry", map[string]any{"value": "same"},
	)
	if err != nil || retried.Status != "succeeded" || retried.Attempt != 2 || calls != 2 {
		t.Fatalf("retried = %#v calls=%d err=%v", retried, calls, err)
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
		return ToolResult{
			Output: map[string]any{"value": input["value"]},
			Files: []FileOutput{{
				Name: "retry-result.txt", MediaType: "text/plain", Data: []byte("retry succeeded"),
			}},
		}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	failed, _, err := service.StartForConversation(
		context.Background(), "u1", "test.flaky", "flaky-1", map[string]any{"value": "ok"},
		"conversation-1", "message-1",
	)
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "tool_failed" || len(failed.Steps) != 4 {
		t.Fatalf("failed = %#v err=%v", failed, err)
	}
	if failed.ConversationID != "conversation-1" || failed.OriginMessageID != "message-1" || failed.DeliveryContent != "" {
		t.Fatalf("failed run origin/delivery = %#v", failed)
	}
	retried, err := service.Retry(context.Background(), "u1", failed.ID, "retry-1")
	if err != nil || retried.Status != "succeeded" || retried.Attempt != 2 {
		t.Fatalf("retried = %#v err=%v", retried, err)
	}
	if len(retried.Files) != 1 {
		t.Fatalf("retry files = %#v", retried.Files)
	}
	fileMarker := "<!--ai-generated-file:" + failed.ID + "|" + retried.Files[0].ID + "|retry-result.txt-->"
	for _, expected := range []string{"任务重试已完成：test.flaky", `"value": "ok"`, fileMarker, "<!--ai-skill-run:" + failed.ID + "|2|succeeded-->"} {
		if !strings.Contains(retried.DeliveryContent, expected) {
			t.Fatalf("retry delivery missing %q: %s", expected, retried.DeliveryContent)
		}
	}
	metadata, data, err := service.Download(context.Background(), "u1", retried.ID, retried.Files[0].ID)
	if err != nil || metadata.Name != "retry-result.txt" || string(data) != "retry succeeded" {
		t.Fatalf("retry download = %#v %q err=%v", metadata, data, err)
	}
	conversationRuns, err := service.ListForConversation(context.Background(), "u1", "conversation-1", 10)
	if err != nil || len(conversationRuns) != 1 || conversationRuns[0].ID != failed.ID {
		t.Fatalf("conversation runs = %#v err=%v", conversationRuns, err)
	}
	replayed, err := service.Retry(context.Background(), "u1", failed.ID, "retry-1")
	if err != nil || replayed.Revision != retried.Revision || attempts != 2 {
		t.Fatalf("retry replay = %#v attempts=%d err=%v", replayed, attempts, err)
	}
}

func TestRetryDeliveryWaitsForTerminalSuccessOrFailure(t *testing.T) {
	run := Run{
		ID: "run-1", SkillName: "office.pptx_generate", Attempt: 1,
		ConversationID: "conversation-1", OriginMessageID: "message-1",
		Status: "failed", ErrorMessage: "first attempt failed",
	}
	if content := retryDeliveryContent(run); content != "" {
		t.Fatalf("initial attempt must not be delivered: %q", content)
	}
	run.Attempt, run.Status = 2, "queued"
	if content := retryDeliveryContent(run); content != "" {
		t.Fatalf("active retry must not be delivered: %q", content)
	}
	run.Status, run.ErrorMessage = "failed", "rendering failed"
	content := retryDeliveryContent(run)
	for _, expected := range []string{
		"任务重试失败：office.pptx_generate",
		"rendering failed",
		"<!--ai-skill-run:run-1|2|failed-->",
	} {
		if !strings.Contains(content, expected) {
			t.Fatalf("terminal retry failure missing %q: %s", expected, content)
		}
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
