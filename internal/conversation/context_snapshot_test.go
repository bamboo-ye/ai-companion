package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/contextengine"
	memorydomain "github.com/windcry1/ai-companion/internal/memory"
	"github.com/windcry1/ai-companion/internal/reliability"
)

func TestSharedContextContainsSummaryRecentGroupsAndSourcedMemory(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	store.conversations["chat"] = Conversation{ID: "chat", UserID: "owner"}
	for index := 1; index <= 130; index++ {
		store.messages["chat"] = append(store.messages["chat"], Message{
			ID: fmt.Sprintf("m%d", index), ConversationID: "chat", UserID: "owner", Role: "user", Sequence: uint64(index), Bubble: 1,
			Content: fmt.Sprintf("第%d轮，项目汇报必须使用中文。", index), CreatedAt: time.Now(),
		})
	}
	memories := memorydomain.NewService(memorydomain.NewMemoryStore())
	fact, err := memories.SaveFromModel(ctx, "owner", "other-chat", "source-message", "项目汇报不要超过六分钟")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = memories.Create(ctx, "other-user", "项目汇报秘密内容")
	service := NewService(store, nil, nil)
	service.SetMemoryContext(memories)
	service.SetContextBudgets(128, 256)
	bundle, err := service.BuildContext(ctx, "owner", "chat", "项目汇报有什么要求")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Version != contextengine.Version || bundle.Summary == nil || len(bundle.History) == 0 || bundle.History[len(bundle.History)-1].ID != "m130" {
		t.Fatalf("missing recent context/summary: %#v", bundle)
	}
	if len(bundle.Memories) != 1 || bundle.Memories[0].Source.ID != fact.ID || bundle.Memories[0].Source.MessageID != "source-message" {
		t.Fatalf("memory provenance: %#v", bundle.Memories)
	}
	if bundle.Summary.Source.EndSequence >= bundle.History[0].Sequence {
		t.Fatal("summary overlaps recent history")
	}
	for _, message := range snapshotMessages(bundle) {
		if message.Role == "system" && (strings.Contains(message.Content, fact.Content) || strings.Contains(message.Content, bundle.Summary.Content)) {
			t.Fatal("reference content elevated to system instructions")
		}
	}
	encoded, _ := json.Marshal(bundle.Manifest)
	if strings.Contains(string(encoded), fact.Content) {
		t.Fatal("manifest contains private text")
	}
	if _, err = service.BuildContext(ctx, "other-user", "chat", "项目汇报"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user read: %v", err)
	}
	if err = memories.Delete(ctx, "owner", fact.ID); err != nil {
		t.Fatal(err)
	}
	next, err := service.BuildContext(ctx, "owner", "chat", "项目汇报")
	if err != nil || len(next.Memories) != 0 {
		t.Fatalf("deleted memory reused: %#v %v", next.Memories, err)
	}
	if next.Summary.Source.ID != bundle.Summary.Source.ID {
		t.Fatal("unchanged history regenerated summary")
	}
}

func TestAgentContextHonorsMemoryDegradationPolicy(t *testing.T) {
	store := NewMemoryStore()
	store.conversations["chat"] = Conversation{ID: "chat", UserID: "owner"}
	service := NewService(store, nil, nil)
	memories := memorydomain.NewService(memorydomain.NewMemoryStore())
	_, _ = memories.Create(context.Background(), "owner", "项目汇报六分钟")
	service.SetMemoryContext(memories)
	service.SetPolicySource(staticPolicySource{policy: reliability.Policy{UseFullRAG: false}})
	bundle, err := service.BuildContext(context.Background(), "owner", "chat", "项目汇报")
	if err != nil || len(bundle.Memories) != 0 || len(bundle.Manifest.Degraded) != 1 {
		t.Fatalf("policy bypass: %#v %v", bundle, err)
	}
}

func TestBuiltinProductKnowledgeGroundsConversationWithoutUploads(t *testing.T) {
	store := NewMemoryStore()
	store.conversations["chat"] = Conversation{ID: "chat", UserID: "owner"}
	service := NewService(store, nil, nil)
	service.SetPolicySource(staticPolicySource{policy: reliability.Policy{UseFullRAG: false}})
	bundle, err := service.BuildContext(context.Background(), "owner", "chat", "如何修改登录密码")
	if err != nil || len(bundle.Knowledge) == 0 || bundle.Manifest.KnowledgeTokens == 0 {
		t.Fatalf("builtin help unavailable: %v %+v", err, bundle)
	}
	if !strings.Contains(bundle.ReferenceText(), "当前密码") {
		t.Fatal("missing complete password guidance")
	}
	for _, m := range snapshotMessages(bundle) {
		if m.Role == "system" && strings.Contains(m.Content, "8–128") {
			t.Fatal("guide elevated to system")
		}
	}
	if _, err = service.BuildContext(context.Background(), "other", "chat", "怎么修改密码"); !errors.Is(err, ErrNotFound) {
		t.Fatal("conversation ownership bypassed")
	}
}
