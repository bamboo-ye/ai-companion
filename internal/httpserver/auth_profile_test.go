package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestCurrentUserProfileAndPasswordChange(t *testing.T) {
	server := New(config.Config{
		HTTPAddr:        ":0",
		ServiceName:     "test",
		Environment:     "test",
		AuthTokenSecret: "profile-test-secret-with-enough-entropy",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	primary := registerProfileUser(t, server, "profile-primary", "old-password-123")
	otherLogin := performJSON(t, server, http.MethodPost, "/v1/auth/login", "", map[string]any{
		"email": "profile@example.com", "password": "old-password-123", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "profile-secondary", "name": "Secondary browser", "platform": "web"},
	})
	if otherLogin.Code != http.StatusOK {
		t.Fatalf("secondary login = %d %s", otherLogin.Code, otherLogin.Body.String())
	}
	var secondary struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(otherLogin.Body.Bytes(), &secondary); err != nil {
		t.Fatal(err)
	}

	profile := performJSON(t, server, http.MethodGet, "/v1/users/me", primary.AccessToken, nil)
	if profile.Code != http.StatusOK || !strings.Contains(profile.Body.String(), `"display_name":"小林"`) || strings.Contains(profile.Body.String(), "password") {
		t.Fatalf("profile = %d %s", profile.Code, profile.Body.String())
	}

	weak := performJSON(t, server, http.MethodPost, "/v1/users/me/password", primary.AccessToken, map[string]string{
		"current_password": "old-password-123", "new_password": "short",
	})
	if weak.Code != http.StatusUnprocessableEntity {
		t.Fatalf("weak password = %d %s", weak.Code, weak.Body.String())
	}
	wrong := performJSON(t, server, http.MethodPost, "/v1/users/me/password", primary.AccessToken, map[string]string{
		"current_password": "incorrect-password", "new_password": "new-password-456",
	})
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), `"code":"invalid_current_password"`) {
		t.Fatalf("wrong current password = %d %s", wrong.Code, wrong.Body.String())
	}

	changed := performJSON(t, server, http.MethodPost, "/v1/users/me/password", primary.AccessToken, map[string]string{
		"current_password": "old-password-123", "new_password": "new-password-456",
	})
	if changed.Code != http.StatusNoContent {
		t.Fatalf("change password = %d %s", changed.Code, changed.Body.String())
	}
	currentStillActive := performJSON(t, server, http.MethodGet, "/v1/users/me", primary.AccessToken, nil)
	if currentStillActive.Code != http.StatusOK {
		t.Fatalf("current session after password change = %d %s", currentStillActive.Code, currentStillActive.Body.String())
	}
	otherRevoked := performJSON(t, server, http.MethodGet, "/v1/users/me", secondary.AccessToken, nil)
	if otherRevoked.Code != http.StatusUnauthorized {
		t.Fatalf("other session after password change = %d %s", otherRevoked.Code, otherRevoked.Body.String())
	}

	oldLogin := performJSON(t, server, http.MethodPost, "/v1/auth/login", "", map[string]any{
		"email": "profile@example.com", "password": "old-password-123", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "profile-old-password", "name": "Old password", "platform": "web"},
	})
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login = %d %s", oldLogin.Code, oldLogin.Body.String())
	}
	newLogin := performJSON(t, server, http.MethodPost, "/v1/auth/login", "", map[string]any{
		"email": "profile@example.com", "password": "new-password-456", "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": "profile-new-password", "name": "New password", "platform": "web"},
	})
	if newLogin.Code != http.StatusOK {
		t.Fatalf("new password login = %d %s", newLogin.Code, newLogin.Body.String())
	}
}

type profileTokens struct {
	AccessToken string `json:"access_token"`
}

func registerProfileUser(t *testing.T, server *Server, deviceKey, password string) profileTokens {
	t.Helper()
	response := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": "profile@example.com", "password": password, "display_name": "小林", "timezone": "Asia/Shanghai", "locale": "zh-CN",
		"device": map[string]any{"device_key": deviceKey, "name": "Primary browser", "platform": "web"},
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("register = %d %s", response.Code, response.Body.String())
	}
	var tokens profileTokens
	if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	return tokens
}
