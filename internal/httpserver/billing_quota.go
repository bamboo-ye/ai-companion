package httpserver

import (
	"net/http"

	"github.com/windcry1/ai-companion/internal/billing"
)

func (s *Server) billingQuotaUser(w http.ResponseWriter, r *http.Request) bool {
	if r.PathValue("user_id") == "" {
		return true
	}
	if _, ok := s.requireIdentityAdmin(w); !ok {
		return false
	}
	if _, err := s.identityAdmin.GetUser(r.Context(), r.PathValue("user_id")); err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", Message: "用户不存在"})
		return false
	}
	return true
}

func (s *Server) getOperatorQuotaPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "support") || !s.billingQuotaUser(w, r) {
		return
	}
	p, err := s.billing.QuotaPolicies(r.Context(), r.PathValue("user_id"))
	if err != nil {
		writeBillingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) setOperatorQuotaPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperatorRole(w, r, "admin") || !s.requireSensitiveOperatorMFA(w, r) || !s.billingQuotaUser(w, r) {
		return
	}
	var input struct {
		Limits   *billing.QuotaLimits `json:"limits"`
		Revision *int64               `json:"revision"`
		Reason   string               `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Limits == nil || input.Revision == nil {
		writeBillingError(w, billing.ErrValidation)
		return
	}
	p, err := s.billing.SetQuotaPolicy(r.Context(), billing.QuotaPolicy{UserID: r.PathValue("user_id"), Limits: *input.Limits, Actor: currentOperator(r).Actor, Reason: input.Reason}, *input.Revision)
	if err != nil {
		writeBillingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
