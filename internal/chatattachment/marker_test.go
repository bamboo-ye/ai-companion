package chatattachment

import "testing"

func TestVisibleTextRemovesAllInternalMetadata(t *testing.T) {
	content := "工作任务已执行完成。\n" +
		"<!--ai-generated-file:20acbdb0-49ab-4fb5-8263-f338f3a94a27|df451b72-8599-4e2f-9c80-3d7bc46af326|体育课程表整理-Semester-A-2026-2027.pptx-->\n" +
		"<!--ai-skill-run:20acbdb0-49ab-4fb5-8263-f338f3a94a27|1|succeeded-->"
	if visible := VisibleText(content); visible != "工作任务已执行完成。" {
		t.Fatalf("VisibleText() = %q", visible)
	}
}

func TestVisibleTextPreservesOrdinaryHTMLComments(t *testing.T) {
	content := "正文 <!--ordinary-comment-->"
	if visible := VisibleText(content); visible != content {
		t.Fatalf("VisibleText() = %q", visible)
	}
}

func TestVisibleDocumentNamesOnlyAcceptsStandaloneAttachmentLabels(t *testing.T) {
	content := "请整理课表\n📎 courses.pdf\n正文中提到 📎 ignored.pdf"
	if names := VisibleDocumentNames(content); len(names) != 1 || names[0] != "courses.pdf" {
		t.Fatalf("VisibleDocumentNames() = %#v", names)
	}
	if visible := RemoveVisibleDocumentNames(content); visible != "请整理课表\n\n正文中提到 📎 ignored.pdf" {
		t.Fatalf("RemoveVisibleDocumentNames() = %q", visible)
	}
}
