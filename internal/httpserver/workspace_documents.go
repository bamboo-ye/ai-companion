package httpserver

import (
	"net/http"

	"github.com/windcry1/ai-companion/internal/document"
)

func (s *Server) shareWorkspaceDocument(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	var input struct {
		DocumentID string `json:"document_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.documents.ShareWithWorkspace(r.Context(), userID, workspaceID, input.DocumentID); err != nil {
		writeDocumentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listWorkspaceDocuments(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	items, err := s.documents.ListWorkspace(r.Context(), workspaceID)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) queryWorkspaceDocuments(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	var input document.QueryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.reliability.Policy().UseFullRAG {
		writeJSON(w, http.StatusOK, document.DegradedQueryResult("reliability_policy"))
		return
	}
	result, err := s.documents.QueryWorkspace(r.Context(), workspaceID, input)
	if err != nil {
		writeDocumentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
