package httpserver

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) shareWorkspaceSkillFile(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	var input struct {
		RunID  string `json:"run_id"`
		FileID string `json:"file_id"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.skills.ShareFileWithWorkspace(r.Context(), userID, workspaceID, input.RunID, input.FileID); err != nil {
		writeSkillError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listWorkspaceSkillFiles(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	items, err := s.skills.ListWorkspaceFiles(r.Context(), workspaceID)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) downloadWorkspaceSkillFile(w http.ResponseWriter, r *http.Request) {
	userID := currentAuth(r).User.ID
	workspaceID := r.PathValue("workspace_id")
	if _, err := s.teams.RequireActiveMember(r.Context(), userID, workspaceID); err != nil {
		writeTeamError(w, err)
		return
	}
	file, data, err := s.skills.DownloadWorkspaceFile(r.Context(), workspaceID, r.PathValue("file_id"))
	if err != nil {
		writeSkillError(w, err)
		return
	}
	name := strings.ReplaceAll(strings.ReplaceAll(file.Name, "\"", ""), "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	w.Header().Set("Content-Type", file.MediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
