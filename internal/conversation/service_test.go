package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/character"
	memorydomain "github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/reliability"
)

type blockingProvider struct{}

func (blockingProvider) Generate(ctx context.Context, _ character.Character, _ []Message) (string, Usage, error) {
	<-ctx.Done()
	return "", Usage{}, ctx.Err()
}

type countingProvider struct{ calls int }

func (p *countingProvider) Generate(context.Context, character.Character, []Message) (string, Usage, error) {
	p.calls++
	return "不会执行到这里", Usage{}, nil
}

type contextCaptureProvider struct{ history []Message }

func (p *contextCaptureProvider) Generate(_ context.Context, _ character.Character, history []Message) (string, Usage, error) {
	p.history = append([]Message(nil), history...)
	return "我记住了今天的安排。", Usage{}, nil
}

type dailyContextTools struct{}

func (dailyContextTools) Execute(context.Context, ToolRequest) (ToolResult, error) {
	return ToolResult{}, nil
}
func (dailyContextTools) Context(context.Context, ToolRequest) (string, error) {
	return "用户今日计划完整内容：\n- 整理客户反馈\n- 15:00 参加评审会", nil
}

type modelQueryProvider struct {
	generateCalls int
	routingCalls  int
}

func (p *modelQueryProvider) Generate(context.Context, character.Character, []Message) (string, Usage, error) {
	p.generateCalls++
	return "不应使用自由生成回答事实查询", Usage{Provider: "test-model", Model: "chat"}, nil
}

func (p *modelQueryProvider) GenerateWithTools(_ context.Context, _ character.Character, history []Message, tools []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	p.routingCalls++
	if len(history) != 2 || history[1].Content != "我今天的计划是什么" {
		return ModelToolTurn{}, Usage{}, errors.New("unexpected routing history")
	}
	if len(tools) != 1 || tools[0].Name != "life_query_today_plan" {
		return ModelToolTurn{}, Usage{}, errors.New("unexpected model tools")
	}
	return ModelToolTurn{Call: &ModelToolCall{ID: "call-1", Name: "life_query_today_plan", Arguments: map[string]any{}}}, Usage{Provider: "test-model", Model: "intent", InputTokens: 12, OutputTokens: 3}, nil
}

type modelQueryTools struct {
	legacyCalls int
	queryCalls  int
}

type modelNoQueryProvider struct{ routingCalls, generateCalls int }

func (p *modelNoQueryProvider) Generate(context.Context, character.Character, []Message) (string, Usage, error) {
	p.generateCalls++
	return "普通模型回答", Usage{Provider: "test-model", Model: "chat"}, nil
}

func (p *modelNoQueryProvider) GenerateWithTools(context.Context, character.Character, []Message, []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	p.routingCalls++
	return ModelToolTurn{Call: &ModelToolCall{ID: "call-ledger", Name: "life_prepare_ledger_entry", Arguments: map[string]any{}}}, Usage{Provider: "test-model", Model: "intent"}, nil
}

type modelNoQueryTools struct{ legacyCalls, queryCalls int }

func (t *modelNoQueryTools) Execute(context.Context, ToolRequest) (ToolResult, error) {
	t.legacyCalls++
	return ToolResult{Handled: true, ToolName: "life.ledger.record", Response: "确认后才会写入生活账本。"}, nil
}

func (t *modelNoQueryTools) ModelTools(ToolRequest) []ModelToolDefinition {
	return []ModelToolDefinition{{Name: "life_prepare_ledger_entry", Description: "记账", Parameters: map[string]any{"type": "object"}}}
}

func (t *modelNoQueryTools) ExecuteModelTool(context.Context, ToolRequest, ModelToolCall) (ToolResult, error) {
	t.queryCalls++
	return ToolResult{Handled: true, ToolName: "life.ledger.record", Response: "确认后才会写入生活账本。"}, nil
}

type modelNoToolProvider struct{ routingCalls, generateCalls int }

func (p *modelNoToolProvider) Generate(context.Context, character.Character, []Message) (string, Usage, error) {
	p.generateCalls++
	return "普通聊天回答", Usage{Provider: "test-model", Model: "chat"}, nil
}

func (p *modelNoToolProvider) GenerateWithTools(context.Context, character.Character, []Message, []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	p.routingCalls++
	return ModelToolTurn{Call: &ModelToolCall{ID: "call-no-tool", Name: "life_no_tool", Arguments: map[string]any{}}}, Usage{Provider: "test-model", Model: "intent"}, nil
}

type modelNoToolTools struct{ legacyCalls, modelCalls int }

func (t *modelNoToolTools) Execute(context.Context, ToolRequest) (ToolResult, error) {
	t.legacyCalls++
	return ToolResult{Handled: true, ToolName: "legacy", Response: "不应执行"}, nil
}

func (t *modelNoToolTools) ModelTools(ToolRequest) []ModelToolDefinition {
	return []ModelToolDefinition{{Name: "life_no_tool", Description: "普通聊天", Parameters: map[string]any{"type": "object"}}}
}

func (t *modelNoToolTools) ExecuteModelTool(context.Context, ToolRequest, ModelToolCall) (ToolResult, error) {
	t.modelCalls++
	return ToolResult{}, nil
}

func (t *modelQueryTools) Execute(context.Context, ToolRequest) (ToolResult, error) {
	t.legacyCalls++
	return ToolResult{}, nil
}

func (t *modelQueryTools) ModelTools(request ToolRequest) []ModelToolDefinition {
	if request.Module != "life" {
		return nil
	}
	return []ModelToolDefinition{{Name: "life_query_today_plan", Description: "查询真实今日计划", Parameters: map[string]any{"type": "object"}}}
}

func (t *modelQueryTools) ExecuteModelTool(_ context.Context, _ ToolRequest, call ModelToolCall) (ToolResult, error) {
	t.queryCalls++
	if call.Name != "life_query_today_plan" {
		return ToolResult{}, errors.New("unexpected selected tool")
	}
	return ToolResult{Handled: true, ToolName: "life.plan.today", Response: "今天还没有计划或提醒。", Data: map[string]any{"items": []any{}}}, nil
}

type failingProvider struct{}

func (failingProvider) Generate(context.Context, character.Character, []Message) (string, Usage, error) {
	return "", Usage{}, errors.New("model unavailable")
}

func TestSetTimeoutExtendsQueuedJobDeadline(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "timeout-user", character.Input{Name: "阿策"})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), characters, DevelopmentProvider{})
	service.SetAsyncDispatch(true)
	service.SetTimeout(6 * time.Minute)
	conversation, err := service.Create(ctx, "timeout-user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, job, err := service.Send(ctx, "timeout-user", conversation.ID, "处理这个长任务")
	if err != nil {
		t.Fatal(err)
	}
	if remaining := job.DeadlineAt.Sub(started); remaining < 5*time.Minute+59*time.Second {
		t.Fatalf("job timeout = %s, want about 6 minutes", remaining)
	}
}

func TestLifeConversationInjectsWholeTodayPlanContext(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "daily-user", character.Input{Name: "小满", Module: "life"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &contextCaptureProvider{}
	service := NewService(NewMemoryStore(), characters, provider)
	service.SetToolExecutor(dailyContextTools{})
	conversation, err := service.Create(ctx, "daily-user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "daily-user", conversation.ID, "我今天忙不忙？")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		current, _ := service.Job(ctx, "daily-user", job.ID)
		return current.Status == "completed"
	})
	contextFound := false
	for _, message := range provider.history {
		if message.Role == "system" && strings.Contains(message.Content, "整理客户反馈") && strings.Contains(message.Content, "参加评审会") {
			contextFound = true
		}
	}
	if !contextFound {
		t.Fatalf("provider history missing daily plan: %#v", provider.history)
	}
}

func TestLifeFactQueryIsSelectedByModelBeforeDatabaseToolRuns(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "query-user", character.Input{Name: "小满", Module: "life"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &modelQueryProvider{}
	tools := &modelQueryTools{}
	service := NewService(NewMemoryStore(), characters, provider)
	service.SetToolExecutor(tools)
	conversation, err := service.Create(ctx, "query-user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "query-user", conversation.ID, "我今天的计划是什么")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		current, _ := service.Job(ctx, "query-user", job.ID)
		return current.Status == "completed"
	})
	if provider.routingCalls != 1 || provider.generateCalls != 0 || tools.queryCalls != 1 || tools.legacyCalls != 0 {
		t.Fatalf("routing=%d generate=%d query=%d legacy=%d", provider.routingCalls, provider.generateCalls, tools.queryCalls, tools.legacyCalls)
	}
	messages, err := service.Messages(ctx, "query-user", conversation.ID, 0, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != "assistant" || messages[1].Content != "今天还没有计划或提醒。" {
		t.Fatalf("messages = %#v", messages)
	}
	events, err := service.Events(ctx, "query-user", job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type == "model_tool_completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing model tool event: %#v", events)
	}
}

func TestLifeRoutingFewShotsContrastCompletionQueryAndCreation(t *testing.T) {
	prompt := routingFewShotPrompt("life", []ModelToolDefinition{
		{Name: "life_no_tool"},
		{Name: "life_prepare_task_completion"},
		{Name: "life_query_active_reminders"},
		{Name: "life_prepare_today_plan"},
	})
	for _, expected := range []string{
		"我完成了8月10号的选课", "life_prepare_task_completion",
		"8月10号有选课提醒吗", "life_query_active_reminders",
		"把选课加入今日计划", "life_prepare_today_plan",
		"我还没完成选课", "我准备完成选课", "否定", "时态",
		"查询候选→唯一匹配→请求确认", "date_hint",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("few-shot prompt missing %q: %s", expected, prompt)
		}
	}
	if routingFewShotPrompt("work", []ModelToolDefinition{{Name: "life_prepare_task_completion"}}) != "" ||
		routingFewShotPrompt("life", []ModelToolDefinition{{Name: "life_query_active_reminders"}}) != "" {
		t.Fatal("life completion few-shots leaked outside their applicable tool catalog")
	}
}

func TestRoutingReferenceHistorySupportsPronounsWithoutCarryingOldCommands(t *testing.T) {
	history := []Message{
		{Role: "system", Content: "摘要不应作为原始对话注入"},
		{Role: "user", Content: "8月10号提醒我选课"},
		{Role: "assistant", Content: "已为你准备选课提醒。"},
		{Role: "user", Content: "我完成这个了"},
	}
	references := routingReferenceHistory(history, 3)
	if len(references) != 2 || references[0].Role != "user" || references[0].Content != "8月10号提醒我选课" ||
		references[1].Role != "assistant" || strings.Contains(references[1].Content, "我完成这个了") {
		t.Fatalf("routing reference history = %#v", references)
	}
	if current := routingReferenceHistory(history, len(history)); current != nil {
		t.Fatalf("out-of-range query index should not leak history: %#v", current)
	}
}

func TestModelMutationSelectionExecutesWithoutLegacyRuleFallback(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "mutation-user", character.Input{Name: "小满", Module: "life"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &modelNoQueryProvider{}
	tools := &modelNoQueryTools{}
	service := NewService(NewMemoryStore(), characters, provider)
	service.SetToolExecutor(tools)
	conversation, err := service.Create(ctx, "mutation-user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "mutation-user", conversation.ID, "我今天吃饭花了50元")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		current, _ := service.Job(ctx, "mutation-user", job.ID)
		return current.Status == "completed"
	})
	if provider.routingCalls != 1 || provider.generateCalls != 0 || tools.queryCalls != 1 || tools.legacyCalls != 0 {
		t.Fatalf("routing=%d generate=%d query=%d legacy=%d", provider.routingCalls, provider.generateCalls, tools.queryCalls, tools.legacyCalls)
	}
	events, err := service.Events(ctx, "mutation-user", job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	routedEvents := 0
	for _, event := range events {
		if event.Type == "model_tool_routed" {
			routedEvents++
		}
	}
	if routedEvents != 1 {
		t.Fatalf("model_tool_routed events = %d, events=%#v", routedEvents, events)
	}
}

func TestModelNoToolSelectionSkipsLegacyRulesAndUsesNormalChat(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "chat-user", character.Input{Name: "小满", Module: "life"})
	if err != nil {
		t.Fatal(err)
	}
	provider := &modelNoToolProvider{}
	tools := &modelNoToolTools{}
	service := NewService(NewMemoryStore(), characters, provider)
	service.SetToolExecutor(tools)
	conversation, err := service.Create(ctx, "chat-user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "chat-user", conversation.ID, "今天心情不错")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		current, _ := service.Job(ctx, "chat-user", job.ID)
		return current.Status == "completed"
	})
	if provider.routingCalls != 1 || provider.generateCalls != 1 || tools.modelCalls != 1 || tools.legacyCalls != 0 {
		t.Fatalf("routing=%d generate=%d model=%d legacy=%d", provider.routingCalls, provider.generateCalls, tools.modelCalls, tools.legacyCalls)
	}
}

func TestCancelStopsGenerationBeforeAnyBubbleIsPersisted(t *testing.T) {
	characterStore := character.NewMemoryStore()
	characters := character.NewService(characterStore)
	persona, _, err := characters.Create(context.Background(), "user-1", character.Input{Name: "小棉", Personality: "温柔", SpeechStyle: "短句"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	service := NewService(store, characters, blockingProvider{})
	conversation, err := service.Create(context.Background(), "user-1", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(context.Background(), "user-1", conversation.ID, "请陪我聊聊")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, _ := service.Job(context.Background(), "user-1", job.ID)
		if current.Status == "running" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := service.Cancel(context.Background(), "user-1", job.ID); err != nil {
		t.Fatal(err)
	}
	status := ""
	for time.Now().Before(deadline) {
		current, _ := service.Job(context.Background(), "user-1", job.ID)
		status = current.Status
		if IsTerminal(status) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if status != "cancelled" {
		t.Fatalf("status = %s, want cancelled", status)
	}
	messages, err := service.Messages(context.Background(), "user-1", conversation.ID, 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != "assistant" ||
		!strings.Contains(messages[1].Content, "原对话已保留") ||
		!strings.Contains(messages[1].Content, "<!--ai-generation-job:"+job.ID+"|cancelled-->") {
		t.Fatalf("messages after cancel = %#v, want persisted retry feedback", messages)
	}
}

func TestCancelAcceptedAsyncJobTerminatesWithoutWorker(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "user-async", character.Input{Name: "小棉", Personality: "温柔", SpeechStyle: "短句"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	service := NewService(store, characters, DevelopmentProvider{})
	service.SetAsyncDispatch(true)
	conversation, err := service.Create(ctx, "user-async", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "user-async", conversation.ID, "先不要回复")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Cancel(ctx, "user-async", job.ID); err != nil {
		t.Fatal(err)
	}
	current, err := service.Job(ctx, "user-async", job.ID)
	if err != nil || current.Status != "cancelled" || current.CompletedAt == nil {
		t.Fatalf("cancelled job = %#v, %v", current, err)
	}
	events, err := store.ListEvents(ctx, "user-async", job.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].Type != "cancelled" {
		t.Fatalf("events = %#v, %v", events, err)
	}
}

func TestL3AcceptOnlyPersistsMessageWithoutStartingProvider(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "u-l3", character.Input{Name: "小棉", Personality: "温柔", SpeechStyle: "短句"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	provider := &countingProvider{}
	service := NewService(store, characters, provider)
	service.SetPolicySource(staticPolicySource{policy: reliability.Policy{UseFullRAG: false, ExtractMemory: false, PreferredModelClass: "local_only", LongSkillsQueued: true, AcceptOnly: true}})
	conversation, err := service.Create(ctx, "u-l3", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "u-l3", conversation.ID, "模型挂了也别丢这句话")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	current, _ := service.Job(ctx, "u-l3", job.ID)
	if current.Status != "accepted" || provider.calls != 0 {
		t.Fatalf("job=%#v provider calls=%d", current, provider.calls)
	}
	events, _ := store.ListEvents(ctx, "u-l3", job.ID, 0, 10)
	if len(events) != 2 || events[1].Type != "degraded_accept_only" {
		t.Fatalf("events=%#v", events)
	}
}

func TestOpenModelCircuitDefersClaimedJobWithoutFailing(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "u-circuit", character.Input{Name: "小棉", Personality: "温柔", SpeechStyle: "短句"})
	if err != nil {
		t.Fatal(err)
	}
	breaker := reliability.NewCircuitBreaker(1, time.Minute)
	breaker.RecordFailure()
	store := NewMemoryStore()
	service := NewService(store, characters, NewCircuitBreakerProvider(failingProvider{}, breaker))
	service.timeBetweenBubbles = 0
	conversation, err := service.Create(ctx, "u-circuit", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "u-circuit", conversation.ID, "这条请求要等模型恢复")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		current, _ := service.Job(ctx, "u-circuit", job.ID)
		return current.Status == "accepted" && current.ErrorCode == "model_circuit_open"
	})
	current, _ := service.Job(ctx, "u-circuit", job.ID)
	if current.Status != "accepted" || current.CompletedAt != nil {
		t.Fatalf("deferred job=%#v", current)
	}
	events, _ := store.ListEvents(ctx, "u-circuit", job.ID, 0, 10)
	if events[len(events)-1].Type != "deferred" {
		t.Fatalf("events=%#v", events)
	}
}

func TestSplitBubblesBounds(t *testing.T) {
	bubbles := SplitBubbles("第一句话说清背景。第二句话回应感受。第三句话提出行动。第四句话继续支持。第五句话收尾。第六句话不应单独超出上限。")
	if len(bubbles) < 2 || len(bubbles) > 5 {
		t.Fatalf("bubble count = %d", len(bubbles))
	}
	if len(SplitBubbles("短回复")) != 1 {
		t.Fatal("short response should stay in one bubble")
	}
	quoted := SplitBubbles("我会记住重要的是「明天的汇报。」")
	if len(quoted) != 1 || quoted[0] != "我会记住重要的是「明天的汇报。」" {
		t.Fatalf("closing quote split incorrectly: %#v", quoted)
	}
	withFile := SplitBubbles("已完成文件翻译。\n<!--ai-generated-file:run-1|file-1|translated.pdf-->")
	if len(withFile) != 1 || !strings.Contains(withFile[0], "<!--ai-generated-file:run-1|file-1|translated.pdf-->") {
		t.Fatalf("generated file marker split incorrectly: %#v", withFile)
	}
	withLedger := SplitBubbles("已完成账单导出。\n<!--ai-ledger-export:export-1|ledger.xlsx-->")
	if len(withLedger) != 1 || !strings.Contains(withLedger[0], "<!--ai-ledger-export:export-1|ledger.xlsx-->") {
		t.Fatalf("ledger export marker split incorrectly: %#v", withLedger)
	}
}

func TestRecoverInterruptedMarksJobRetryable(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	store.conversations["c1"] = Conversation{ID: "c1", UserID: "u1"}
	store.jobs["j1"] = Job{ID: "j1", ConversationID: "c1", Status: "running"}
	if err := store.RecoverInterrupted(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	job, _ := store.GetJob(context.Background(), "u1", "j1")
	if job.Status != "failed" || job.ErrorCode != "process_interrupted" {
		t.Fatalf("recovered job = %#v", job)
	}
	events, _ := store.ListEvents(context.Background(), "u1", "j1", 0, 10)
	if len(events) != 1 || events[0].Type != "failed" {
		t.Fatalf("recovery events = %#v", events)
	}
}

func TestDeleteConversationHidesHistoryAndCancelsActiveJob(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "delete-user", character.Input{Name: "小棉"})
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	service := NewService(store, characters, DevelopmentProvider{})
	service.SetAsyncDispatch(true)
	item, err := service.Create(ctx, "delete-user", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, job, err := service.Send(ctx, "delete-user", item.ID, "稍后再回复")
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Delete(ctx, "delete-user", item.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(ctx, "delete-user")
	if err != nil || len(listed) != 0 {
		t.Fatalf("listed after delete = %#v, %v", listed, err)
	}
	if _, err = service.Messages(ctx, "delete-user", item.ID, 0, 0, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("messages error = %v, want ErrNotFound", err)
	}
	cancelled, err := service.Job(ctx, "delete-user", job.ID)
	if err != nil || cancelled.Status != "cancelled" || cancelled.ErrorCode != "conversation_deleted" {
		t.Fatalf("cancelled job = %#v, %v", cancelled, err)
	}
	recreated, err := service.Create(ctx, "delete-user", persona.ID)
	if err != nil || recreated.ID == item.ID || recreated.Status != "active" {
		t.Fatalf("recreated conversation = %#v, %v", recreated, err)
	}
}

func TestModelSelectedMemoryIsUsedThenDeletionStopsRecall(t *testing.T) {
	ctx := context.Background()
	characters := character.NewService(character.NewMemoryStore())
	persona, _, err := characters.Create(ctx, "u1", character.Input{Name: "小棉", Personality: "温柔", SpeechStyle: "短句"})
	if err != nil {
		t.Fatal(err)
	}
	memories := memorydomain.NewService(memorydomain.NewMemoryStore())
	store := NewMemoryStore()
	service := NewService(store, characters, DevelopmentProvider{})
	service.timeBetweenBubbles = 0
	service.SetMemoryContext(memories)
	conv, err := service.Create(ctx, "u1", persona.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = memories.SaveFromModel(ctx, "u1", conv.ID, "memory-source", "我不吃香菜"); err != nil {
		t.Fatal(err)
	}
	items, _ := memories.List(ctx, "u1")
	if len(items) != 1 {
		t.Fatalf("memories = %d", len(items))
	}
	_, secondJob, err := service.Send(ctx, "u1", conv.ID, "点菜时可以放香菜吗？")
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, service, "u1", secondJob.ID)
	messages, _ := service.Messages(ctx, "u1", conv.ID, 0, 0, 100)
	found := false
	for _, message := range messages {
		if message.Role == "assistant" && strings.Contains(message.Content, "不吃香菜") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recalled preference missing from %#v", messages)
	}
	if err = memories.Delete(ctx, "u1", items[0].ID); err != nil {
		t.Fatal(err)
	}
	recalled, _ := memories.Recall(ctx, "u1", "香菜", 8)
	if len(recalled) != 0 {
		t.Fatalf("deleted memory recalled: %#v", recalled)
	}
}

func waitForJob(t *testing.T, service *Service, userID, jobID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		job, _ := service.Job(context.Background(), userID, jobID)
		if IsTerminal(job.Status) {
			if job.Status != "completed" {
				t.Fatalf("job status = %s", job.Status)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not complete")
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met")
}
