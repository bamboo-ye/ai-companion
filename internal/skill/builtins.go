package skill

import (
	"context"
	"fmt"
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
				Name: "office.email_draft", Version: "2.1.0", DisplayName: "邮件草稿", Category: "office",
				Description: "生成经过语言一致性、结构分工、内容去重、礼貌表达、自我介绍和署名校验的邮件草稿，不连接邮箱也不会发送。", RiskLevel: "none", Enabled: true,
				ToolName: "email.draft", TimeoutMS: 5_000, MaxSteps: 8,
				InputSchema: objectSchema([]string{
					"subject", "purpose", "output_language", "relationship", "introduction_policy",
					"sender_name", "salutation", "body_paragraphs", "request_or_next_step",
					"courtesy", "closing", "signature_lines", "tone",
				}, map[string]any{
					"to": map[string]any{"type": "array"}, "subject": map[string]any{"type": "string"}, "purpose": map[string]any{"type": "string"}, "tone": map[string]any{"type": "string"},
					"output_language": map[string]any{"type": "string"}, "relationship": map[string]any{"type": "string"}, "introduction_policy": map[string]any{"type": "string"},
					"sender_name": map[string]any{"type": "string"}, "sender_role": map[string]any{"type": "string"}, "sender_organization": map[string]any{"type": "string"},
					"salutation": map[string]any{"type": "string"}, "introduction": map[string]any{"type": "string"}, "body_paragraphs": map[string]any{"type": "array"},
					"request_or_next_step": map[string]any{"type": "string"}, "courtesy": map[string]any{"type": "string"}, "closing": map[string]any{"type": "string"}, "signature_lines": map[string]any{"type": "array"},
				}),
				OutputSchema: objectSchema([]string{"to", "subject", "body", "send_status"}, map[string]any{
					"to": map[string]any{"type": "array"}, "subject": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "send_status": map[string]any{"type": "string"}, "tone": map[string]any{"type": "string"}, "provider_message_id": map[string]any{"type": "string"},
					"output_language": map[string]any{"type": "string"}, "relationship": map[string]any{"type": "string"}, "email_policy_version": map[string]any{"type": "string"}, "quality_report": map[string]any{"type": "object"},
				}),
			},
			Handler: HandlerFunc(emailDraftHandler),
		},
		{
			Manifest: Manifest{
				Name: "office.markdown_document", Version: "1.1.0", DisplayName: "Markdown 文档", Category: "office",
				Description: "确认后创建新的 Markdown 文件副本，永不覆盖源文件。", RiskLevel: "medium", RequiresConfirmation: true, Enabled: true,
				ToolName: "file.create_markdown", TimeoutMS: 10_000, MaxSteps: 10,
				InputSchema: objectSchema([]string{"title", "content"}, map[string]any{
					"title": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"title", "change_summary", "source_overwritten"}, map[string]any{
					"title": map[string]any{"type": "string"}, "change_summary": map[string]any{"type": "string"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
				RepairPolicies: filenameRepairPolicies(".md"),
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
				Name: "office.pptx_outline", Version: "1.1.0", DisplayName: "PPTX 大纲", Category: "office",
				Description: "根据受众、页数、风格和简报预览逐页大纲，不创建文件。", RiskLevel: "none", Enabled: true,
				ToolName: "presentation.outline", TimeoutMS: 30_000, MaxSteps: 8, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"title", "audience", "style", "brief", "slide_count"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"title", "audience", "style", "slide_count", "outline", "source_overwritten"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "outline": map[string]any{"type": "array"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
				RepairPolicies: filenameRepairPolicies(".pptx"),
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
				Name: "office.pptx_generate", Version: "1.1.0", DisplayName: "PPTX 生成", Category: "office",
				Description: "根据已确认的受众、页数、风格和简报生成演示文稿。", RiskLevel: "medium", RequiresConfirmation: true, Enabled: true,
				ToolName: "file.generate_pptx", TimeoutMS: 30_000, MaxSteps: 12, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"title", "audience", "style", "brief", "slide_count"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "brief": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"title", "audience", "style", "slide_count", "outline", "source_overwritten"}, map[string]any{
					"title": map[string]any{"type": "string"}, "audience": map[string]any{"type": "string"}, "style": map[string]any{"type": "string"}, "slide_count": map[string]any{"type": "integer"}, "outline": map[string]any{"type": "array"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
				RepairPolicies: filenameRepairPolicies(".pptx"),
			},
			Handler: officeWorkerHandler(worker, "pptx_generate"),
		},
		{
			Manifest: Manifest{
				Name: "office.document_extract", Version: "1.0.0", DisplayName: "附件正文提取", Category: "office",
				Description: "从受支持的文本或 PDF 附件中提取结构化正文，供 Agent 独立规划后续任务。", RiskLevel: "none", Enabled: true,
				ToolName: "document.extract_text", TimeoutMS: 120_000, MaxSteps: 8, MaxInputBytes: 30 << 20, ExecutionMode: "worker",
				InputSchema: objectSchema([]string{"source_filename", "source_base64", "media_type"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "source_base64": map[string]any{"type": "string"}, "media_type": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"source_filename", "media_type", "page_count", "character_count", "text", "truncated", "source_overwritten"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "media_type": map[string]any{"type": "string"},
					"page_count": map[string]any{"type": "integer"}, "character_count": map[string]any{"type": "integer"},
					"text": map[string]any{"type": "string"}, "truncated": map[string]any{"type": "boolean"}, "source_overwritten": map[string]any{"type": "boolean"},
				}),
			},
			Handler: officeWorkerHandler(worker, "document_extract"),
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
		{
			Manifest: Manifest{
				Name: "office.pdf_translate", Version: "1.0.0", DisplayName: "PDF 翻译", Category: "office",
				Description: "读取文本型 PDF，使用受预算约束的 OpenRouter 模型翻译正文并生成新的 PDF 文件，不覆盖源文件。", RiskLevel: "none", Enabled: true,
				ToolName: "document.translate_pdf", TimeoutMS: 300_000, MaxSteps: 8, MaxInputBytes: 30 << 20, ExecutionMode: "worker",
				MaxCostMicros: 60_000,
				InputSchema: objectSchema([]string{"source_filename", "source_base64", "target_language"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "source_base64": map[string]any{"type": "string"}, "target_language": map[string]any{"type": "string"},
					"output_filename": map[string]any{"type": "string"},
				}),
				OutputSchema: objectSchema([]string{"source_filename", "output_filename", "target_language", "page_count", "source_overwritten", "model_usage"}, map[string]any{
					"source_filename": map[string]any{"type": "string"}, "output_filename": map[string]any{"type": "string"}, "target_language": map[string]any{"type": "string"},
					"page_count": map[string]any{"type": "integer"}, "source_overwritten": map[string]any{"type": "boolean"}, "model_usage": map[string]any{"type": "object"},
				}),
			},
			Handler: officeWorkerHandler(worker, "pdf_translate"),
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

func filenameRepairPolicies(extension string) []RepairPolicy {
	return []RepairPolicy{
		{OperatorID: "remove_optional_filename", FieldPath: "/filename", Extension: extension, SemanticsPreserving: true, Preflight: true},
		{OperatorID: "filename.safe_basename", FieldPath: "/filename", Extension: extension, SemanticsPreserving: true, Preflight: true},
	}
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
