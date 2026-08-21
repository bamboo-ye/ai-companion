package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/windcry1/ai-companion/internal/agent"
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
	item, created, err := s.agentRuns.RetryChat(
		r.Context(), currentAuth(r).User.ID, r.PathValue("run_id"),
		r.Header.Get("Idempotency-Key"),
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
