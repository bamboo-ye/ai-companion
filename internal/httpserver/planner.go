package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/windcry1/ai-companion/internal/planner"
)

func (s *Server) createPlan(w http.ResponseWriter, r *http.Request) {
	var input planner.PlanInput
	if !decodeJSON(w, r, &input) {
		return
	}
	auth := currentAuth(r)
	if input.Timezone == "" {
		input.Timezone = auth.User.Timezone
	}
	item, err := s.planner.CreatePlan(r.Context(), auth.User.ID, input)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (s *Server) listPlans(w http.ResponseWriter, r *http.Request) {
	items, err := s.planner.ListPlans(r.Context(), currentAuth(r).User.ID, r.URL.Query().Get("date"))
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) createReminderCandidate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Text     string `json:"text"`
		Timezone string `json:"timezone"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	auth := currentAuth(r)
	if input.Timezone == "" {
		input.Timezone = auth.User.Timezone
	}
	item, err := s.planner.ParseReminder(r.Context(), auth.User.ID, "", input.Text, input.Timezone)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (s *Server) confirmReminder(w http.ResponseWriter, r *http.Request) {
	item, created, err := s.planner.ConfirmReminder(r.Context(), currentAuth(r).User.ID, r.PathValue("reminder_id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writePlannerError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"reminder": item, "deduplicated": !created})
}
func (s *Server) listReminders(w http.ResponseWriter, r *http.Request) {
	var start, end *time.Time
	if value := r.URL.Query().Get("start"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writePlannerError(w, planner.ErrValidation)
			return
		}
		start = &parsed
	}
	if value := r.URL.Query().Get("end"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writePlannerError(w, planner.ErrValidation)
			return
		}
		end = &parsed
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.planner.ListReminders(r.Context(), currentAuth(r).User.ID, start, end, limit)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) completeReminder(w http.ResponseWriter, r *http.Request) {
	if err := s.planner.CompleteReminder(r.Context(), currentAuth(r).User.ID, r.PathValue("reminder_id")); err != nil {
		writePlannerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) reportReminderSync(w http.ResponseWriter, r *http.Request) {
	var input planner.SyncResult
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.planner.ReportSystemSync(r.Context(), currentAuth(r).User.ID, r.PathValue("reminder_id"), input)
	if err != nil {
		writePlannerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func writePlannerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, planner.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "计划或提醒不存在"})
	case errors.Is(err, planner.ErrConfirmation):
		writeJSON(w, http.StatusConflict, apiError{Code: "clarification_required", Message: "请先确认提醒的绝对日期和时间"})
	case errors.Is(err, planner.ErrIdempotencyKey):
		writeJSON(w, http.StatusBadRequest, apiError{Code: "idempotency_key_required", Message: "确认提醒必须提供 Idempotency-Key"})
	case errors.Is(err, planner.ErrIdempotencyReuse):
		writeJSON(w, http.StatusConflict, apiError{Code: "idempotency_key_reused", Message: "该 Idempotency-Key 已用于其他提醒"})
	case errors.Is(err, planner.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "计划与提醒服务暂时不可用"})
	}
}
