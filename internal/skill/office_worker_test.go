package skill

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseOfficeWorkerFailurePreservesRepairContract(t *testing.T) {
	failure, ok := parseOfficeWorkerFailure(`{"contract_version":"tool-failure-v1","code":"invalid_output_filename","category":"argument_validation","phase":"pre_execution","message":"safe","retry_same_input":false,"repairable":true,"side_effect_state":"none","field_paths":["/filename"],"allowed_repairs":["filename.safe_basename"]}`)
	if !ok || failure.Code != "invalid_output_filename" || !failure.Repairable || failure.SideEffectState != "none" {
		t.Fatalf("failure = %#v ok=%v", failure, ok)
	}
	err := NewToolExecutionError(failure, errors.New("worker exit"))
	if structured := toolFailureFromError(err, "tool_failed"); structured.Code != failure.Code {
		t.Fatalf("structured = %#v", structured)
	}
	if _, accepted := parseOfficeWorkerFailure(`{"contract_version":"tool-failure-v0","code":"invalid_output_filename"}`); accepted {
		t.Fatal("incompatible failure contract was accepted")
	}
}

type fakeOfficeWorker struct{ calls []string }

func (w *fakeOfficeWorker) Execute(_ context.Context, operation string, _ map[string]any) (ToolResult, error) {
	w.calls = append(w.calls, operation)
	switch operation {
	case "docx_edit":
		return ToolResult{Output: map[string]any{
			"source_filename": "source.docx", "output_filename": "source-edited.docx", "change_summary": "新增 1 个段落",
			"appended_paragraphs": float64(1), "source_overwritten": false,
		}, Files: []FileOutput{{Name: "source-edited.docx", MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Data: []byte("docx-copy")}}}, nil
	case "tabular_profile":
		return ToolResult{Output: map[string]any{
			"analysis_version": "office-tools-tabular-v1", "source_filename": "sample.csv", "sheet_name": "CSV", "row_count": float64(2), "column_count": float64(2),
			"duplicate_rows": float64(0), "truncated": false, "columns": []any{}, "source_overwritten": false,
		}, Files: []FileOutput{{Name: "sample-analysis.json", MediaType: "application/json", Data: []byte(`{"row_count":2}`)}}}, nil
	case "pptx_generate":
		return ToolResult{Output: map[string]any{
			"title": "课程介绍", "audience": "学生", "style": "简洁", "slide_count": float64(4),
			"outline": []any{}, "source_overwritten": false,
		}, Files: []FileOutput{{Name: "课程介绍.pptx", MediaType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", Data: []byte("pptx")}}}, nil
	default:
		return ToolResult{}, ErrNotFound
	}
}

func TestPPTXGenerationRunsWithoutConfirmation(t *testing.T) {
	worker := &fakeOfficeWorker{}
	registry := NewRegistry()
	if err := RegisterOfficeSkills(registry, worker); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	run, _, err := service.Start(context.Background(), "u1", "office.pptx_generate", "pptx-create", map[string]any{
		"title": "课程介绍", "audience": "学生", "style": "简洁", "brief": "课程目标与安排", "slide_count": float64(4),
	})
	if err != nil || run.Status != "succeeded" || run.RequiresConfirmation || run.RiskLevel != "none" || len(run.Files) != 1 || len(worker.calls) != 1 {
		t.Fatalf("run = %#v calls=%v err=%v", run, worker.calls, err)
	}
}

func TestOfficeFileSkillsRespectConfirmationAndNoOverwrite(t *testing.T) {
	worker := &fakeOfficeWorker{}
	registry := NewRegistry()
	if err := RegisterOfficeSkills(registry, worker); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	candidate, _, err := service.Start(context.Background(), "u1", "office.docx_edit", "docx-create", map[string]any{
		"source_filename": "source.docx", "source_base64": base64.StdEncoding.EncodeToString([]byte("source")), "append_text": "新增内容",
	})
	if err != nil || candidate.Status != "waiting_confirmation" || len(candidate.Files) != 0 || len(worker.calls) != 0 {
		t.Fatalf("candidate = %#v calls=%v err=%v", candidate, worker.calls, err)
	}
	confirmed, err := service.Confirm(context.Background(), "u1", candidate.ID, "confirm-docx")
	if err != nil || confirmed.Status != "succeeded" || len(confirmed.Files) != 1 || len(worker.calls) != 1 || !strings.Contains(string(confirmed.Output), `"source_overwritten":false`) {
		t.Fatalf("confirmed = %#v calls=%v err=%v", confirmed, worker.calls, err)
	}
	replayed, err := service.Confirm(context.Background(), "u1", candidate.ID, "confirm-docx")
	if err != nil || replayed.ID != confirmed.ID || len(worker.calls) != 1 {
		t.Fatalf("replayed = %#v calls=%v err=%v", replayed, worker.calls, err)
	}
}

func TestTabularProfileRunsWithoutConfirmation(t *testing.T) {
	worker := &fakeOfficeWorker{}
	registry := NewRegistry()
	if err := RegisterOfficeSkills(registry, worker); err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(), NewMemoryFileStore(), registry)
	run, _, err := service.Start(context.Background(), "u1", "office.tabular_profile", "profile-create", map[string]any{
		"source_filename": "sample.csv", "source_base64": base64.StdEncoding.EncodeToString([]byte("a,b\n1,2\n")),
	})
	if err != nil || run.Status != "succeeded" || len(run.Files) != 1 || len(worker.calls) != 1 {
		t.Fatalf("run = %#v calls=%v err=%v", run, worker.calls, err)
	}
}

func TestPythonOfficeWorkerIntegration(t *testing.T) {
	executable := "../../workers/python/.venv/bin/python"
	if _, err := os.Stat(executable); err != nil {
		t.Skip("python worker environment is not installed")
	}
	worker := PythonOfficeWorker{Executable: executable, ModulePath: "../../workers/python/src", Timeout: 30 * time.Second}
	presentation, err := worker.Execute(context.Background(), "pptx_generate", map[string]any{
		"title": "季度复盘", "audience": "管理层", "style": "简洁", "brief": "业绩亮点\n风险与机会", "slide_count": 4,
	})
	if err != nil || len(presentation.Files) != 1 || !strings.HasPrefix(string(presentation.Files[0].Data), "PK") || presentation.Output["slide_count"] != float64(4) {
		t.Fatalf("presentation = %#v err=%v", presentation, err)
	}
	profile, err := worker.Execute(context.Background(), "tabular_profile", map[string]any{
		"source_filename": "sample.csv", "source_base64": base64.StdEncoding.EncodeToString([]byte("name,amount\nA,10\nB,20\n")),
	})
	if err != nil || len(profile.Files) != 1 || profile.Output["row_count"] != float64(2) {
		t.Fatalf("profile = %#v err=%v", profile, err)
	}
}
