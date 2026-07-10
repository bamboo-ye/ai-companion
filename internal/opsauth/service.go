package opsauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUnauthorized = errors.New("operator unauthorized")
	ErrForbidden    = errors.New("operator forbidden")
	ErrValidation   = errors.New("operator auth validation failed")
)

type Account struct {
	ID                  string     `json:"id"`
	DisplayName         string     `json:"display_name"`
	Role                string     `json:"role"`
	Status              string     `json:"status"`
	TokenHash           string     `json:"-"`
	TOTPSecret          string     `json:"-"`
	MFAEnabled          bool       `json:"mfa_enabled"`
	CreatedAt           time.Time  `json:"created_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at,omitempty"`
	LastAuthenticatedAt *time.Time `json:"last_authenticated_at,omitempty"`
}

type Principal struct {
	ID          string
	Role        string
	MFAVerified bool
	Legacy      bool
}

type Store interface {
	FindOperatorByTokenHash(context.Context, string) (Account, error)
	RecordOperatorAuthenticated(context.Context, string, time.Time) error
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

func (s *Service) Authenticate(ctx context.Context, bearerToken, otp string) (Principal, error) {
	tokenHash := HashToken(bearerToken)
	if tokenHash == "" {
		return Principal{}, ErrUnauthorized
	}
	account, err := s.store.FindOperatorByTokenHash(ctx, tokenHash)
	if err != nil || account.Status != "active" {
		return Principal{}, ErrUnauthorized
	}
	role := normalizeRole(account.Role)
	if role == "" {
		return Principal{}, ErrUnauthorized
	}
	mfaVerified := !account.MFAEnabled
	if account.MFAEnabled {
		if !ValidateTOTP(account.TOTPSecret, otp, s.now().UTC()) {
			return Principal{}, ErrUnauthorized
		}
		mfaVerified = true
	}
	_ = s.store.RecordOperatorAuthenticated(ctx, account.ID, s.now().UTC())
	return Principal{ID: account.ID, Role: role, MFAVerified: mfaVerified}, nil
}

func Can(role, required string) bool {
	rank := map[string]int{"viewer": 1, "support": 2, "admin": 3}
	return rank[normalizeRole(role)] >= rank[normalizeRole(required)]
}

func HashToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func GenerateTOTP(secret string, now time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	counter := uint64(now.Unix() / 30)
	return hotp(key, counter), nil
}

func ValidateTOTP(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	key, err := decodeSecret(secret)
	if err != nil {
		return false
	}
	counter := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		candidate := hotp(key, uint64(counter+offset))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func BootstrapAccount(id, displayName, role, token, totpSecret string, mfaEnabled bool, now time.Time) (Account, error) {
	id, displayName, role = strings.TrimSpace(id), strings.TrimSpace(displayName), normalizeRole(role)
	if id == "" || displayName == "" || role == "" || HashToken(token) == "" {
		return Account{}, ErrValidation
	}
	if mfaEnabled {
		if _, err := decodeSecret(totpSecret); err != nil {
			return Account{}, fmt.Errorf("%w: invalid totp secret", ErrValidation)
		}
	}
	return Account{ID: id, DisplayName: displayName, Role: role, Status: "active", TokenHash: HashToken(token), TOTPSecret: strings.TrimSpace(totpSecret), MFAEnabled: mfaEnabled, CreatedAt: now, UpdatedAt: now}, nil
}

func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		return "viewer"
	}
	switch role {
	case "viewer", "support", "admin":
		return role
	default:
		return ""
	}
}

func decodeSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	if secret == "" {
		return nil, ErrValidation
	}
	padding := len(secret) % 8
	if padding != 0 {
		secret += strings.Repeat("=", 8-padding)
	}
	return base32.StdEncoding.DecodeString(secret)
}

func hotp(key []byte, counter uint64) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	binCode := (uint32(sum[offset])&0x7f)<<24 | (uint32(sum[offset+1])&0xff)<<16 | (uint32(sum[offset+2])&0xff)<<8 | (uint32(sum[offset+3]) & 0xff)
	value := binCode % uint32(math.Pow10(6))
	return leftPad6(strconv.Itoa(int(value)))
}

func leftPad6(value string) string {
	if len(value) >= 6 {
		return value
	}
	return strings.Repeat("0", 6-len(value)) + value
}
