package contextengine

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestGoPythonContextReplayContract(t *testing.T) {
	data, err := os.ReadFile("../../evals/context/conversation-context.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != Version || len(snapshot.History) != 2 || snapshot.Summary.Source.EndSequence != 6 || snapshot.Memories[0].Source.MessageID != "original-fact" {
		t.Fatalf("invalid replay: %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var original, roundTrip any
	_ = json.Unmarshal(data, &original)
	_ = json.Unmarshal(encoded, &roundTrip)
	originalJSON, _ := json.Marshal(original)
	roundTripJSON, _ := json.Marshal(roundTrip)
	if string(originalJSON) != string(roundTripJSON) {
		t.Fatal("Go context contract dropped replay fields")
	}
}

func TestMemoryBudgetKeepsWholeFactsAndSkipsOversizedCandidates(t *testing.T) {
	items := []Item{
		{Content: strings.Repeat("不能截断否定条件", 300), Source: Source{Kind: "memory", ID: "large"}},
		{Content: "不吃香菜", Source: Source{Kind: "memory", ID: "small"}},
	}
	selected, tokens, omitted := BoundMemories(items, 100)
	if len(selected) != 1 || selected[0].Source.ID != "small" || tokens > 100 || omitted != 1 {
		t.Fatalf("selection=%#v tokens=%d omitted=%d", selected, tokens, omitted)
	}
}

func TestFullRequestBudgetCountsToolSchemaAndOutputReserve(t *testing.T) {
	payload := map[string]any{"messages": []map[string]string{{"role": "user", "content": "你好"}}}
	if _, err := CheckRequest(payload, 2048, 256); err != nil {
		t.Fatal(err)
	}
	payload["tools"] = []map[string]any{{"description": strings.Repeat("工具参数说明", 300)}}
	if _, err := CheckRequest(payload, 2048, 256); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("oversized schema: %v", err)
	}
	delete(payload, "tools")
	if _, err := CheckRequest(payload, 2048, 1500); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("missing output reserve: %v", err)
	}
}
