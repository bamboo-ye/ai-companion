package router

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const Version = "intent-rules-2026-08-22.v2"

type Input struct {
	Text string `json:"text"`
	Page string `json:"page,omitempty"`
}

type Result struct {
	Intent         string         `json:"intent"`
	Confidence     float64        `json:"confidence"`
	RequiredSlots  []string       `json:"required_slots"`
	RiskLevel      string         `json:"risk_level"`
	SuggestedSkill string         `json:"suggested_skill,omitempty"`
	Slots          map[string]any `json:"slots"`
	Layer          string         `json:"layer"`
	Version        string         `json:"version"`
	Reason         string         `json:"reason"`
}

type Router struct{}

var (
	moneyPattern = regexp.MustCompile(`(?i)(?:¥|￥)?\s*([0-9]+(?:\.[0-9]{1,2})?)\s*(?:元|块|rmb|cny|¥|￥)`)
	emailPattern = regexp.MustCompile(`(?i)[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+`)
	pagePattern  = regexp.MustCompile(`([0-9]{1,2})\s*页`)
)

func New() *Router { return &Router{} }

func (r *Router) Route(input Input) (Result, error) {
	text := strings.TrimSpace(input.Text)
	if text == "" || len([]rune(text)) > 4000 {
		return Result{}, fmt.Errorf("text must contain 1 to 4000 characters")
	}
	lower := strings.ToLower(text)
	result := Result{Intent: "unknown", Confidence: 0.35, RiskLevel: "none", RequiredSlots: []string{}, Slots: map[string]any{}, Layer: "fallback_required", Version: Version, Reason: "规则层未得到稳定分类，等待小模型或用户澄清"}

	if containsAny(lower, "翻译", "译成", "译为", "translate") {
		result = routed("office", 0.98, "none", "office.translate", "命中翻译动作")
		if target := targetLanguage(lower); target != "" {
			result.Slots["target_language"] = target
		} else {
			result.RequiredSlots = append(result.RequiredSlots, "target_language")
		}
		return result, nil
	}
	if containsAny(lower, "邮件", "email", "回信", "回复客户", "回复供应商") {
		result = routed("office", 0.96, "none", "office.email_draft", "命中邮件草稿动作")
		if recipient := emailPattern.FindString(text); recipient != "" {
			result.Slots["to"] = []string{recipient}
		}
		result.RequiredSlots = append(result.RequiredSlots, "subject")
		result.Slots["purpose"] = text
		if language := targetLanguage(lower); language != "" {
			if language == "English" {
				result.Slots["output_language"] = "en-US"
			} else if language == "Chinese" {
				result.Slots["output_language"] = "zh-CN"
			}
		}
		return result, nil
	}
	if containsAny(lower, "docx", "word文档", "word 文档", "编辑文档", "修改文档") {
		result = routed("office", 0.97, "medium", "office.docx_edit", "命中 DOCX 副本编辑动作")
		result.RequiredSlots = []string{"source_file", "append_text"}
		return result, nil
	}
	if containsAny(lower, "ppt", "pptx", "演示文稿", "幻灯片") {
		skill, risk := "office.pptx_generate", "none"
		if strings.Contains(lower, "大纲") {
			skill, risk = "office.pptx_outline", "none"
		}
		result = routed("office", 0.97, risk, skill, "命中演示文稿动作")
		if match := pagePattern.FindStringSubmatch(lower); len(match) == 2 {
			if count, err := strconv.Atoi(match[1]); err == nil && count >= 3 && count <= 20 {
				result.Slots["slide_count"] = count
			}
		}
		result.Slots["brief"] = text
		if audience := presentationAudience(lower); audience != "" {
			result.Slots["audience"] = audience
		}
		if style := presentationStyle(lower); style != "" {
			result.Slots["style"] = style
		}
		for _, slot := range []string{"audience", "slide_count", "style", "brief"} {
			if _, exists := result.Slots[slot]; !exists {
				result.RequiredSlots = append(result.RequiredSlots, slot)
			}
		}
		return result, nil
	}
	if containsAny(lower, "csv", "xlsx", "excel", "表格分析", "分析表格", "数据表分析") {
		result = routed("office", 0.97, "none", "office.tabular_profile", "命中表格分析动作")
		result.RequiredSlots = []string{"source_file"}
		return result, nil
	}

	if containsAny(lower, "提醒", "闹钟", "别忘了", "记得叫我", "到点叫我") {
		result = routed("reminder", 0.97, "medium", "", "命中提醒动作")
		if expression := timeExpression(text); expression != "" {
			result.Slots["time_expression"] = expression
		} else {
			result.RequiredSlots = append(result.RequiredSlots, "due_at")
		}
		result.Slots["title"] = text
		return result, nil
	}

	if moneyPattern.MatchString(lower) || containsAny(lower, "记账", "账本", "支出", "收入") {
		result = routed("ledger", 0.96, "medium", "", "命中金额或记账动作")
		if match := moneyPattern.FindStringSubmatch(lower); len(match) == 2 {
			amount, _ := strconv.ParseFloat(match[1], 64)
			result.Slots["amount_minor"] = int64(amount*100 + 0.5)
			result.Slots["currency"] = "CNY"
		} else {
			result.RequiredSlots = append(result.RequiredSlots, "amount")
		}
		direction := ""
		if containsAny(lower, "工资", "收入", "到账", "收款") {
			direction = "income"
		} else if containsAny(lower, "买", "花", "打车", "吃饭", "支出", "付款", "消费") {
			direction = "expense"
		}
		if direction == "" {
			result.RequiredSlots = append(result.RequiredSlots, "direction")
		} else {
			result.Slots["direction"] = direction
		}
		return result, nil
	}

	if containsAny(lower, "股票", "期货", "商品价格", "价格走势", "金融", "研报", "最大回撤", "波动率") {
		result = routed("finance", 0.94, "low", "", "命中金融研究主题")
		result.RequiredSlots = []string{"data_source"}
		return result, nil
	}
	if containsAny(lower, "生成图片", "画一张", "做个logo", "做个 logo", "海报", "修图", "图片生成") {
		result = routed("image", 0.95, "medium", "", "命中图片生成或编辑动作")
		result.Slots["prompt"] = text
		return result, nil
	}
	if containsAny(lower, "文档", "文件", "pdf") && (strings.ContainsAny(lower, "?？") || containsAny(lower, "什么", "哪一", "如何", "根据", "总结", "回答")) {
		result = routed("document_qa", 0.93, "none", "", "命中文档问答表达")
		result.RequiredSlots = []string{"document"}
		result.Slots["question"] = text
		return result, nil
	}
	if containsAny(lower, "你好", "在吗", "聊聊", "陪我", "心情", "难过", "开心", "谢谢", "晚安", "早安") {
		return routed("casual_chat", 0.9, "none", "", "命中陪伴或闲聊表达"), nil
	}
	if strings.EqualFold(strings.TrimSpace(input.Page), "work") {
		result.Intent, result.Confidence, result.Layer = "office", 0.55, "fallback_required"
		result.RequiredSlots = []string{"task"}
		result.Reason = "工作伙伴页面提供了弱上下文，但仍需澄清任务"
	}
	return result, nil
}

func routed(intent string, confidence float64, risk, skill, reason string) Result {
	return Result{Intent: intent, Confidence: confidence, RequiredSlots: []string{}, RiskLevel: risk, SuggestedSkill: skill, Slots: map[string]any{}, Layer: "rules", Version: Version, Reason: reason}
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func targetLanguage(text string) string {
	for _, item := range []struct{ token, value string }{{"英文", "English"}, {"英语", "English"}, {"english", "English"}, {"日文", "Japanese"}, {"日语", "Japanese"}, {"japanese", "Japanese"}, {"中文", "Chinese"}, {"chinese", "Chinese"}} {
		if strings.Contains(text, item.token) {
			return item.value
		}
	}
	return ""
}

func presentationAudience(text string) string {
	for _, item := range []struct{ token, value string }{{"管理层", "管理层"}, {"客户", "客户"}, {"投资人", "投资人"}, {"投资者", "投资者"}, {"团队", "团队"}, {"员工", "员工"}, {"学生", "学生"}} {
		if strings.Contains(text, item.token) {
			return item.value
		}
	}
	return ""
}

func presentationStyle(text string) string {
	for _, item := range []struct{ token, value string }{{"简洁", "简洁"}, {"专业", "专业"}, {"商务", "商务"}, {"活泼", "活泼"}, {"科技", "科技"}, {"极简", "极简"}} {
		if strings.Contains(text, item.token) {
			return item.value
		}
	}
	return ""
}

func timeExpression(text string) string {
	lower := strings.ToLower(text)
	for _, token := range []string{"今天", "明天", "后天", "今晚", "明早", "早上", "上午", "中午", "下午", "晚上", "周一", "周二", "周三", "周四", "周五", "周六", "周日"} {
		if strings.Contains(lower, token) {
			return text
		}
	}
	if regexp.MustCompile(`[0-9]{1,2}(?::[0-9]{2}|点(?:半|[0-9]{1,2}分)?)`).MatchString(lower) {
		return text
	}
	return ""
}
