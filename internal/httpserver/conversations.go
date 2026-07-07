package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/windcry1/ai-companion/internal/conversation"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/planner"
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
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	auth := currentAuth(r)
	message, job, err := s.conversations.Send(r.Context(), auth.User.ID, r.PathValue("conversation_id"), input.Content)
	if err != nil {
		writeConversationError(w, err)
		return
	}
	response := map[string]any{"message": message, "job": job}
	if ledger.LooksLikeCandidate(input.Content) {
		if candidate, candidateErr := s.ledger.ParseCandidate(r.Context(), auth.User.ID, message.ID, input.Content, auth.User.Timezone); candidateErr == nil {
			response["ledger_candidate"] = candidate
		}
	}
	if planner.LooksLikeReminder(input.Content) {
		if candidate, candidateErr := s.planner.ParseReminder(r.Context(), auth.User.ID, message.ID, input.Content, auth.User.Timezone); candidateErr == nil {
			response["reminder_candidate"] = candidate
		}
	}
	writeJSON(w, http.StatusAccepted, response)
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
