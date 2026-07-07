package skill

import (
	"context"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

func RegisterBuiltins(registry *Registry) error {
	definitions := []Definition{
		{
			Manifest: Manifest{
				Name: "office.translate", Version: "1.0.0", DisplayName: "翻译", Category: "office",
				Description: "保留原文并生成可对照的确定性翻译预览。", RiskLevel: "none", Enabled: true,
				ToolName: "text.translate", TimeoutMS: 5_000, MaxSteps: 8,
				InputSchema: objectSchema([]string{"text", "target_language"}, map[string]any{
					"text": map[string]any{"type": "string"}, "target_language": map[string]any{"type": "string"}, "tone": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"source_text", "target_language", "translated_text"}, map[string]any{
					"source_text": map[string]any{"type": "string"}, "target_language": map[string]any{"type": "string"}, "translated_text": map[string]any{"type": "string"}, "tone": map[string]any{"type": "string"}, "engine": map[string]any{"type": "string"},
				}),
			},
			Handler: HandlerFunc(translateHandler),
		},
		{
			Manifest: Manifest{
				Name: "office.email_draft", Version: "1.0.0", DisplayName: "邮件草稿", Category: "office",
				Description: "只生成邮件草稿，不连接邮箱也不会发送。", RiskLevel: "none", Enabled: true,
				ToolName: "email.draft", TimeoutMS: 5_000, MaxSteps: 8,
				InputSchema: objectSchema([]string{"to", "subject", "purpose"}, map[string]any{
					"to": map[string]any{"type": "array"}, "subject": map[string]any{"type": "string"}, "purpose": map[string]any{"type": "string"}, "tone": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"to", "subject", "body", "send_status"}, map[string]any{
					"to": map[string]any{"type": "array"}, "subject": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "send_status": map[string]any{"type": "string"}, "tone": map[string]any{"type": "string"}, "provider_message_id": map[string]any{"type": "string"},
				}),
			},
			Handler: HandlerFunc(emailDraftHandler),
		},
		{
			Manifest: Manifest{
				Name: "office.markdown_document", Version: "1.0.0", DisplayName: "Markdown 文档", Category: "office",
				Description: "确认后创建新的 Markdown 文件副本，永不覆盖源文件。", RiskLevel: "medium", RequiresConfirmation: true, Enabled: true,
				ToolName: "file.create_markdown", TimeoutMS: 10_000, MaxSteps: 10,
				InputSchema: objectSchema([]string{"title", "content"}, map[string]any{
					"title": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"title", "change_summary", "source_overwritten"}, map[string]any{
					"title": map[string]any{"type": "string"}, "change_summary": map[string]any{"type": "string"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
			},
			Handler: HandlerFunc(markdownHandler),
		},
	}
	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func RegisterOfficeSkills(registry *Registry, worker OfficeWorker) error {
	definitions := []Definition{
		{
			Manifest: Manifest{
				Name: "office.pptx_outline", Version: "1.0.0", DisplayName: "PPTX 大纲", Category: "office",
				Description: "根据受众、页数、风格和简报预览逐页大纲，不创建文件。", RiskLevel: "none", Enabled: true,
				ToolName: "presentation.outline", TimeoutMS: 30_000, MaxSteps: 8, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"title", "audience", "style", "brief", "slide_count"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"title", "audience", "style", "slide_count", "outline", "source_overwritten"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "outline": map[string]any{"type": "array"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
			},
			Handler: officeWorkerHandler(worker, "pptx_outline"),
		},
		{
			Manifest: Manifest{
				Name: "office.docx_edit", Version: "1.0.0", DisplayName: "DOCX 副本编辑", Category: "office",
				Description: "确认后在 DOCX 副本末尾追加内容，不覆盖源文件。", RiskLevel: "medium", RequiresConfirmation: true, Enabled: true,
				ToolName: "file.edit_docx_copy", TimeoutMS: 30_000, MaxSteps: 12, MaxInputBytes: 1 << 20, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"source_filename", "source_base64", "append_text"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "source_base64": map[string]any{"type": "string"}, "append_text": map[string]any{"type": "string"}, "output_filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"source_filename", "output_filename", "change_summary", "appended_paragraphs", "source_overwritten"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "output_filename": map[string]any{"type": "string"}, "change_summary": map[string]any{"type": "string"}, "appended_paragraphs": map[string]any{"type": "integer"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
			},
			Handler: officeWorkerHandler(worker, "docx_edit"),
		},
		{
			Manifest: Manifest{
				Name: "office.pptx_generate", Version: "1.0.0", DisplayName: "PPTX 生成", Category: "office",
				Description: "根据已确认的受众、页数、风格和简报生成演示文稿。", RiskLevel: "medium", RequiresConfirmation: true, Enabled: true,
				ToolName: "file.generate_pptx", TimeoutMS: 30_000, MaxSteps: 12, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"title", "audience", "style", "brief", "slide_count"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"title", "audience", "style", "slide_count", "outline", "source_overwritten"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "outline": map[string]any{"type": "array"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
			},
			Handler: officeWorkerHandler(worker, "pptx_generate"),
		},
		{
			Manifest: Manifest{
				Name: "office.tabular_profile", Version: "1.0.0", DisplayName: "CSV/XLSX 分析", Category: "office",
				Description: "确定性识别表头、类型、缺失值、重复行和数值摘要。", RiskLevel: "none", Enabled: true,
				ToolName: "data.profile_tabular", TimeoutMS: 30_000, MaxSteps: 8, MaxInputBytes: 1 << 20, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"source_filename", "source_base64"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "source_base64": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"analysis_version", "source_filename", "sheet_name", "row_count", "column_count", "duplicate_rows", "truncated", "columns", "source_overwritten"}, map[string]any{
					"analysis_version": map[string]any{"type": "string"}, "source_filename": map[string]any{"type": "string"}, "sheet_name": map[string]any{"type": "string"}, "row_count": map[string]any{"type": "integer"}, "column_count": map[string]any{"type": "integer"}, "duplicate_rows": map[string]any{"type": "integer"}, "truncated": map[string]any{"type": "boolean"}, "columns": map[string]any{"type": "array"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
			},
			Handler: officeWorkerHandler(worker, "tabular_profile"),
		},
	}
	for _, definition := range definitions {
		if err := registry.Register(definition); err != nil {
			return err
		}
	}
	return nil
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
}

func translateHandler(_ context.Context, input map[string]any) (ToolResult, error) {
	text := strings.TrimSpace(input["text"].(string))
	target := strings.TrimSpace(input["target_language"].(string))
	if text == "" || target == "" {
		return ToolResult{}, fmt.Errorf("%w: text and target_language are required", ErrValidation)
	}
	tone, _ := input["tone"].(string)
	if tone == "" {
		tone = "neutral"
	}
	translated := deterministicTranslation(text, target)
	return ToolResult{Output: map[string]any{
		"source_text": text, "target_language": target, "tone": tone, "translated_text": translated,
		"engine": "deterministic-baseline-v1",
	}}, nil
}

func deterministicTranslation(text, target string) string {
	lowerTarget := strings.ToLower(target)
	if strings.Contains(lowerTarget, "english") || lowerTarget == "en" || lowerTarget == "英文" || lowerTarget == "英语" {
		exact := map[string]string{
			"你好": "Hello", "谢谢": "Thank you", "项目已经完成": "The project has been completed.",
			"请查收附件": "Please find the attachment.", "我们明天见": "See you tomorrow.",
		}
		if value, ok := exact[strings.TrimRight(text, "。.!！")]; ok {
			return value
		}
		return strings.NewReplacer("你好", "Hello", "谢谢", "Thank you", "请", "Please ", "明天", "tomorrow", "完成", "completed").Replace(text)
	}
	if strings.Contains(lowerTarget, "chinese") || lowerTarget == "zh" || lowerTarget == "中文" || lowerTarget == "汉语" {
		exact := map[string]string{"Hello": "你好", "Thank you": "谢谢", "See you tomorrow": "明天见。", "The project has been completed": "项目已经完成。"}
		if value, ok := exact[strings.TrimRight(text, "。.!！")]; ok {
			return value
		}
		return strings.NewReplacer("Hello", "你好", "Thank you", "谢谢", "tomorrow", "明天", "completed", "已完成").Replace(text)
	}
	return fmt.Sprintf("[%s] %s", target, text)
}

func emailDraftHandler(_ context.Context, input map[string]any) (ToolResult, error) {
	rawRecipients, _ := input["to"].([]any)
	recipients := make([]string, 0, len(rawRecipients))
	for _, value := range rawRecipients {
		address := strings.TrimSpace(fmt.Sprint(value))
		parsed, err := mail.ParseAddress(address)
		if err != nil || parsed.Address != address {
			return ToolResult{}, fmt.Errorf("%w: invalid recipient %q", ErrValidation, address)
		}
		recipients = append(recipients, address)
	}
	if len(recipients) == 0 {
		return ToolResult{}, fmt.Errorf("%w: at least one recipient is required", ErrValidation)
	}
	subject := strings.TrimSpace(input["subject"].(string))
	purpose := strings.TrimSpace(input["purpose"].(string))
	if subject == "" || purpose == "" {
		return ToolResult{}, fmt.Errorf("%w: subject and purpose are required", ErrValidation)
	}
	tone, _ := input["tone"].(string)
	opening, closing := "您好：", "祝好"
	if tone == "friendly" {
		opening, closing = "你好！", "谢谢，期待你的回复。"
	}
	body := opening + "\n\n" + purpose + "\n\n" + closing
	return ToolResult{Output: map[string]any{
		"to": recipients, "subject": subject, "body": body, "tone": tone, "send_status": "draft_only", "provider_message_id": "",
	}}, nil
}

var unsafeFilename = regexp.MustCompile(`[^a-zA-Z0-9\p{Han}._-]+`)

func markdownHandler(_ context.Context, input map[string]any) (ToolResult, error) {
	title := strings.TrimSpace(input["title"].(string))
	content := strings.TrimSpace(input["content"].(string))
	if title == "" || content == "" {
		return ToolResult{}, fmt.Errorf("%w: title and content are required", ErrValidation)
	}
	filename, _ := input["filename"].(string)
	filename = strings.TrimSpace(filename)
	if filename == "" {
		filename = title
	}
	filename = strings.Trim(unsafeFilename.ReplaceAllString(filename, "-"), ".-")
	if filename == "" {
		filename = "document"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".md") {
		filename += ".md"
	}
	data := []byte("# " + title + "\n\n" + content + "\n")
	return ToolResult{
		Output: map[string]any{"title": title, "change_summary": "创建新的 Markdown 文档副本", "source_overwritten": false},
		Files:  []FileOutput{{Name: filename, MediaType: "text/markdown; charset=utf-8", Data: data}},
	}, nil
}
