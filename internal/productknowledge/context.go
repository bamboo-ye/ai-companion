package productknowledge

import (
	"fmt"
	"strings"

	"github.com/windcry1/ai-companion/internal/contextengine"
)

const ContextBudget = 3600

func containsAny(value string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func isProductQuestion(query string) bool {
	value := strings.ToLower(query)
	if containsAny(value, "伴ai", "ai companion", "ai_companion", "本项目", "这个项目", "该项目", "本应用", "这个应用", "这个软件", "项目介绍", "使用说明", "用户指南", "你能做什么", "有哪些功能", "what can you do") {
		return true
	}
	help := containsAny(value, "怎么", "如何", "哪里", "在哪", "为什么", "教程", "说明", "介绍", "步骤", "限制", "支持", "失败", "无法", "不能", "区别", "入口", "可以吗", "what", "how", "where", "why", "help", "limit", "support", "unable", "can i")
	feature := containsAny(value, "注册", "登录", "账户", "账号", "密码", "角色", "会话", "聊天", "消息", "记忆", "记住", "偏好", "记账", "账本", "提醒", "计划", "工作台", "wiki", "知识库", "文档", "附件", "上传", "ppt", "演示", "翻译", "导出", "下载", "表格", "csv", "xlsx", "docx", "离线", "安装", "后台", "管理员", "模型", "部署", "skill", "agent", "passkey", "upload", "document", "account", "password", "reminder", "ledger", "memory", "offline", "companion")
	return help && feature
}

// Follow-up resolution uses only the immediately preceding user topic. It never
// carries product instructions across an unrelated conversation or invents a
// new task from historical examples. The current legacy message may be present.
func RetrievalQuery(query string, history []contextengine.Message) string {
	if isProductQuestion(query) {
		return query
	}
	if len([]rune(query)) > 100 || !containsAny(strings.ToLower(query), "具体", "步骤", "限制", "然后", "继续", "怎么", "哪里", "在哪", "入口", "详细", "还有", "how", "more", "next", "limit") {
		return ""
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != "user" || history[i].Content == query {
			continue
		}
		if isProductQuestion(history[i].Content) {
			return history[i].Content + " " + query
		}
		break
	}
	return ""
}

func Context(query string, history []contextengine.Message) ([]contextengine.Item, int, int) {
	query = RetrievalQuery(query, history)
	if query == "" {
		return nil, 0, 0
	}
	// Bound lookup CPU independently of the current message's provider limit.
	runes := []rune(query)
	if len(runes) > 1000 {
		query = string(runes[:1000])
	}
	hits, _ := Search(query, 8)
	items := make([]contextengine.Item, 0, len(hits))
	for _, hit := range hits {
		p := hit.Page
		items = append(items, contextengine.Item{
			Content: fmt.Sprintf("[产品指南：%s](/#knowledge=%s)\n来源：%s · %s\n%s", p.Title, p.ID, p.Source, p.Section, p.Body),
			Source:  contextengine.Source{Kind: "builtin_knowledge", ID: p.ID, Version: p.Version},
		})
	}
	selected, tokens, omitted := contextengine.BoundMemories(items, ContextBudget)
	return selected, tokens, omitted
}
