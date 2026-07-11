package opsauth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
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
	ErrAdminLockout = fmt.Errorf("%w: admin lockout protection", ErrForbidden)
	ErrValidation   = errors.New("operator auth validation failed")
	ErrNotFound     = errors.New("operator account not found")
	ErrConflict     = errors.New("operator account already exists")
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
	ListOperators(context.Context, OperatorFilter) ([]Account, error)
	GetOperator(context.Context, string) (Account, error)
	CreateOperator(context.Context, Account, AuditInput) (Account, error)
	SetOperatorStatus(context.Context, string, string, AuditInput) (Account, error)
	ResetOperatorToken(context.Context, string, string, AuditInput) (Account, error)
	ResetOperatorMFA(context.Context, string, string, bool, AuditInput) (Account, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

type OperatorFilter struct {
	Role   string
	Status string
	Limit  int
}

type AuditInput struct {
	Actor  string
	Action string
	Reason string
	Now    time.Time
}

type CreateInput struct {
	ID          string
	DisplayName string
	Role        string
	Token       string
	TOTPSecret  string
	MFAEnabled  bool
	Actor       string
	Reason      string
}

type CreateResult struct {
	Account    Account `json:"operator"`
	Token      string  `json:"token,omitempty"`
	TOTPSecret string  `json:"totp_secret,omitempty"`
}

type ResetTokenResult struct {
	Account Account `json:"operator"`
	Token   string  `json:"token"`
}

type ResetMFAResult struct {
	Account    Account `json:"operator"`
	TOTPSecret string  `json:"totp_secret,omitempty"`
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
	mfaVerified := false
	if account.MFAEnabled {
		if !ValidateTOTP(account.TOTPSecret, otp, s.now().UTC()) {
			return Principal{}, ErrUnauthorized
		}
		mfaVerified = true
	}
	_ = s.store.RecordOperatorAuthenticated(ctx, account.ID, s.now().UTC())
	return Principal{ID: account.ID, Role: role, MFAVerified: mfaVerified}, nil
}

func (s *Service) List(ctx context.Context, filter OperatorFilter) ([]Account, error) {
	filter.Role = strings.TrimSpace(filter.Role)
	if filter.Role != "" {
		filter.Role = normalizeRole(filter.Role)
		if filter.Role == "" {
			return nil, ErrValidation
		}
	}
	filter.Status = normalizeStatus(filter.Status)
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Status == "" {
		return nil, ErrValidation
	}
	return s.store.ListOperators(ctx, filter)
}

func (s *Service) Get(ctx context.Context, id string) (Account, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Account{}, ErrValidation
	}
	return s.store.GetOperator(ctx, id)
}

func (s *Service) Create(ctx context.Context, input CreateInput) (CreateResult, error) {
	input.ID, input.DisplayName = strings.TrimSpace(input.ID), strings.TrimSpace(input.DisplayName)
	input.Role = normalizeRole(input.Role)
	input.Actor, input.Reason = strings.TrimSpace(input.Actor), strings.TrimSpace(input.Reason)
	if input.ID == "" || input.DisplayName == "" || input.Role == "" || input.Actor == "" || input.Reason == "" || len(input.Reason) > 512 {
		return CreateResult{}, ErrValidation
	}
	token := strings.TrimSpace(input.Token)
	if token == "" {
		generated, err := RandomToken()
		if err != nil {
			return CreateResult{}, err
		}
		token = generated
	}
	secret := strings.TrimSpace(input.TOTPSecret)
	if input.MFAEnabled && secret == "" {
		generated, err := RandomTOTPSecret()
		if err != nil {
			return CreateResult{}, err
		}
		secret = generated
	}
	now := s.now().UTC()
	account, err := BootstrapAccount(input.ID, input.DisplayName, input.Role, token, secret, input.MFAEnabled, now)
	if err != nil {
		return CreateResult{}, err
	}
	account, err = s.store.CreateOperator(ctx, account, AuditInput{Actor: input.Actor, Action: "operator.create", Reason: input.Reason, Now: now})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Account: account, Token: token, TOTPSecret: secret}, nil
}

func (s *Service) Disable(ctx context.Context, id, actor, reason string) (Account, error) {
	return s.setStatus(ctx, id, "disabled", actor, reason)
}

func (s *Service) Enable(ctx context.Context, id, actor, reason string) (Account, error) {
	return s.setStatus(ctx, id, "active", actor, reason)
}

func (s *Service) ResetToken(ctx context.Context, id, actor, reason string) (ResetTokenResult, error) {
	id, actor, reason = strings.TrimSpace(id), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if id == "" || actor == "" || reason == "" || len(reason) > 512 {
		return ResetTokenResult{}, ErrValidation
	}
	token, err := RandomToken()
	if err != nil {
		return ResetTokenResult{}, err
	}
	account, err := s.store.ResetOperatorToken(ctx, id, HashToken(token), AuditInput{Actor: actor, Action: "operator.token.reset", Reason: reason, Now: s.now().UTC()})
	if err != nil {
		return ResetTokenResult{}, err
	}
	return ResetTokenResult{Account: account, Token: token}, nil
}

func (s *Service) ResetMFA(ctx context.Context, id, actor, reason string, enabled bool) (ResetMFAResult, error) {
	id, actor, reason = strings.TrimSpace(id), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if id == "" || actor == "" || reason == "" || len(reason) > 512 {
		return ResetMFAResult{}, ErrValidation
	}
	if !enabled {
		if err := s.preventAdminLockout(ctx, id, false, true); err != nil {
			return ResetMFAResult{}, err
		}
	}
	secret := ""
	if enabled {
		generated, err := RandomTOTPSecret()
		if err != nil {
			return ResetMFAResult{}, err
		}
		secret = generated
	}
	account, err := s.store.ResetOperatorMFA(ctx, id, secret, enabled, AuditInput{Actor: actor, Action: "operator.mfa.reset", Reason: reason, Now: s.now().UTC()})
	if err != nil {
		return ResetMFAResult{}, err
	}
	return ResetMFAResult{Account: account, TOTPSecret: secret}, nil
}

func Can(role, required string) bool {
	rank := map[string]int{"viewer": 1, "support": 2, "admin": 3}
	return rank[normalizeRole(role)] >= rank[normalizeRole(required)]
}

func RandomToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return "op_" + hex.EncodeToString(data[:]), nil
}

func RandomTOTPSecret() (string, error) {
	var data [20]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(data[:]), "="), nil
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
		return ""
	}
	switch role {
	case "viewer", "support", "admin":
		return role
	default:
		return ""
	}
}

func normalizeStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		return "active"
	}
	switch status {
	case "active", "disabled":
		return status
	default:
		return ""
	}
}

func (s *Service) setStatus(ctx context.Context, id, status, actor, reason string) (Account, error) {
	id, actor, reason = strings.TrimSpace(id), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if id == "" || actor == "" || reason == "" || len(reason) > 512 {
		return Account{}, ErrValidation
	}
	if status == "disabled" {
		if err := s.preventAdminLockout(ctx, id, true, false); err != nil {
			return Account{}, err
		}
	}
	return s.store.SetOperatorStatus(ctx, id, status, AuditInput{Actor: actor, Action: "operator.status.update", Reason: reason, Now: s.now().UTC()})
}

func (s *Service) preventAdminLockout(ctx context.Context, targetID string, disablingAccount, disablingMFA bool) error {
	account, err := s.store.GetOperator(ctx, targetID)
	if err != nil {
		return err
	}
	if account.Status != "active" || normalizeRole(account.Role) != "admin" {
		return nil
	}
	activeAdmins, err := s.store.ListOperators(ctx, OperatorFilter{Role: "admin", Status: "active", Limit: 500})
	if err != nil {
		return err
	}
	if disablingAccount && len(activeAdmins) <= 1 {
		return ErrAdminLockout
	}
	if account.MFAEnabled && (disablingAccount || disablingMFA) {
		activeMFAAdmins := 0
		for _, admin := range activeAdmins {
			if admin.MFAEnabled {
				activeMFAAdmins++
			}
		}
		if activeMFAAdmins <= 1 {
			return ErrAdminLockout
		}
	}
	return nil
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
