package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/gin-gonic/gin"
	"strconv"
	"strings"
	"time"
)

func signRunAccess(key, run string, expires int64) string {
	payload := run + ":" + strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(payload))
	return strconv.FormatInt(expires, 10) + "." + hex.EncodeToString(mac.Sum(nil))
}
func verifyRunAccess(key, run, token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || expires < now.Unix() || expires > now.Add(3*time.Minute).Unix() {
		return false
	}
	return equal(signRunAccess(key, run, expires), token)
}
func (a *App) requireRunAccess(c *gin.Context) bool {
	if !verifyRunAccess(a.Config.RunSigningKey, c.Param("id"), c.GetHeader("X-Run-Access"), time.Now()) {
		fail(c, 403, "run_scope_rejected", "运行身份已失效或不匹配。")
		return false
	}
	return true
}
