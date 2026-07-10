package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/windcry1/ai-companion/internal/billing"
	"github.com/windcry1/ai-companion/internal/skill"
)

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	items, err := s.skills.Skills(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updateSkillSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "enabled 是必填布尔值"})
		return
	}
	manifest, err := s.skills.SetEnabled(r.Context(), currentAuth(r).User.ID, r.PathValue("skill_name"), *input.Enabled)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) startSkillRun(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Input map[string]any `json:"input"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	manifest, err := s.skills.ManifestForUser(r.Context(), currentAuth(r).User.ID, r.PathValue("skill_name"))
	if err != nil {
		writeSkillError(w, err)
		return
	}
	if err = s.userSafety.CheckSkillRisk(r.Context(), currentAuth(r).User.ID, manifest.Name, manifest.RiskLevel); err != nil {
		writeSafetyError(w, err)
		return
	}
	if !s.requireQuota(w, r, billing.ResourceSkillRuns) {
		return
	}
	run, created, err := s.skills.Start(r.Context(), currentAuth(r).User.ID, r.PathValue("skill_name"), r.Header.Get("Idempotency-Key"), input.Input)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	} else if run.Status == "waiting_confirmation" || run.Status == "queued" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"run": run, "deduplicated": !created})
}

func (s *Server) listSkillRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.skills.List(r.Context(), currentAuth(r).User.ID, limit)
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs})
}

func (s *Server) getSkillRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.skills.Get(r.Context(), currentAuth(r).User.ID, r.PathValue("run_id"))
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) confirmSkillRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.skills.Confirm(r.Context(), currentAuth(r).User.ID, r.PathValue("run_id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) cancelSkillRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.skills.Cancel(r.Context(), currentAuth(r).User.ID, r.PathValue("run_id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) retrySkillRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.skills.Retry(r.Context(), currentAuth(r).User.ID, r.PathValue("run_id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) downloadSkillFile(w http.ResponseWriter, r *http.Request) {
	file, data, err := s.skills.Download(r.Context(), currentAuth(r).User.ID, r.PathValue("run_id"), r.PathValue("file_id"))
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

func writeSkillError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skill.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "Skill 或任务不存在"})
	case errors.Is(err, skill.ErrIdempotencyKey):
		writeJSON(w, http.StatusBadRequest, apiError{Code: "idempotency_key_required", Message: "该操作必须提供 Idempotency-Key"})
	case errors.Is(err, skill.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "invalid_state", Message: "任务状态或幂等键冲突"})
	case errors.Is(err, skill.ErrDisabled):
		writeJSON(w, http.StatusForbidden, apiError{Code: "skill_disabled", Message: "该 Skill 已停用"})
	case errors.Is(err, skill.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "Skill Runtime 暂时不可用"})
	}
}
