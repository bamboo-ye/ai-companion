package httpserver

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/opsauth"
)

type authContextKey struct{}
type operatorContextKey struct{}

type authenticated struct {
	User   identity.User
	Claims identity.AccessClaims
}

type operatorAuth struct {
	Actor  string
	Role   string
	MFA    bool
	Legacy bool
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var input identity.RegisterInput
	if !decodeJSON(w, r, &input) {
		return
	}
	pair, err := s.identity.Register(r.Context(), input)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pair)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input identity.LoginInput
	if !decodeJSON(w, r, &input) {
		return
	}
	pair, err := s.identity.Login(r.Context(), input)
	if err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			writeJSON(w, http.StatusUnauthorized, apiError{
				Code:    "invalid_credentials",
				Message: "邮箱或密码不正确；如果尚未注册，请先创建账户",
			})
			return
		}
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	pair, err := s.identity.Refresh(r.Context(), input.RefreshToken)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pair)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.identity.Logout(r.Context(), currentAuth(r).Claims.SessionID); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logoutAll(w http.ResponseWriter, r *http.Request) {
	if err := s.identity.LogoutAll(r.Context(), currentAuth(r).User.ID); err != nil {
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getCurrentUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentAuth(r).User)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input identity.ChangePasswordInput
	if !decodeJSON(w, r, &input) {
		return
	}
	current := currentAuth(r)
	if err := s.identity.ChangePassword(r.Context(), current.User.ID, current.Claims.SessionID, input); err != nil {
		if errors.Is(err, identity.ErrUnauthorized) {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "invalid_current_password", Message: "当前密码不正确"})
			return
		}
		writeDomainError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeDomainError(w, identity.ErrUnauthorized)
			return
		}
		user, claims, err := s.identity.Authenticate(r.Context(), parts[1])
		if err != nil {
			writeDomainError(w, err)
			return
		}
		_ = s.realtime.Touch(r.Context(), user.ID, claims.SessionID, s.presenceTTL)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authContextKey{}, authenticated{User: user, Claims: claims})))
	})
}

func (s *Server) requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "运维认证无效"})
			return
		}
		if s.operatorAuth != nil {
			principal, err := s.operatorAuth.Authenticate(r.Context(), parts[1], r.Header.Get("X-Operator-TOTP"))
			if err == nil {
				if s.operatorMFARequired && !principal.MFAVerified {
					writeJSON(w, http.StatusUnauthorized, apiError{Code: "operator_mfa_required", Message: "运维账号必须使用 MFA 认证"})
					return
				}
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, operatorAuth{Actor: principal.ID, Role: principal.Role, MFA: principal.MFAVerified, Legacy: principal.Legacy})))
				return
			}
		}
		if s.operatorMFARequired {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "operator_mfa_required", Message: "运维账号必须使用 MFA 认证"})
			return
		}
		expected := strings.TrimSpace(s.operatorToken)
		if expected == "" {
			writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "operator_auth_unconfigured", Message: "运维认证未配置"})
			return
		}
		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(expected)) != 1 {
			writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "运维认证无效"})
			return
		}
		actor := strings.TrimSpace(r.Header.Get("X-Operator-ID"))
		if actor == "" {
			actor = "operator"
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, operatorAuth{Actor: actor, Role: "admin", Legacy: true})))
	})
}

func (s *Server) requireOperatorRole(w http.ResponseWriter, r *http.Request, role string) bool {
	operator := currentOperator(r)
	if operator.Role == "" {
		operator.Role = "viewer"
	}
	if !opsauth.Can(operator.Role, role) {
		writeJSON(w, http.StatusForbidden, apiError{Code: "operator_forbidden", Message: "当前运维角色无权执行该操作"})
		return false
	}
	return true
}

func (s *Server) presenceHeartbeat(w http.ResponseWriter, r *http.Request) {
	auth := currentAuth(r)
	if err := s.realtime.Touch(r.Context(), auth.User.ID, auth.Claims.SessionID, s.presenceTTL); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "presence_unavailable", Message: "在线状态服务暂时不可用"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getMyPresence(w http.ResponseWriter, r *http.Request) {
	online, err := s.realtime.IsOnline(r.Context(), currentAuth(r).User.ID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Code: "presence_unavailable", Message: "在线状态服务暂时不可用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"online": online})
}

func currentAuth(r *http.Request) authenticated {
	value, _ := r.Context().Value(authContextKey{}).(authenticated)
	return value
}

func currentOperator(r *http.Request) operatorAuth {
	value, _ := r.Context().Value(operatorContextKey{}).(operatorAuth)
	return value
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "请求 JSON 无效"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, apiError{Code: "invalid_request", Message: "请求只能包含一个 JSON 对象"})
		return false
	}
	return true
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthorized):
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "unauthorized", Message: "登录状态无效或已过期"})
	case errors.Is(err, identity.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Code: "conflict", Message: "该邮箱已注册"})
	case errors.Is(err, identity.ErrValidation):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: "validation_error", Message: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal_error", Message: "服务暂时不可用"})
	}
}
