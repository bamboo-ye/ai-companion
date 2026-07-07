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

type failingProvider struct{}

func (failingProvider) Generate(context.Context, character.Character, []Message) (string, Usage, error) {
	return "", Usage{}, errors.New("model unavailable")
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
	if len(messages) != 1 {
		t.Fatalf("messages after cancel = %d, want only user message", len(messages))
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

func TestExplicitMemoryIsUsedThenDeletionStopsRecall(t *testing.T) {
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
	_, firstJob, err := service.Send(ctx, "u1", conv.ID, "记住我不吃香菜")
	if err != nil {
		t.Fatal(err)
	}
	waitForJob(t, service, "u1", firstJob.ID)
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
