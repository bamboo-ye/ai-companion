package httpserver

import (
	"errors"
	"net/http"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/team"
)

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.requireQuota(w, r, billing.ResourceWorkspaces) {
		return
	}
	workspace, err := s.teams.CreateWorkspace(r.Context(), currentAuth(r).User, input.Name)
	if err != nil {
		writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"workspace": workspace})
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	items, err := s.teams.ListWorkspaces(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listWorkspaceMembers(w http.ResponseWriter, r *http.Request) {
	items, err := s.teams.ListMembers(r.Context(), currentAuth(r).User.ID, r.PathValue("workspace_id"))
	if err != nil {
		writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createWorkspaceInvitation(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	invitation, err := s.teams.Invite(r.Context(), currentAuth(r).User, r.PathValue("workspace_id"), input.Email, input.Role)
	if err != nil {
		writeTeamError(w, err)
		return
	}
	delivery, err := s.emails.QueueWorkspaceInvitation(r.Context(), currentAuth(r).User, invitation)
	if err != nil {
		writeJSON(w, http.StatusCreated, map[string]any{"invitation": invitation, "email_delivery_error": true})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invitation": invitation, "email_delivery": delivery})
}

func (s *Server) listWorkspaceInvitations(w http.ResponseWriter, r *http.Request) {
	items, err := s.teams.ListInvitations(r.Context(), currentAuth(r).User.ID, r.PathValue("workspace_id"))
	if err != nil {
		writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) acceptWorkspaceInvitation(w http.ResponseWriter, r *http.Request) {
	member, invitation, err := s.teams.AcceptInvitation(r.Context(), currentAuth(r).User, r.PathValue("invitation_id"))
	if err != nil {
		writeTeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"member": member, "invitation": invitation})
}

func writeTeamError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, team.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	case errors.Is(err, team.ErrForbidden):
		writeJSON(w, http.StatusForbidden, apiError{Code: "forbidden", Message: "无权操作该团队空间"})
	case errors.Is(err, team.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "团队空间或邀请不存在"})
	case errors.Is(err, team.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "conflict", Message: "团队空间状态冲突或邀请已存在"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "team_unavailable", Message: "团队服务暂时不可用"})
	}
}
