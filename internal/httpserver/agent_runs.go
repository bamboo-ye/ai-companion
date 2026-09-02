package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/chatattachment"
)

func (s *Server) getAgentRun(w http.ResponseWriter, r *http.Request) {
	if s.agentRuns == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "Agent 运行时暂不可用"})
		return
	}
	item, err := s.agentRuns.GetForUser(
		r.Context(),
		currentAuth(r).User.ID,
		r.PathValue("run_id"),
	)
	if err != nil {
		writeAgentRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicAgentRun(item))
}

func (s *Server) getActiveAgentRun(w http.ResponseWriter, r *http.Request) {
	if s.agentRuns == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	item, err := s.agentRuns.ActiveForConversation(
		r.Context(), currentAuth(r).User.ID, r.PathValue("conversation_id"),
	)
	if errors.Is(err, agent.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeAgentRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_run": publicAgentRun(item)})
}

func (s *Server) resolveAgentRun(w http.ResponseWriter, r *http.Request) {
	if s.agentRuns == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "Agent 运行时暂不可用"})
		return
	}
	var input struct {
		Approved *bool `json:"approved"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Approved == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "approved 必须是布尔值"})
		return
	}
	item, changed, err := s.agentRuns.ResolveApproval(
		r.Context(),
		currentAuth(r).User.ID,
		r.PathValue("run_id"),
		*input.Approved,
		r.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		writeAgentRunError(w, err)
		return
	}
	status := http.StatusOK
	if changed {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"run": publicAgentRun(item), "deduplicated": !changed})
}

func (s *Server) retryAgentRun(w http.ResponseWriter, r *http.Request) {
	if s.agentRuns == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "Agent 运行时暂不可用"})
		return
	}
	userID := currentAuth(r).User.ID
	runID := r.PathValue("run_id")
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if key := strings.TrimSpace(idempotencyKey); key == "" || len(key) > 128 {
		writeAgentRunError(w, agent.ErrValidation)
		return
	}
	prior, err := s.agentRuns.GetForUser(r.Context(), userID, runID)
	if err != nil {
		writeAgentRunError(w, err)
		return
	}
	var payload map[string]any
	if err = json.Unmarshal(prior.Input, &payload); err != nil || payload == nil {
		writeAgentRunError(w, agent.ErrValidation)
		return
	}
	repaired := false
	if text, ok := payload["text"].(string); ok && len(chatattachment.DocumentIDs(text)) == 0 {
		visible, documentIDs, resolveErr := s.resolveVisibleDocumentReferences(
			r.Context(), userID, text,
		)
		if resolveErr != nil {
			writeJSON(w, http.StatusUnprocessableEntity, apiError{
				Code:    "attachment_reference_invalid",
				Message: "原消息中的附件无法关联到文档库，请重新选择文件后发送",
			})
			return
		}
		if len(documentIDs) > 0 {
			for _, documentID := range documentIDs {
				documentItem, documentErr := s.documents.Get(r.Context(), userID, documentID)
				if documentErr != nil {
					writeDocumentError(w, documentErr)
					return
				}
				visible = chatattachment.AppendDocument(visible, documentItem.ID, documentItem.Name)
			}
			payload["text"] = visible
			repaired = true
		}
	}
	if repaired {
		idempotencyKey = "attachment-repair-v1:" + runID
	}
	item, created, err := s.agentRuns.RetryChatWithPayload(
		r.Context(), userID, runID, idempotencyKey, payload,
	)
	if err != nil {
		writeAgentRunError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{
		"agent_run": publicAgentRun(item), "deduplicated": !created,
	})
}

func (s *Server) cancelAgentRun(w http.ResponseWriter, r *http.Request) {
	if s.agentRuns == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_unavailable", Message: "Agent 运行时暂不可用"})
		return
	}
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key == "" || len(key) > 128 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "Idempotency-Key 必须存在且不超过 128 个字符"})
		return
	}
	item, changed, err := s.agentRuns.Cancel(
		r.Context(),
		currentAuth(r).User.ID,
		r.PathValue("run_id"),
	)
	if err != nil {
		writeAgentRunError(w, err)
		return
	}
	status := http.StatusOK
	if changed {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"run": publicAgentRun(item), "deduplicated": !changed})
}

func writeAgentRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "Agent 运行不存在"})
	case errors.Is(err, agent.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "invalid_state", Message: "Agent 运行状态已变化"})
	case errors.Is(err, agent.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "Agent 运行时暂不可用"})
	}
}

func (s *Server) agentModuleEnabled(module string) bool {
	return s.agentChatModules[strings.TrimSpace(module)]
}

func publicAgentRun(item agent.Run) map[string]any {
	response := map[string]any{
		"id": item.ID, "conversation_id": item.ConversationID,
		"character_id": item.CharacterID, "module": item.Module,
		"status": item.Status, "revision": item.Revision,
		"created_at": item.CreatedAt, "updated_at": item.UpdatedAt,
		"deadline_at": item.DeadlineAt,
	}
	if item.ErrorCode != "" {
		response["error_code"] = item.ErrorCode
	}
	if item.ErrorMessage != "" {
		response["error_message"] = item.ErrorMessage
	}
	if item.CompletedAt != nil {
		response["completed_at"] = item.CompletedAt
	}
	if len(item.Output) > 0 && string(item.Output) != "null" {
		var output any
		if json.Unmarshal(item.Output, &output) == nil {
			if object, ok := output.(map[string]any); ok {
				if interrupts, ok := object["interrupts"].([]any); ok {
					for _, interrupt := range interrupts {
						if value, ok := interrupt.(map[string]any); ok {
							delete(value, "confirmation_token")
						}
					}
				}
			}
			response["output"] = output
		}
	}
	return response
}
