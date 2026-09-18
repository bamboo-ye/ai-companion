package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/windcry1/ai-companion/internal/adminpasskey"
	"github.com/windcry1/ai-companion/internal/opsauth"
)

func (s *Server) adminCookieName(suffix string) string {
	name := "ai_admin" + suffix
	if s.adminPasskeys != nil && s.adminPasskeys.Secure {
		name = "__Host-" + name
	}
	return name
}
func (s *Server) adminCookie(r *http.Request, suffix string) string {
	c, err := r.Cookie(s.adminCookieName(suffix))
	if err != nil {
		return ""
	}
	return c.Value
}
func (s *Server) setAdminCookie(w http.ResponseWriter, suffix, value string, ttl time.Duration) {
	maxAge := int(ttl.Seconds())
	if value == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: s.adminCookieName(suffix), Value: value, Path: "/", HttpOnly: true, Secure: s.adminPasskeys != nil && s.adminPasskeys.Secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}

// Exact Origin plus a non-simple header protects both login and authenticated
// writes against CSRF. WEB_ORIGIN is trusted configuration, never a Host header.
func (s *Server) adminOriginOK(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.adminPasskeys == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "admin_passkey_unavailable", Message: "通行密钥服务未配置，请检查 WEB_ORIGIN 与数据库迁移"})
		return false
	}
	if r.Header.Get("Origin") != s.adminPasskeys.Origin || r.Header.Get("X-Admin-CSRF") != "1" {
		writeJSON(w, http.StatusForbidden, apiError{Code: "admin_origin_rejected", Message: "登录请求来源无效，请从配置的后台地址访问"})
		return false
	}
	return true
}
func (s *Server) adminCeremonyOK(w http.ResponseWriter, r *http.Request) bool {
	if !s.adminOriginOK(w, r) {
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if err = s.adminPasskeys.AllowCeremony(r.Context(), peer); err != nil {
		if errors.Is(err, adminpasskey.ErrRateLimit) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, apiError{Code: "admin_rate_limited", Message: "登录尝试过于频繁，请稍后重试"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "admin_store_unavailable", Message: "登录存储暂时不可用，请检查数据库迁移"})
		}
		return false
	}
	return true
}
func writePasskeyError(w http.ResponseWriter, err error) {
	if errors.Is(err, adminpasskey.ErrDenied) || errors.Is(err, adminpasskey.ErrConflict) {
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "admin_passkey_invalid", Message: "验证失败或已过期，请重新登录；邀请失效时请联系管理员重新签发"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "admin_passkey_error", Message: "通行密钥服务暂时不可用"})
}
func (s *Server) beginAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.adminCeremonyOK(w, r) {
		return
	}
	expected := ""
	if a, _, err := s.adminPasskeys.Authenticate(r.Context(), s.adminCookie(r, "")); err == nil {
		expected = a.ID
	}
	options, secret, err := s.adminPasskeys.BeginLogin(r.Context(), expected)
	if err != nil {
		writePasskeyError(w, err)
		return
	}
	s.setAdminCookie(w, "_login", secret, adminpasskey.CeremonyTTL)
	writeJSON(w, http.StatusOK, options)
}
func (s *Server) finishAdminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.adminOriginOK(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	token, err := s.adminPasskeys.FinishLogin(r.Context(), s.adminCookie(r, "_login"), r)
	s.setAdminCookie(w, "_login", "", 0)
	if err != nil {
		writePasskeyError(w, err)
		return
	}
	s.finishAdminSession(w, r, token)
}
func (s *Server) beginAdminRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.adminCeremonyOK(w, r) {
		return
	}
	var input struct {
		Invite string `json:"invite"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Invite == "" {
		a, session, err := s.adminPasskeys.Authenticate(r.Context(), s.adminCookie(r, ""))
		if err != nil {
			writePasskeyError(w, adminpasskey.ErrDenied)
			return
		}
		if !s.adminPasskeys.Fresh(session) {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "operator_reauth_required", Message: "请重新验证通行密钥后继续"})
			return
		}
		input.Invite, err = s.adminPasskeys.IssueInvite(r.Context(), a.ID)
		if err != nil {
			writePasskeyError(w, err)
			return
		}
	}
	options, secret, err := s.adminPasskeys.BeginRegistration(r.Context(), input.Invite)
	if err != nil {
		writePasskeyError(w, err)
		return
	}
	s.setAdminCookie(w, "_register", secret, adminpasskey.CeremonyTTL)
	writeJSON(w, http.StatusOK, options)
}
func (s *Server) finishAdminRegistration(w http.ResponseWriter, r *http.Request) {
	if !s.adminOriginOK(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	token, err := s.adminPasskeys.FinishRegistration(r.Context(), s.adminCookie(r, "_register"), r)
	s.setAdminCookie(w, "_register", "", 0)
	if err != nil {
		writePasskeyError(w, err)
		return
	}
	s.finishAdminSession(w, r, token)
}
func (s *Server) finishAdminSession(w http.ResponseWriter, r *http.Request, token string) {
	if err := s.adminPasskeys.Logout(r.Context(), s.adminCookie(r, "")); err != nil {
		_ = s.adminPasskeys.Logout(r.Context(), token)
		writePasskeyError(w, err)
		return
	}
	s.setAdminCookie(w, "", token, adminpasskey.SessionTTL)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}
func (s *Server) logoutAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.adminOriginOK(w, r) {
		return
	}
	if err := s.adminPasskeys.Logout(r.Context(), s.adminCookie(r, "")); err != nil {
		writePasskeyError(w, err)
		return
	}
	for _, suffix := range []string{"", "_login", "_register"} {
		s.setAdminCookie(w, suffix, "", 0)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) inviteAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.adminOriginOK(w, r) || !s.requireOperatorRole(w, r, "admin") {
		return
	}
	var input struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Role   string `json:"role"`
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.ID) > 128 || len(input.Name) > 128 {
		writeOperatorAccountError(w, opsauth.ErrValidation)
		return
	}
	result, err := s.operatorAuth.Create(r.Context(), opsauth.CreateInput{ID: input.ID, DisplayName: input.Name, Role: input.Role, Actor: currentOperator(r).Actor, Reason: input.Reason})
	if errors.Is(err, opsauth.ErrConflict) {
		account, lookupErr := s.operatorAuth.Get(r.Context(), input.ID)
		if lookupErr == nil && account.Status == "active" && account.Role == input.Role && account.DisplayName == input.Name && !s.adminPasskeys.HasCredential(r.Context(), account.ID) {
			// A cancelled/expired first enrollment can be reissued without creating
			// a second account. Existing enrolled accounts require offline recovery.
			reset, resetErr := s.operatorAuth.ResetToken(r.Context(), account.ID, currentOperator(r).Actor, input.Reason)
			if resetErr != nil {
				writeOperatorAccountError(w, resetErr)
				return
			}
			result.Account, err = reset.Account, nil
		}
	}
	if err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	token, err := s.adminPasskeys.IssueInvite(r.Context(), result.Account.ID)
	if err != nil {
		writePasskeyError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"invite": token, "expires_in": int(adminpasskey.InviteTTL.Seconds())})
}

func (s *Server) revokeAdminSessions(w http.ResponseWriter, r *http.Request) {
	if !s.adminOriginOK(w, r) {
		return
	}
	if _, err := s.operatorAuth.ResetToken(r.Context(), currentOperator(r).Actor, currentOperator(r).Actor, "退出此账号的所有后台会话"); err != nil {
		writeOperatorAccountError(w, err)
		return
	}
	if err := s.adminPasskeys.RevokeSessions(r.Context(), currentOperator(r).Actor); err != nil {
		writePasskeyError(w, err)
		return
	}
	s.setAdminCookie(w, "", "", 0)
	w.WriteHeader(http.StatusNoContent)
}

// Browser sessions are rechecked against the current account and credential on
// every request; ordinary user cookies cannot satisfy this middleware.
func (s *Server) authenticateAdminSession(w http.ResponseWriter, r *http.Request, next http.Handler) {
	w.Header().Set("Cache-Control", "no-store")
	if s.adminPasskeys == nil {
		writePasskeyError(w, adminpasskey.ErrDenied)
		return
	}
	a, session, err := s.adminPasskeys.Authenticate(r.Context(), s.adminCookie(r, ""))
	if err != nil {
		writePasskeyError(w, err)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !s.adminOriginOK(w, r) {
			return
		}
		if !s.adminPasskeys.Fresh(session) {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "operator_reauth_required", Message: "请重新验证通行密钥后继续"})
			return
		}
	}
	principal := operatorAuth{Actor: a.ID, Role: a.Role, MFA: true}
	next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, principal)))
}
