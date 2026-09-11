package httpserver

import (
	"errors"
	"net/http"
	"strings"

	"github.com/windcry1/ai-companion/internal/billing"
)

func (s *Server) getBillingSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := s.billing.Summary(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeBillingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) requireQuota(w http.ResponseWriter, r *http.Request, resource string) bool {
	if err := s.billing.Check(r.Context(), currentAuth(r).User.ID, resource); err != nil {
		writeBillingError(w, err)
		return false
	}
	return true
}

func (s *Server) getOperatorBillingUsage(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	if _, ok := s.requireIdentityAdmin(w); !ok {
		return
	}
	userID := r.PathValue("user_id")
	if _, err := s.identityAdmin.GetUser(r.Context(), userID); err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "用户不存在"})
		return
	}
	summary, err := s.billing.Summary(r.Context(), userID)
	if err != nil {
		writeBillingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) listOperatorBillingAdjustments(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") {
		return
	}
	if _, ok := s.requireIdentityAdmin(w); !ok {
		return
	}
	items, err := s.billing.UsageAdjustments(r.Context(), r.PathValue("user_id"), queryLimitMax(r, 100, 500))
	if err != nil {
		writeBillingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"adjustments": items})
}

func (s *Server) createOperatorBillingAdjustment(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") || !s.requireSensitiveOperatorMFA(w, r) {
		return
	}
	if _, ok := s.requireIdentityAdmin(w); !ok {
		return
	}
	userID := r.PathValue("user_id")
	if _, err := s.identityAdmin.GetUser(r.Context(), userID); err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "用户不存在"})
		return
	}
	var input struct {
		Resource string `json:"resource"`
		Delta    int    `json:"delta"`
		Reason   string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	item, err := s.billing.AdjustUsage(r.Context(), billing.CreateUsageAdjustmentInput{
		UserID: userID, Resource: strings.TrimSpace(input.Resource), Delta: input.Delta,
		Reason: strings.TrimSpace(input.Reason), Actor: currentOperator(r).Actor,
	})
	if err != nil {
		writeBillingError(w, err)
		return
	}
	summary, err := s.billing.Summary(r.Context(), userID)
	if err != nil {
		writeBillingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"adjustment": item, "summary": summary})
}

func (s *Server) requireSensitiveOperatorMFA(w http.ResponseWriter, r *http.Request) bool {
	operator := currentOperator(r)
	if !operator.MFA && !(operator.Legacy && !s.operatorMFARequired) {
		writeJSON(w, http.StatusForbidden, apiError{Code: "operator_step_up_mfa_required", Message: "敏感额度调整必须使用已验证 MFA 的运维账号"})
		return false
	}
	return true
}

func writeBillingError(w http.ResponseWriter, err error) {
	var quota billing.QuotaError
	switch {
	case errors.As(err, &quota):
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"code":      "quota_exceeded",
			"message":   "当前套餐额度已用完，请升级或等待额度重置",
			"resource":  quota.Resource,
			"plan_code": quota.PlanCode,
			"used":      quota.Used,
			"limit":     quota.Limit,
		})
	case errors.Is(err, billing.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	case errors.Is(err, billing.ErrNotFound):
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "billing_usage_ledger_unavailable", Message: "统一用量账本暂未启用"})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "billing_unavailable", Message: "套餐与额度服务暂时不可用"})
	}
}
