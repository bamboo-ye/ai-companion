package httpserver

import (
	"errors"
	"net/http"

	"github.com/windcry1/ai-companion/internal/character"
)

func (s *Server) createCharacter(w http.ResponseWriter, r *http.Request) {
	var input character.Input
	if !decodeJSON(w, r, &input) {
		return
	}
	item, persona, err := s.characters.Create(r.Context(), currentAuth(r).User.ID, input)
	if err != nil {
		writeCharacterError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"character": item, "persona": persona})
}

func (s *Server) listCharacters(w http.ResponseWriter, r *http.Request) {
	items, err := s.characters.List(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeCharacterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getCharacter(w http.ResponseWriter, r *http.Request) {
	item, err := s.characters.Get(r.Context(), currentAuth(r).User.ID, r.PathValue("character_id"))
	if err != nil {
		writeCharacterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) updateCharacter(w http.ResponseWriter, r *http.Request) {
	var input character.Input
	if !decodeJSON(w, r, &input) {
		return
	}
	item, persona, err := s.characters.Update(r.Context(), currentAuth(r).User.ID, r.PathValue("character_id"), input)
	if err != nil {
		writeCharacterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"character": item, "persona": persona})
}

func (s *Server) deleteCharacter(w http.ResponseWriter, r *http.Request) {
	if err := s.characters.Delete(r.Context(), currentAuth(r).User.ID, r.PathValue("character_id")); err != nil {
		writeCharacterError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listPersonaVersions(w http.ResponseWriter, r *http.Request) {
	items, err := s.characters.PersonaVersions(r.Context(), currentAuth(r).User.ID, r.PathValue("character_id"))
	if err != nil {
		writeCharacterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeCharacterError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, character.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "角色不存在"})
	case errors.Is(err, character.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "服务暂时不可用"})
	}
}
