package app

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a *App) authenticateCompanion(c *gin.Context) {
	userID := c.GetHeader("X-Companion-User-ID")
	id, err := uuid.Parse(userID)
	if err != nil || id == uuid.Nil || id.String() != userID ||
		!equal(c.GetHeader("Authorization"), "Bearer "+a.Config.CompanionToken) || len(a.Config.CompanionToken) < 32 {
		fail(c, http.StatusUnauthorized, "unauthorized", "请先登录伴AI。")
		return
	}
	c.Set("actor", Actor{ID: userID})
	c.Next()
}

// Use the immutable Companion user ID as the owner. Never link by email or
// copy profile, password, health or consent data from the parent account.
func (a *App) connect(c *gin.Context) {
	id := actor(c).ID
	tx, err := a.DB.Begin(c)
	if err != nil {
		internalError(c, err)
		return
	}
	defer tx.Rollback(c)
	if _, err = tx.Exec(c, "INSERT INTO users(id) VALUES($1) ON CONFLICT(id) DO NOTHING", id); err != nil {
		internalError(c, err)
		return
	}
	zone := c.GetHeader("X-Companion-Timezone")
	if _, zoneErr := time.LoadLocation(zone); zone == "" || zone == "Local" || len(zone) > 100 || zoneErr != nil {
		zone = "Asia/Shanghai"
	}
	if _, err = tx.Exec(c, "INSERT INTO profiles(user_id,data) VALUES($1,jsonb_build_object('timezone',$2::text)) ON CONFLICT DO NOTHING", id, zone); err != nil {
		internalError(c, err)
		return
	}
	if _, err = tx.Exec(c, "INSERT INTO conversations(user_id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
		internalError(c, err)
		return
	}
	if err = tx.Commit(c); err != nil {
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// All interactive health writes for an account coordinate with record erasure.
func lockHealthWrites(ctx context.Context, tx pgx.Tx, user string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,671))", user)
	return err
}
