package arbitration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/windcry1/ai-companion/internal/semantic"
)

func fixture() Input {
	return Input{Kind: "wiki", Issues: []Issue{{ID: "budget", Label: "预算", Candidates: []Candidate{{ID: "a", Text: "预算100", EvidenceIDs: []string{"old"}}, {ID: "b", Text: "预算200", EvidenceIDs: []string{"new"}}}}}, Evidence: []Evidence{{ID: "old", Text: "预算100", Version: "v1"}, {ID: "new", Text: "预算200；明确替代旧预算", Version: "v2"}}}
}
func validDecision() Decision {
	return Decision{IssueID: "budget", Action: "accept", Classification: "version_update", SelectedIDs: []string{"b"}, Reason: "来源明确替代", Evidence: []Citation{{ID: "new", Quote: "明确替代旧预算"}}}
}
func TestVerdictRequiresCompleteGroundedSelection(t *testing.T) {
	input := fixture()
	if err := Validate(input, []Decision{validDecision()}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Decision){
		func(d *Decision) { d.SelectedIDs = []string{"invented"} },
		func(d *Decision) { d.Evidence = []Citation{{ID: "other-user", Quote: "预算200"}} },
		func(d *Decision) { d.Evidence = []Citation{{ID: "new", Quote: "伪造摘录"}} },
		func(d *Decision) { d.Evidence = []Citation{{ID: "old", Quote: "预算100"}} },
		func(d *Decision) { d.Evidence = []Citation{{ID: "new", Quote: "预算200"}} },
		func(d *Decision) { d.Action = "override_permissions" },
		func(d *Decision) { d.IssueID = "missing" },
	} {
		d := validDecision()
		mutate(&d)
		if Validate(input, []Decision{d}) == nil {
			t.Fatalf("accepted invalid decision: %+v", d)
		}
	}
	if Validate(input, nil) == nil {
		t.Fatal("accepted missing issue")
	}
}

func TestJudgeHasOneCallAndRetainsOnInvalidResponseWithoutLosingUsage(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		out, _ := json.Marshal(map[string]any{"decisions": []Decision{{IssueID: "foreign", Action: "reject"}}})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(out)}}}, "usage": map[string]any{"total_tokens": 99}})
	}))
	defer server.Close()
	client := semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "mock"})
	r := Judge(context.Background(), client, fixture())
	if calls.Load() != 1 || r.Calls != 1 || r.Tokens != 99 || r.Status != "retained" || r.Decisions[0].Action != "retain" {
		t.Fatalf("record=%+v calls=%d", r, calls.Load())
	}
	large := fixture()
	large.Evidence[0].Text = strings.Repeat("a", 32001)
	r = Judge(context.Background(), client, large)
	if calls.Load() != 1 || r.Reason != "input_budget_exceeded" || r.Calls != 0 {
		t.Fatal("oversized input was truncated or dispatched")
	}
}
