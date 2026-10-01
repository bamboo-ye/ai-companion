package arbitration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDebateIsolationNoveltyAndBoundedResolution(t *testing.T) {
	for _, mode := range []string{"resolved", "duplicate", "revoked", "ignored_challenge", "write_request", "failed_position"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var revoked atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				var packet struct {
					Focus  string `json:"focus"`
					Packet struct {
						Input      Input               `json:"input"`
						New        map[string][]string `json:"new_evidence_issues"`
						Challenges []Position          `json:"challenges"`
					} `json:"packet"`
					Input     Input      `json:"input"`
					Positions []Position `json:"positions"`
				}
				_ = json.Unmarshal([]byte(body.Messages[len(body.Messages)-1].Content), &packet)
				var output any
				if packet.Focus != "" {
					if len(packet.Packet.New) == 0 && len(packet.Packet.Challenges) > 0 {
						t.Error("peer outputs leaked into independent round")
					}
					if mode == "failed_position" {
						http.Error(w, "fixture unavailable", 500)
						return
					}
					p := Position{Assessments: []Assessment{}, Novelty: []Novelty{}}
					for _, i := range packet.Packet.Input.Issues {
						for _, c := range i.Candidates {
							p.Assessments = append(p.Assessments, Assessment{IssueID: i.ID, CandidateID: c.ID, Stance: "uncertain", Reason: "核对原始依据", Evidence: []Citation{}})
						}
					}
					for id, issues := range packet.Packet.New {
						for _, issue := range issues {
							p.Novelty = append(p.Novelty, Novelty{IssueID: issue, EvidenceID: id, Quote: "最终批复为200元", Reason: "明确最终批复金额"})
						}
					}
					output = p
				} else {
					d := Decision{IssueID: "budget", Action: "need_evidence", Classification: "unresolved", SelectedIDs: []string{}, Reason: "缺少最终批复", Evidence: []Citation{}}
					if len(packet.Input.Evidence) == 2 {
						tool := "read_original"
						if mode == "write_request" {
							tool = "delete_source"
						}
						d.EvidenceRequests = []EvidenceRequest{{Question: "预算最终批复", Discriminates: "最终金额", ToolName: tool, Arguments: map[string]any{"query": "预算"}}}
					} else {
						d = validDecision()
						d.Evidence = append(d.Evidence, Citation{"footnote", "最终批复为200元"})
						if mode != "ignored_challenge" {
							for _, p := range packet.Positions {
								for _, a := range p.Assessments {
									d.AssessmentResponses = append(d.AssessmentResponses, AssessmentResponse{a.ID, "批复确认最终金额", d.Evidence})
								}
							}
						}
					}
					output = map[string]any{"decisions": []Decision{d}}
				}
				encoded, _ := json.Marshal(output)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}, "usage": map[string]any{"total_tokens": 7}})
			}))
			defer server.Close()
			reads := 0
			read := func(context.Context, EvidenceRequest) ([]Supplement, error) {
				reads++
				if mode == "revoked" {
					revoked.Store(true)
				}
				text := "预算最终批复为200元"
				if mode == "duplicate" {
					text = "预算100"
				}
				return []Supplement{{Evidence{"footnote", text, "v1"}, []string{"budget"}}}, nil
			}
			verify := func(context.Context) error {
				if revoked.Load() {
					return fmt.Errorf("revoked")
				}
				return nil
			}
			input := fixture()
			before := inputHash(input)
			result := Debate(context.Background(), semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "mock"}), input, read, verify)
			if result.Calls != int(calls.Load()) || result.Calls > 6 || reads > 2 {
				t.Fatalf("wrong accounting %+v calls=%d reads=%d", result, calls.Load(), reads)
			}
			if before != inputHash(input) {
				t.Fatal("original candidates mutated")
			}
			switch mode {
			case "resolved":
				if result.ResolutionStatus != "resolved" || result.Calls != 6 || result.Tokens != 42 || len(result.Rounds) != 2 || result.Independence[1].Mode != "cross_review" {
					t.Fatalf("%+v", result)
				}
			case "duplicate":
				if result.TerminationReason != "no_new_evidence" || result.Calls != 3 {
					t.Fatalf("%+v", result)
				}
			case "write_request":
				if reads != 0 || result.ResolutionStatus == "resolved" {
					t.Fatalf("%+v", result)
				}
			case "revoked":
				if result.TerminationReason != "evidence_access_changed" || result.Calls != 3 {
					t.Fatalf("%+v", result)
				}
			case "ignored_challenge":
				if result.TerminationReason != "unanswered_assessment" || result.ResolutionStatus == "resolved" {
					t.Fatalf("%+v", result)
				}
			case "failed_position":
				if result.CallStatus != "failed" || result.Calls != 2 {
					t.Fatalf("%+v", result)
				}
			}
		})
	}
}

func TestDebateInputAndExpiredDeadlineNeverCallModel(t *testing.T) {
	input := fixture()
	input.Evidence[0].Text = strings.Repeat("x", 24001)
	client := semantic.New(semantic.Config{SummaryModel: "mock", BaseURL: "http://127.0.0.1:1"})
	if r := Debate(context.Background(), client, input, nil, nil); r.Calls != 0 {
		t.Fatal("oversized input dispatched")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := Debate(ctx, client, fixture(), nil, nil); r.Calls != 0 || r.TerminationReason != "deadline_exceeded" {
		t.Fatalf("%+v", r)
	}
}

func TestDebatePersistsReservationsResultsAndDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": `{"value":"original"}`}}}, "usage": map[string]any{"total_tokens": 11}})
	}))
	defer server.Close()
	var saved []byte
	store := DebatePersistence{Load: func(context.Context, string) ([]byte, error) { return append([]byte(nil), saved...), nil }, Save: func(_ context.Context, _ string, b []byte) error { saved = append([]byte(nil), b...); return nil }}
	client := semantic.New(semantic.Config{BaseURL: server.URL, SummaryModel: "mock"})
	first, deadline, err := persistedModel(context.Background(), client, fixture(), []DebatePersistence{store})
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if tokens, err := first.JSONWithUsage(context.Background(), "prompt", fixture(), 10, &output); err != nil || tokens != 11 {
		t.Fatalf("%d %v", tokens, err)
	}
	second, restored, err := persistedModel(context.Background(), client, fixture(), []DebatePersistence{store})
	if err != nil || !deadline.Equal(restored) {
		t.Fatal("deadline refreshed")
	}
	if tokens, err := second.JSONWithUsage(context.Background(), "prompt", fixture(), 10, &output); err != nil || tokens != 11 || calls.Load() != 1 {
		t.Fatal("completed provider call repeated")
	}
	cached := second.(*checkpointModel)
	key := inputHash([]any{"pending", fixture(), 10})
	cached.state.Calls[key] = cachedModelCall{Status: "pending"}
	_ = cached.save(context.Background())
	third, _, _ := persistedModel(context.Background(), client, fixture(), []DebatePersistence{store})
	if _, err := third.JSONWithUsage(context.Background(), "pending", fixture(), 10, &output); err == nil || calls.Load() != 1 {
		t.Fatal("uncertain provider call repeated")
	}
	if n, tokens := third.(*checkpointModel).usage(); n != 2 || tokens != 11 {
		t.Fatal("persisted usage lost")
	}
}
