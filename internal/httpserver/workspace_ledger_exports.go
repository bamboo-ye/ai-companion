package httpserver

import (
	"net/http"
	"strconv"
)

func (s *Server) shareWorkspaceLedgerExport(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	var input struct {
		ExportID string `json:"export_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.ledger.ShareExportWithWorkspace(r.Context(), userID, workspaceID, input.ExportID); err != nil {
		writeLedgerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listWorkspaceLedgerExports(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	items, err := s.ledger.ListWorkspaceExports(r.Context(), workspaceID)
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) downloadWorkspaceLedgerExport(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	job, data, err := s.ledger.DownloadWorkspaceExport(r.Context(), workspaceID, r.PathValue("export_id"))
	if err != nil {
		writeLedgerError(w, err)
		return
	}
	w.Header().Set("Content-Type", job.MediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+job.FileName+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
