package httpserver

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/windcry1/ai-companion/internal/agent"
	"github.com/windcry1/ai-companion/internal/ledger"
	"github.com/windcry1/ai-companion/internal/planner"
	"github.com/windcry1/ai-companion/internal/skill"
)

func (s *Server) requireAgentService(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "Agent 服务认证无效"})
			return
		}
		expected := strings.TrimSpace(s.agentGatewayToken)
		if expected == "" {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unconfigured", Message: "Agent 工具网关未配置"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) != 1 {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "Agent 服务认证无效"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) prepareAgentTool(w http.ResponseWriter, r *http.Request) {
	if s.agentGateway == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unavailable", Message: "Agent 工具网关暂不可用"})
		return
	}
	var input agent.ToolPrepareInput
	if !decodeJSON(w, r, &input) {
		return
	}
	revision, ok := agentRunRevision(w, r)
	if !ok {
		return
	}
	input.ExpectedRevision = revision
	result, err := s.agentGateway.Prepare(r.Context(), r.PathValue("run_id"), input)
	if err != nil {
		writeAgentGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) listAgentTools(w http.ResponseWriter, r *http.Request) {
	if s.agentGateway == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unavailable", Message: "Agent 工具网关暂不可用"})
		return
	}
	revision, ok := agentRunRevision(w, r)
	if !ok {
		return
	}
	items, err := s.agentGateway.Definitions(r.Context(), r.PathValue("run_id"), revision)
	if err != nil {
		writeAgentGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) commitAgentTool(w http.ResponseWriter, r *http.Request) {
	if s.agentGateway == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unavailable", Message: "Agent 工具网关暂不可用"})
		return
	}
	var input agent.ToolCommitInput
	if !decodeJSON(w, r, &input) {
		return
	}
	revision, ok := agentRunRevision(w, r)
	if !ok {
		return
	}
	input.ExpectedRevision = revision
	result, err := s.agentGateway.Commit(r.Context(), r.PathValue("run_id"), input)
	if err != nil {
		writeAgentGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) observeAgentTask(w http.ResponseWriter, r *http.Request) {
	if s.agentGateway == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unavailable", Message: "Agent 工具网关暂不可用"})
		return
	}
	revision, ok := agentRunRevision(w, r)
	if !ok {
		return
	}
	result, err := s.agentGateway.ObserveTask(
		r.Context(), r.PathValue("run_id"), r.PathValue("task_id"), revision,
	)
	if err != nil {
		writeAgentGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) retryAgentTask(w http.ResponseWriter, r *http.Request) {
	if s.agentGateway == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "agent_gateway_unavailable", Message: "Agent 工具网关暂不可用"})
		return
	}
	var input agent.ToolRetryInput
	if !decodeJSON(w, r, &input) {
		return
	}
	revision, ok := agentRunRevision(w, r)
	if !ok {
		return
	}
	input.ExpectedRevision = revision
	result, err := s.agentGateway.RetryTask(
		r.Context(), r.PathValue("run_id"), r.PathValue("task_id"), input,
	)
	if err != nil {
		writeAgentGatewayError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func agentRunRevision(w http.ResponseWriter, r *http.Request) (int, bool) {
	value, err := strconv.Atoi(strings.TrimSpace(r.Header.Get("X-Agent-Run-Revision")))
	if err != nil || value <= 0 {
		writeJSON(w, http.StatusBadRequest, apiError{
			Code: "agent_revision_required", Message: "Agent 运行 revision 缺失或无效",
		})
		return 0, false
	}
	return value, true
}

func writeAgentGatewayError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound),
		errors.Is(err, ledger.ErrNotFound),
		errors.Is(err, planner.ErrNotFound),
		errors.Is(err, skill.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "Agent 运行或工具资源不存在"})
	case errors.Is(err, agent.ErrToolDenied):
		writeJSON(w, http.StatusForbidden, apiError{Code: "tool_denied", Message: "当前角色无权调用该工具"})
	case errors.Is(err, agent.ErrInvalidToken):
		writeJSON(w, http.StatusForbidden, apiError{Code: "confirmation_token_invalid", Message: "确认令牌无效或已过期"})
	case errors.Is(err, agent.ErrConflict),
		errors.Is(err, agent.ErrConfirmationState),
		errors.Is(err, ledger.ErrConfirmation),
		errors.Is(err, ledger.ErrIdempotencyReuse),
		errors.Is(err, planner.ErrConfirmation),
		errors.Is(err, planner.ErrIdempotencyReuse),
		errors.Is(err, planner.ErrStaleUpdate),
		errors.Is(err, skill.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "conflict", Message: "Agent 工具状态已变化，请重新执行"})
	case errors.Is(err, agent.ErrValidation),
		errors.Is(err, ledger.ErrValidation),
		errors.Is(err, ledger.ErrIdempotencyKey),
		errors.Is(err, planner.ErrValidation),
		errors.Is(err, planner.ErrIdempotencyKey),
		errors.Is(err, planner.ErrDayPeriodRequired),
		errors.Is(err, planner.ErrPastSchedule),
		errors.Is(err, skill.ErrValidation),
		errors.Is(err, skill.ErrIdempotencyKey):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "Agent 工具网关暂时不可用"})
	}
}
