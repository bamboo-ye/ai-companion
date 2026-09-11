package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/chatattachment"
	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/document"
)

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CharacterID string `json:"character_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.conversations.Create(r.Context(), currentAuth(r).User.ID, input.CharacterID)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	items, err := s.conversations.List(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if err := s.conversations.Delete(r.Context(), currentAuth(r).User.ID, r.PathValue("conversation_id")); err != nil {
		writeConversationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after_sequence"), 10, 64)
	afterBubble, _ := strconv.Atoi(r.URL.Query().Get("after_bubble"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.conversations.Messages(r.Context(), currentAuth(r).User.ID, r.PathValue("conversation_id"), after, afterBubble, limit)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	allowed, remaining, retryAfter, err := s.realtime.Allow(r.Context(), "chat:user:"+currentAuth(r).User.ID, s.chatRateLimit, s.chatRateWindow)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "rate_limit_unavailable", Message: "限流服务暂时不可用"})
		return
	}
	w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
	if !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, apiError{Code: "rate_limited", Message: "发送太快了，请稍后再试"})
		return
	}
	var input struct {
		Content     string   `json:"content"`
		DocumentIDs []string `json:"document_ids"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	auth := currentAuth(r)
	if !s.requireQuota(w, r, billing.ResourceModelCost) {
		return
	}
	if len(input.DocumentIDs) > 3 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "too_many_attachments", Message: "单条消息最多发送 3 个文件"})
		return
	}
	visibleContent := strings.TrimSpace(input.Content)
	if len(input.DocumentIDs) == 0 {
		var resolveErr error
		visibleContent, input.DocumentIDs, resolveErr = s.resolveVisibleDocumentReferences(
			r.Context(), auth.User.ID, visibleContent,
		)
		if resolveErr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{
				Code:    "attachment_reference_invalid",
				Message: "消息中的附件无法关联到文档库，请重新选择文件后发送",
			})
			return
		}
	}
	documents := make([]document.Document, 0, len(input.DocumentIDs))
	for _, documentID := range input.DocumentIDs {
		item, documentErr := s.documents.Get(r.Context(), auth.User.ID, documentID)
		if documentErr != nil {
			writeDocumentError(w, documentErr)
			return
		}
		documents = append(documents, item)
	}
	if visibleContent == "" && len(input.DocumentIDs) > 0 {
		visibleContent = "请处理这个文件"
	}
	messageContent := visibleContent
	for _, item := range documents {
		messageContent = chatattachment.AppendDocument(messageContent, item.ID, item.Name)
	}
	forceArtifactAgent := requiresDurableArtifactAgent(messageContent)
	if len(s.agentChatModules) > 0 || forceArtifactAgent {
		conversationItem, conversationErr := s.conversations.Get(
			r.Context(),
			auth.User.ID,
			r.PathValue("conversation_id"),
		)
		if conversationErr != nil {
			writeConversationError(w, conversationErr)
			return
		}
		persona, characterErr := s.characters.Get(
			r.Context(),
			auth.User.ID,
			conversationItem.CharacterID,
		)
		if characterErr != nil {
			writeConversationError(w, conversation.ErrNotFound)
			return
		}
		if s.agentModuleEnabled(persona.Module) || (forceArtifactAgent && persona.Module == "work") {
			if !s.requireQuota(w, r, billing.ResourceAgentRuns) {
				return
			}
			if s.agentRuns == nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "当前模块的 Agent 运行时暂不可用"})
				return
			}
			activeRun, activeErr := s.agentRuns.ActiveForConversation(
				r.Context(), auth.User.ID, conversationItem.ID,
			)
			if activeErr == nil {
				writeJSON(w, http.StatusConflict, map[string]any{
					"code": "agent_run_active", "message": "当前会话已有任务正在执行",
					"agent_run": publicAgentRun(activeRun),
				})
				return
			}
			if !errors.Is(activeErr, agent.ErrNotFound) {
				writeAgentRunError(w, activeErr)
				return
			}
			if validateErr := s.conversations.ValidateContent(messageContent); validateErr != nil {
				writeConversationError(w, validateErr)
				return
			}
			history, historyErr := s.conversations.RecentMessages(
				r.Context(),
				auth.User.ID,
				conversationItem.ID,
				100,
			)
			if historyErr != nil {
				writeConversationError(w, historyErr)
				return
			}
			trustedHistory := make([]map[string]string, 0, len(history))
			for _, prior := range history {
				if prior.Role == "user" || prior.Role == "assistant" {
					trustedHistory = append(trustedHistory, map[string]string{
						"role": prior.Role, "content": prior.Content,
					})
				}
			}
			message, run, acceptErr := s.agentRuns.AcceptChat(r.Context(), agent.AcceptChatInput{
				UserID: auth.User.ID, ConversationID: conversationItem.ID,
				CharacterID: persona.ID, Module: persona.Module, Content: messageContent,
				Context: map[string]any{
					"timezone":      auth.User.Timezone,
					"system_prompt": conversation.PersonaSystemPrompt(persona),
					"history":       trustedHistory,
					"email_profile": map[string]any{
						"sender_name":      auth.User.DisplayName,
						"default_language": defaultEmailLanguage(auth.User.Locale),
						"profile_version":  "account-email-profile-v1",
					},
				},
			})
			if acceptErr != nil {
				writeAgentRunError(w, acceptErr)
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{
				"message": message, "agent_run": publicAgentRun(run), "runtime": "agent",
			})
			return
		}
	}
	message, job, err := s.conversations.Send(r.Context(), auth.User.ID, r.PathValue("conversation_id"), messageContent)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"message": message, "job": job})
}

// resolveVisibleDocumentReferences repairs legacy messages that contain only
// the rendered attachment label ("📎 filename") and have lost document_ids.
// It also repairs explicit follow-ups such as "use the previous courses.pdf"
// where the UI persisted the filename but omitted the attachment marker.
// Resolution is exact-name, user-scoped and newest-first; unresolved labels
// fail closed instead of letting an exhaustive artifact task run source-free.
func (s *Server) resolveVisibleDocumentReferences(
	ctx context.Context,
	userID string,
	content string,
) (string, []string, error) {
	names := chatattachment.VisibleDocumentNames(content)
	inlineReference := len(names) == 0 && explicitlyReferencesPriorDocument(content)
	if len(names) == 0 && !inlineReference {
		return content, nil, nil
	}
	items, err := s.documents.List(ctx, userID)
	if err != nil {
		return content, nil, err
	}
	if inlineReference {
		seen := map[string]bool{}
		visible := strings.ToLower(chatattachment.VisibleText(content))
		for _, item := range items {
			name := strings.TrimSpace(item.Name)
			if name == "" || seen[name] || item.Status == "failed" || item.Status == "deleted" {
				continue
			}
			if strings.Contains(visible, strings.ToLower(name)) {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return content, nil, nil
	}
	if len(names) > 3 {
		return content, nil, document.ErrValidation
	}
	resolved := make([]string, 0, len(names))
	for _, name := range names {
		found := ""
		for _, item := range items {
			if item.Name == name && item.Status != "failed" && item.Status != "deleted" {
				found = item.ID
				break
			}
		}
		if found == "" {
			return content, nil, document.ErrNotFound
		}
		resolved = append(resolved, found)
	}
	return chatattachment.RemoveVisibleDocumentNames(content), resolved, nil
}

func explicitlyReferencesPriorDocument(content string) bool {
	visible := strings.ToLower(chatattachment.VisibleText(content))
	if visible == "" {
		return false
	}
	for _, marker := range []string{
		"上一条", "上一个", "之前", "刚才", "原文件", "源文件", "附件", "文档", "文件",
		"previous", "prior", "above", "attached", "attachment", "document", "file",
	} {
		if strings.Contains(visible, marker) {
			return true
		}
	}
	return false
}

func requiresDurableArtifactAgent(content string) bool {
	visible := strings.ToLower(chatattachment.VisibleText(content))
	if visible == "" {
		return false
	}
	if len(chatattachment.DocumentIDs(content)) > 0 {
		return true
	}
	retry := strings.TrimSpace(strings.NewReplacer(
		"，", "", "。", "", "！", "", "？", "", "!", "", "?", "", ".", "",
	).Replace(visible))
	switch retry {
	case "重试", "再试", "重新执行", "重新开始":
		return true
	}
	hasArtifact := false
	for _, marker := range []string{"ppt", "pptx", "演示文稿", "幻灯片", "xlsx", "excel", "电子表格", "docx", "word 文档", "markdown"} {
		if strings.Contains(visible, marker) {
			hasArtifact = true
			break
		}
	}
	if !hasArtifact {
		return false
	}
	for _, action := range []string{"生成", "创建", "制作", "整理", "展示", "输出", "导出", "create", "generate", "make", "build", "export"} {
		if strings.Contains(visible, action) {
			return true
		}
	}
	return false
}
func (s *Server) getGenerationJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.conversations.Job(r.Context(), currentAuth(r).User.ID, r.PathValue("job_id"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
func (s *Server) cancelGeneration(w http.ResponseWriter, r *http.Request) {
	if err := s.conversations.Cancel(r.Context(), currentAuth(r).User.ID, r.PathValue("job_id")); err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancel_requested"})
}
func (s *Server) retryGeneration(w http.ResponseWriter, r *http.Request) {
	job, err := s.conversations.Retry(r.Context(), currentAuth(r).User.ID, r.PathValue("job_id"))
	if err != nil {
		writeConversationError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) streamGenerationEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "stream_unsupported", Message: "当前连接不支持流式响应"})
		return
	}
	// Generation jobs can legitimately run longer than the server-wide write
	// timeout (for example, PDF translation). Heartbeats and authentication
	// keep this dedicated stream bounded by the request context.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	after, _ := strconv.ParseUint(r.Header.Get("Last-Event-ID"), 10, 64)
	if query, _ := strconv.ParseUint(r.URL.Query().Get("after_event_id"), 10, 64); query > after {
		after = query
	}
	userID := currentAuth(r).User.ID
	jobID := r.PathValue("job_id")
	if _, err := s.conversations.Job(r.Context(), userID, jobID); err != nil {
		writeConversationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		events, err := s.conversations.Events(r.Context(), userID, jobID, after)
		if err != nil {
			return
		}
		for _, event := range events {
			payload, _ := json.Marshal(event.Data)
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.ID, event.Type, payload)
			after = event.ID
			flusher.Flush()
		}
		job, err := s.conversations.Job(r.Context(), userID, jobID)
		if err != nil {
			return
		}
		if conversation.IsTerminal(job.Status) && len(events) == 0 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flusher.Flush()
		case <-ticker.C:
		}
	}
}

func defaultEmailLanguage(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "zh") {
		return "zh-CN"
	}
	return "en-US"
}

func writeConversationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, conversation.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "会话或生成任务不存在"})
	case errors.Is(err, conversation.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "invalid_state", Message: "当前状态不允许此操作"})
	case errors.Is(err, conversation.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "服务暂时不可用"})
	}
}
