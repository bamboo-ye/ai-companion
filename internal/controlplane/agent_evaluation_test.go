package controlplane

import (
	"context"
	"errors"
	"testing"
)

type deterministicEvaluationSandbox struct{}

func (deterministicEvaluationSandbox) Run(_ context.Context, input AgentSandboxRequest) (AgentSandboxReport, error) {
	response := "合成评测回答"
	if models, ok := input.Scenario["models"].(map[string]any); ok {
		if answer, ok := models["answer"].(map[string]any); ok {
			if value, ok := answer["response"].(string); ok {
				response = value
			}
		}
	}
	return AgentSandboxReport{
		SchemaVersion: "agent-studio-synthetic-run/v1", RunID: "sandbox-test",
		Mode: "synthetic", Status: "completed", Outcome: "completed", Response: response,
		Safety:     map[string]any{"side_effects": false, "production_data_access": false, "external_model_calls": 0, "external_tool_calls": 0},
		Budget:     map[string]any{"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 4}},
		NodeTrace:  []map[string]any{{"node": "studio__route", "status": "completed"}, {"node": "studio__answer", "status": "completed"}},
		ModelCalls: []map[string]any{{"role": "router"}, {"role": "responder"}},
		ToolCalls:  []map[string]any{}, ToolSelections: []map[string]any{}, Interrupts: []map[string]any{},
	}, nil
}

func TestAgentEvaluationPersistsResultsAndUnlocksSubmissionGate(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	item, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "evaluated-assistant", Payload: validAgentDefinitionPayload(t),
		Actor: "author-a", Reason: "exercise version evaluation gate",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err = service.Validate(ctx, item.ID, "author-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Submit(ctx, item.ID, "author-a"); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("Submit(without evaluation) error = %v", err)
	}

	run, err := service.EvaluateAgentVersion(ctx, item.ID, passingEvaluationSuite(), "support-a", deterministicEvaluationSandbox{})
	if err != nil || run.Status != EvaluationStatusCompleted || run.Decision != EvaluationDecisionPass ||
		run.Summary.PassedCases != 2 || len(run.Results) != 2 || !run.Results[0].Passed {
		t.Fatalf("EvaluateAgentVersion() = %#v, %v", run, err)
	}
	loaded, err := service.EvaluationRun(ctx, run.ID)
	if err != nil || loaded.SuiteFingerprint != run.SuiteFingerprint || loaded.AgentFingerprint != item.Fingerprint {
		t.Fatalf("EvaluationRun() = %#v, %v", loaded, err)
	}
	runs, err := service.EvaluationRuns(ctx, item.ID, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("EvaluationRuns() = %#v, %v", runs, err)
	}
	if submitted, submitErr := service.Submit(ctx, item.ID, "author-a"); submitErr != nil || submitted.Status != StatusSubmitted {
		t.Fatalf("Submit(after passing evaluation) = %#v, %v", submitted, submitErr)
	}
}

func TestAgentEvaluationFailureDoesNotUnlockSubmission(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	item, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "failing-assistant", Payload: validAgentDefinitionPayload(t),
		Actor: "author-a", Reason: "verify failing evaluation gate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if item, err = service.Validate(ctx, item.ID, "author-a"); err != nil {
		t.Fatal(err)
	}
	suite := passingEvaluationSuite()
	suite.Cases[0].Expect.ResponseContains = []string{"不会出现的内容"}
	run, err := service.EvaluateAgentVersion(ctx, item.ID, suite, "support-a", deterministicEvaluationSandbox{})
	if err != nil || run.Decision != EvaluationDecisionFail || run.Summary.FailedCases != 1 {
		t.Fatalf("EvaluateAgentVersion(fail) = %#v, %v", run, err)
	}
	if _, err = service.Submit(ctx, item.ID, "author-a"); !errors.Is(err, ErrEvaluation) {
		t.Fatalf("Submit(after failing evaluation) error = %v", err)
	}
}

func TestAgentEvaluationComparesACompatiblePassingBaseline(t *testing.T) {
	ctx := context.Background()
	service := NewService(NewMemoryStore(), "test")
	first, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "baseline-assistant", Payload: validAgentDefinitionPayload(t),
		Actor: "author-a", Reason: "create evaluation baseline",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first, err = service.Validate(ctx, first.ID, "author-a"); err != nil {
		t.Fatal(err)
	}
	baseline, err := service.EvaluateAgentVersion(ctx, first.ID, passingEvaluationSuite(), "support-a", deterministicEvaluationSandbox{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(ctx, CreateInput{
		Kind: KindAgentDefinition, Key: "baseline-assistant", BaseVersion: 1, Payload: validAgentDefinitionPayload(t),
		Actor: "author-b", Reason: "compare the next immutable version",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second, err = service.Validate(ctx, second.ID, "author-b"); err != nil {
		t.Fatal(err)
	}
	suite := passingEvaluationSuite()
	suite.BaselineRunID = baseline.ID
	run, err := service.EvaluateAgentVersion(ctx, second.ID, suite, "support-a", deterministicEvaluationSandbox{})
	if err != nil || run.Summary.Comparison == nil || !run.Summary.Comparison.Available ||
		run.Summary.Comparison.BaselineRunID != baseline.ID || run.Summary.Comparison.PassRateDelta != 0 {
		t.Fatalf("EvaluateAgentVersion(with baseline) = %#v, %v", run, err)
	}
}

func passingEvaluationSuite() AgentEvaluationSuite {
	maxCalls, maxSteps, maxRetries, quality := 2, 4, 0, true
	outcome, _ := jsonRaw("completed")
	workCase := AgentEvaluationCase{
		SchemaVersion: AgentEvaluationCaseSchema, ID: "direct-answer", Tags: []string{"direct", "synthetic"},
		Given: AgentEvaluationGiven{
			Module: "work", UserMessage: "给我一个合成回答", Tools: []map[string]any{},
			Context: map[string]any{"synthetic": map[string]any{
				"routes": map[string]any{"route": map[string]any{"condition": "direct"}},
				"models": map[string]any{"answer": map[string]any{"response": "合成评测回答"}},
			}},
		},
		Expect: AgentEvaluationExpect{
			Status: "completed", Outcome: outcome, ExecutionMode: "direct", ToolSequence: []string{},
			RequiredNodes: []string{"route", "answer"}, ForbiddenNodes: []string{"use-tool"},
			MaxModelCalls: &maxCalls, MaxSteps: &maxSteps, MaxRetries: &maxRetries, QualityPass: &quality,
			ResponseContains: []string{"合成评测"}, ResponseNotContains: []string{"生产数据"},
		},
		Tape: map[string]any{},
	}
	lifeCase := workCase
	lifeCase.ID = "direct-answer-life"
	lifeCase.Given.Module = "life"
	return AgentEvaluationSuite{
		SchemaVersion: AgentEvaluationSuiteSchema, Name: "studio-smoke", Version: "v1",
		Cases: []AgentEvaluationCase{workCase, lifeCase},
	}
}

func jsonRaw(value string) ([]byte, error) {
	return []byte(`"` + value + `"`), nil
}
