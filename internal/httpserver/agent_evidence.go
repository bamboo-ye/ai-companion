package httpserver

import (
	"net/http"

	"github.com/windcry1/ai-companion/internal/document"
)

func (s *Server) validateAgentEvidence(w http.ResponseWriter, r *http.Request) {
	if s.agentGateway == nil || s.documents == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unavailable", Message: "证据校验暂不可用"})
		return
	}
	var input struct {
		References []document.EvidenceReference `json:"references"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	revision, ok := agentRunRevision(w, r)
	if !ok {
		return
	}
	user, err := s.agentGateway.EvidenceUser(r.Context(), r.PathValue("run_id"), revision)
	if err != nil {
		writeAgentGatewayError(w, err)
		return
	}
	versions, err := s.documents.ValidateEvidence(r.Context(), user, input.References)
	if err != nil {
		// Do not distinguish missing, revoked, stale or another user's sources.
		writeJSON(w, http.StatusConflict, apiError{Code: "evidence_unavailable", Message: "来源或权限已变化，需重新核对"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}
