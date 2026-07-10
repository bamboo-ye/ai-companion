package httpserver

import (
	"errors"
	"net/http"

	"github.com/windcry1/ai-companion/internal/safety"
)

func (s *Server) getSafetyPolicy(w http.ResponseWriter, r *http.Request) {
	policy, err := s.userSafety.Get(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeSafetyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (s *Server) updateSafetyPolicy(w http.ResponseWriter, r *http.Request) {
	var input safety.UpdateUserPolicy
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.MinorMode == nil && input.GuardianEmail == nil && input.RiskySkillsAllowed == nil {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: "至少需要更新一个安全策略字段"})
		return
	}
	policy, err := s.userSafety.Update(r.Context(), currentAuth(r).User.ID, input)
	if err != nil {
		writeSafetyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func writeSafetyError(w http.ResponseWriter, err error) {
	var capability safety.CapabilityError
	switch {
	case errors.As(err, &capability):
		writeJSON(w, http.StatusForbidden, map[string]any{
			"code":       "safety_capability_blocked",
			"message":    "当前安全策略不允许使用该高风险能力",
			"skill_name": capability.SkillName,
			"risk_level": capability.RiskLevel,
			"minor_mode": capability.MinorMode,
		})
	case errors.Is(err, safety.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "safety_unavailable", Message: "安全策略服务暂时不可用"})
	}
}
