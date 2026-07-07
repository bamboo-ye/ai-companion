package httpserver

import (
	"errors"
	"net/http"

	"github.com/windcry1/ai-companion/internal/memory"
)

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	items, err := s.memories.List(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.memories.Create(r.Context(), currentAuth(r).User.ID, input.Content)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}
func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	var input memory.UpdateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.memories.Update(r.Context(), currentAuth(r).User.ID, r.PathValue("memory_id"), input)
	if err != nil {
		writeMemoryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.memories.Delete(r.Context(), currentAuth(r).User.ID, r.PathValue("memory_id")); err != nil {
		writeMemoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) clearMemories(w http.ResponseWriter, r *http.Request) {
	if err := s.memories.Clear(r.Context(), currentAuth(r).User.ID); err != nil {
		writeMemoryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func writeMemoryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, memory.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "记忆不存在"})
	case errors.Is(err, memory.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "记忆服务暂时不可用"})
	}
}
