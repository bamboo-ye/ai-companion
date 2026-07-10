package httpserver

import (
	"errors"
	"net/http"

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
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "billing_unavailable", Message: "套餐与额度服务暂时不可用"})
	}
}
