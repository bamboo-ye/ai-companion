package identity

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/id"
)

var (
	ErrConflict     = errors.New("resource already exists")
	ErrNotFound     = errors.New("resource not found")
	ErrUnauthorized = errors.New("invalid credentials")
	ErrValidation   = errors.New("validation failed")
)

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"display_name"`
	Timezone     string    `json:"timezone"`
	Locale       string    `json:"locale"`
	Status       string    `json:"status,omitempty"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Device struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	DeviceKey string    `json:"device_key"`
	Name      string    `json:"name"`
	Platform  string    `json:"platform"`
	Timezone  string    `json:"timezone"`
	LastSeen  time.Time `json:"last_seen_at"`
}

type Session struct {
	ID          string
	UserID      string
	DeviceID    string
	TokenHash   string
	ExpiresAt   time.Time
	CreatedAt   time.Time
	LastUsedAt  time.Time
	RevokedAt   *time.Time
	RotatedToID string
}

type Store interface {
	CreateUser(context.Context, User) error
	FindUserByEmail(context.Context, string) (User, error)
	GetUser(context.Context, string) (User, error)
	UpsertDevice(context.Context, Device) (Device, error)
	CreateSession(context.Context, Session) error
	GetSession(context.Context, string) (Session, error)
	FindSessionByTokenHash(context.Context, string) (Session, error)
	RotateSession(context.Context, string, Session, time.Time) error
	RevokeSession(context.Context, string, time.Time) error
	RevokeUserSessions(context.Context, string, time.Time) error
}

type DeviceInput struct {
	DeviceKey string `json:"device_key"`
	Name      string `json:"name"`
	Platform  string `json:"platform"`
}

type RegisterInput struct {
	Email       string      `json:"email"`
	Password    string      `json:"password"`
	DisplayName string      `json:"display_name"`
	Timezone    string      `json:"timezone"`
	Locale      string      `json:"locale"`
	Device      DeviceInput `json:"device"`
}

type LoginInput struct {
	Email    string      `json:"email"`
	Password string      `json:"password"`
	Timezone string      `json:"timezone"`
	Device   DeviceInput `json:"device"`
}

type TokenPair struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	TokenType        string    `json:"token_type"`
	AccessExpiresAt  time.Time `json:"access_expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	User             User      `json:"user"`
}

type Service struct {
	store      Store
	tokens     TokenManager
	refreshTTL time.Duration
	now        func() time.Time
}

func NewService(store Store, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{store: store, tokens: NewTokenManager(secret, accessTTL), refreshTTL: refreshTTL, now: time.Now}
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (TokenPair, error) {
	input.Email = normalizeEmail(input.Email)
	if err := validateEmailPasswordTimezone(input.Email, input.Password, input.Timezone); err != nil {
		return TokenPair{}, err
	}
	if strings.TrimSpace(input.DisplayName) == "" || len([]rune(input.DisplayName)) > 40 {
		return TokenPair{}, fmt.Errorf("%w: display_name must contain 1-40 characters", ErrValidation)
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		return TokenPair{}, fmt.Errorf("hash password: %w", err)
	}
	now := s.now().UTC()
	userID, err := id.New()
	if err != nil {
		return TokenPair{}, err
	}
	user := User{ID: userID, Email: input.Email, DisplayName: strings.TrimSpace(input.DisplayName), Timezone: input.Timezone, Locale: defaultString(input.Locale, "zh-CN"), Status: "active", PasswordHash: hash, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return TokenPair{}, err
	}
	return s.createSession(ctx, user, input.Device, input.Timezone)
}

func (s *Service) Login(ctx context.Context, input LoginInput) (TokenPair, error) {
	user, err := s.store.FindUserByEmail(ctx, normalizeEmail(input.Email))
	if err != nil || !verifyPassword(user.PasswordHash, input.Password) {
		return TokenPair{}, ErrUnauthorized
	}
	timezone := defaultString(input.Timezone, user.Timezone)
	if _, err := time.LoadLocation(timezone); err != nil {
		return TokenPair{}, fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	return s.createSession(ctx, user, input.Device, timezone)
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	now := s.now().UTC()
	current, err := s.store.FindSessionByTokenHash(ctx, HashRefreshToken(refreshToken))
	if err != nil || current.RevokedAt != nil || !now.Before(current.ExpiresAt) {
		return TokenPair{}, ErrUnauthorized
	}
	user, err := s.store.GetUser(ctx, current.UserID)
	if err != nil {
		return TokenPair{}, ErrUnauthorized
	}
	plain, hash, err := NewRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	newID, err := id.New()
	if err != nil {
		return TokenPair{}, err
	}
	next := Session{ID: newID, UserID: current.UserID, DeviceID: current.DeviceID, TokenHash: hash, ExpiresAt: now.Add(s.refreshTTL), CreatedAt: now, LastUsedAt: now}
	if err := s.store.RotateSession(ctx, current.ID, next, now); err != nil {
		return TokenPair{}, ErrUnauthorized
	}
	access, accessExpiry, err := s.tokens.IssueAccess(user.ID, next.ID, now)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, TokenType: "Bearer", AccessExpiresAt: accessExpiry, RefreshExpiresAt: next.ExpiresAt, User: user}, nil
}

func (s *Service) Authenticate(ctx context.Context, accessToken string) (User, AccessClaims, error) {
	claims, err := s.tokens.ParseAccess(accessToken, s.now().UTC())
	if err != nil {
		return User{}, AccessClaims{}, ErrUnauthorized
	}
	session, err := s.store.GetSession(ctx, claims.SessionID)
	if err != nil || session.UserID != claims.UserID || session.RevokedAt != nil {
		return User{}, AccessClaims{}, ErrUnauthorized
	}
	user, err := s.store.GetUser(ctx, claims.UserID)
	if err != nil {
		return User{}, AccessClaims{}, ErrUnauthorized
	}
	return user, claims, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	return s.store.RevokeSession(ctx, sessionID, s.now().UTC())
}

func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	return s.store.RevokeUserSessions(ctx, userID, s.now().UTC())
}

func (s *Service) createSession(ctx context.Context, user User, input DeviceInput, timezone string) (TokenPair, error) {
	if input.Platform != "" && input.Platform != "web" && input.Platform != "ios" && input.Platform != "android" && input.Platform != "service" {
		return TokenPair{}, fmt.Errorf("%w: invalid device platform", ErrValidation)
	}
	now := s.now().UTC()
	deviceID, err := id.New()
	if err != nil {
		return TokenPair{}, err
	}
	device, err := s.store.UpsertDevice(ctx, Device{ID: deviceID, UserID: user.ID, DeviceKey: defaultString(input.DeviceKey, deviceID), Name: defaultString(input.Name, "Web Browser"), Platform: defaultString(input.Platform, "web"), Timezone: timezone, LastSeen: now})
	if err != nil {
		return TokenPair{}, err
	}
	plain, hash, err := NewRefreshToken()
	if err != nil {
		return TokenPair{}, err
	}
	sessionID, err := id.New()
	if err != nil {
		return TokenPair{}, err
	}
	session := Session{ID: sessionID, UserID: user.ID, DeviceID: device.ID, TokenHash: hash, ExpiresAt: now.Add(s.refreshTTL), CreatedAt: now, LastUsedAt: now}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return TokenPair{}, err
	}
	access, accessExpiry, err := s.tokens.IssueAccess(user.ID, session.ID, now)
	if err != nil {
		return TokenPair{}, err
	}
	return TokenPair{AccessToken: access, RefreshToken: plain, TokenType: "Bearer", AccessExpiresAt: accessExpiry, RefreshExpiresAt: session.ExpiresAt, User: user}, nil
}

func validateEmailPasswordTimezone(email, password, timezone string) error {
	address, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(address.Address, email) {
		return fmt.Errorf("%w: invalid email", ErrValidation)
	}
	if len(password) < 8 || len(password) > 128 {
		return fmt.Errorf("%w: password must contain 8-128 characters", ErrValidation)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("%w: invalid timezone", ErrValidation)
	}
	return nil
}

func normalizeEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
