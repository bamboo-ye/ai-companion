package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"healthycare/internal/config"

	"github.com/gin-gonic/gin"
)

func TestPrivateAPIRequiresServiceCredentialAndCanonicalOwner(t *testing.T) {
	const token = "health-service-test-token-at-least-32-characters"
	const owner = "cbd1f5f9-fe81-48b0-abb5-6cba5d9ef377"
	a := App{Config: config.Config{CompanionToken: token}}
	router := gin.New()
	router.GET("/identity", a.authenticateCompanion, func(c *gin.Context) {
		c.String(200, actor(c).ID)
	})
	for _, tc := range []struct {
		token, user string
		status      int
	}{
		{"", owner, 401}, {"different-token", owner, 401}, {token, "", 401},
		{token, "../x", 401}, {token, "00000000-0000-0000-0000-000000000000", 401},
		{token, owner, 200},
	} {
		r := httptest.NewRequest(http.MethodGet, "/identity", nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		r.Header.Set("X-Companion-User-ID", tc.user)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		if rec.Code != tc.status || (tc.status == 200 && rec.Body.String() != owner) {
			t.Errorf("owner %q: status = %d, want %d", tc.user, rec.Code, tc.status)
		}
	}
}
