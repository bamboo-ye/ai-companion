package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidToken = errors.New("invalid token")

type AccessClaims struct {
	UserID    string `json:"sub"`
	SessionID string `json:"sid"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
}

type TokenManager struct {
	secret    []byte
	accessTTL time.Duration
}

func NewTokenManager(secret string, accessTTL time.Duration) TokenManager {
	return TokenManager{secret: []byte(secret), accessTTL: accessTTL}
}

func (m TokenManager) IssueAccess(userID, sessionID string, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(m.accessTTL)
	claims := AccessClaims{UserID: userID, SessionID: sessionID, IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal access claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := m.sign(encoded)
	return "v1." + encoded + "." + signature, expiresAt, nil
}

func (m TokenManager) ParseAccess(token string, now time.Time) (AccessClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "v1" || !hmac.Equal([]byte(parts[2]), []byte(m.sign(parts[1]))) {
		return AccessClaims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AccessClaims{}, ErrInvalidToken
	}
	var claims AccessClaims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.UserID == "" || claims.SessionID == "" || now.Unix() >= claims.ExpiresAt {
		return AccessClaims{}, ErrInvalidToken
	}
	return claims, nil
}

func NewRefreshToken() (plain, hash string, err error) {
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return "", "", fmt.Errorf("generate refresh token: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(value)
	return plain, HashRefreshToken(plain), nil
}

func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (m TokenManager) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
