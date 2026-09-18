// Package adminpasskey implements administrator WebAuthn ceremonies and opaque,
// revocable browser sessions independently of end-user authentication.
package adminpasskey

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/windcry1/ai-companion/internal/opsauth"
)

const (
	CeremonyTTL = 5 * time.Minute
	SessionTTL  = 8 * time.Hour
	IdleTTL     = 30 * time.Minute
	FreshTTL    = 5 * time.Minute
	InviteTTL   = 30 * time.Minute
)

type Service struct {
	Store    Store
	Accounts opsauth.Store
	WebAuthn *webauthn.WebAuthn
	Origin   string
	Secure   bool
	now      func() time.Time
}

func New(store Store, accounts opsauth.Store, origin string) (*Service, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("WEB_ORIGIN must be an exact origin for admin passkeys")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "localhost") {
		return nil, fmt.Errorf("admin passkeys require HTTPS or http://localhost")
	}
	origin = strings.TrimSuffix(origin, "/")
	w, err := webauthn.New(&webauthn.Config{
		RPID: u.Hostname(), RPDisplayName: "伴AI 管理后台", RPOrigins: []string{origin},
		AttestationPreference:  protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired},
		Timeouts:               webauthn.TimeoutsConfig{Login: webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL}, Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: CeremonyTTL}},
	})
	if err != nil {
		return nil, err
	}
	return &Service{Store: store, Accounts: accounts, WebAuthn: w, Origin: origin, Secure: u.Scheme == "https", now: func() time.Time { return time.Now().UTC() }}, nil
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func Key(kind, secret string) string {
	h := sha256.Sum256([]byte(secret))
	return kind + ":" + hex.EncodeToString(h[:])
}
func marshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func accountVersion(a opsauth.Account) string {
	return fmt.Sprintf("%s:%d", a.TokenHash, a.SessionVersion)
}
func permanent() time.Time { return time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC) }

type credentialRecord struct {
	RPID              string
	Credential        webauthn.Credential
	Created           time.Time
	EnrollmentVersion string
}
type ceremony struct {
	Data           webauthn.SessionData
	AccountVersion string
	ExpectedOwner  string
}
type invitation struct{ AccountVersion string }
type Session struct {
	Owner           string    `json:"owner"`
	CredentialKey   string    `json:"credential_key"`
	AccountVersion  string    `json:"account_version"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
	LastSeen        time.Time `json:"last_seen"`
	Expires         time.Time `json:"expires"`
}
type user struct {
	account     opsauth.Account
	handle      []byte
	credentials []webauthn.Credential
}

func (u user) WebAuthnID() []byte                         { return u.handle }
func (u user) WebAuthnName() string                       { return u.account.ID }
func (u user) WebAuthnDisplayName() string                { return u.account.DisplayName }
func (u user) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Service) loadUser(ctx context.Context, owner string) (user, error) {
	a, err := s.Accounts.GetOperator(ctx, owner)
	if err != nil || a.Status != "active" {
		return user{}, ErrDenied
	}
	h, err := s.Store.Get(ctx, Key("user", owner))
	if errors.Is(err, ErrDenied) {
		h = Record{Key: Key("user", owner), Owner: owner, Kind: "user", Data: make([]byte, 32), Expires: permanent()}
		if _, err = rand.Read(h.Data); err != nil {
			return user{}, err
		}
		// Store JSON because the SQL transport uses a text column.
		h.Data = marshal(h.Data)
		if err = s.Store.Insert(ctx, h); err != nil && !errors.Is(err, ErrConflict) {
			return user{}, err
		}
		h, err = s.Store.Get(ctx, h.Key)
	}
	if err != nil {
		return user{}, err
	}
	u := user{account: a}
	if err = json.Unmarshal(h.Data, &u.handle); err != nil {
		return user{}, err
	}
	records, err := s.Store.List(ctx, "credential", owner)
	if err != nil {
		return user{}, err
	}
	for _, r := range records {
		var c credentialRecord
		if err = json.Unmarshal(r.Data, &c); err != nil {
			return user{}, err
		}
		if c.RPID == s.WebAuthn.Config.RPID {
			u.credentials = append(u.credentials, c.Credential)
		}
	}
	return u, nil
}

// IssueInvite is called only after administrator authorization or by the
// database-authorized offline provisioning command. The secret is returned once.
func (s *Service) IssueInvite(ctx context.Context, owner string) (string, error) {
	u, err := s.loadUser(ctx, owner)
	if err != nil {
		return "", err
	}
	token, err := randomSecret()
	if err != nil {
		return "", err
	}
	err = s.Store.Insert(ctx, Record{Key: Key("invite", token), Owner: owner, Kind: "invite", Data: marshal(invitation{AccountVersion: accountVersion(u.account)}), Expires: s.now().Add(InviteTTL)})
	if err == nil {
		err = s.recordEvent(ctx, owner, "passkey.invitation.issued")
	}
	return token, err
}

func (s *Service) BeginRegistration(ctx context.Context, token string) (any, string, error) {
	invite, err := s.Store.Take(ctx, Key("invite", token))
	if err != nil || !invite.Expires.After(s.now()) {
		return nil, "", ErrDenied
	}
	var in invitation
	if json.Unmarshal(invite.Data, &in) != nil {
		return nil, "", ErrDenied
	}
	u, err := s.loadUser(ctx, invite.Owner)
	if err != nil {
		return nil, "", err
	}
	if accountVersion(u.account) != in.AccountVersion || len(u.credentials) >= 10 {
		return nil, "", ErrDenied
	}
	excluded := make([]protocol.CredentialDescriptor, 0, len(u.credentials))
	for _, c := range u.credentials {
		excluded = append(excluded, c.Descriptor())
	}
	options, data, err := s.WebAuthn.BeginRegistration(u, webauthn.WithExclusions(excluded))
	if err != nil {
		return nil, "", err
	}
	secret, err := s.saveCeremony(ctx, "register", u.account.ID, ceremony{Data: *data, AccountVersion: accountVersion(u.account)})
	return options, secret, err
}

func (s *Service) saveCeremony(ctx context.Context, kind, owner string, data ceremony) (string, error) {
	secret, err := randomSecret()
	if err != nil {
		return "", err
	}
	err = s.Store.Insert(ctx, Record{Key: Key(kind, secret), Owner: owner, Kind: kind, Data: marshal(data), Expires: s.now().Add(CeremonyTTL)})
	return secret, err
}
func (s *Service) takeCeremony(ctx context.Context, kind, secret string) (Record, ceremony, error) {
	r, err := s.Store.Take(ctx, Key(kind, secret))
	if err != nil || !r.Expires.After(s.now()) {
		return Record{}, ceremony{}, ErrDenied
	}
	var c ceremony
	if json.Unmarshal(r.Data, &c) != nil {
		return Record{}, ceremony{}, ErrDenied
	}
	return r, c, nil
}

func (s *Service) FinishRegistration(ctx context.Context, secret string, request *http.Request) (string, error) {
	r, data, err := s.takeCeremony(ctx, "register", secret)
	if err != nil {
		return "", err
	}
	u, err := s.loadUser(ctx, r.Owner)
	if err != nil {
		return "", err
	}
	if accountVersion(u.account) != data.AccountVersion {
		return "", ErrDenied
	}
	c, err := s.WebAuthn.FinishRegistration(u, data.Data, request)
	if err != nil || !c.Flags.UserVerified {
		return "", ErrDenied
	}
	key := Key("credential", string(c.ID))
	err = s.Store.Insert(ctx, Record{Key: key, Owner: u.account.ID, Kind: "credential", Data: marshal(credentialRecord{RPID: s.WebAuthn.Config.RPID, Credential: *c, Created: s.now(), EnrollmentVersion: accountVersion(u.account)}), Expires: permanent()})
	if err != nil {
		return "", err
	}
	if err = s.recordEvent(ctx, u.account.ID, "passkey.registered"); err != nil {
		return "", err
	}
	return s.newSession(ctx, u.account, key)
}

func (s *Service) BeginLogin(ctx context.Context, expectedOwner string) (any, string, error) {
	options, data, err := s.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, "", err
	}
	secret, err := s.saveCeremony(ctx, "login", expectedOwner, ceremony{Data: *data, ExpectedOwner: expectedOwner})
	return options, secret, err
}

func (s *Service) FinishLogin(ctx context.Context, secret string, request *http.Request) (string, error) {
	_, data, err := s.takeCeremony(ctx, "login", secret)
	if err != nil {
		return "", err
	}
	var owner user
	var stored Record
	var saved credentialRecord
	handler := func(rawID, handle []byte) (webauthn.User, error) {
		var err error
		stored, err = s.Store.Get(ctx, Key("credential", string(rawID)))
		if err != nil {
			return nil, ErrDenied
		}
		if json.Unmarshal(stored.Data, &saved) != nil || saved.RPID != s.WebAuthn.Config.RPID {
			return nil, ErrDenied
		}
		owner, err = s.loadUser(ctx, stored.Owner)
		if err != nil {
			return nil, ErrDenied
		}
		if !bytes.Equal(handle, owner.handle) || (data.ExpectedOwner != "" && data.ExpectedOwner != owner.account.ID) {
			return nil, ErrDenied
		}
		return owner, nil
	}
	c, err := s.WebAuthn.FinishDiscoverableLogin(handler, data.Data, request)
	if err != nil || !c.Flags.UserVerified || c.Authenticator.CloneWarning {
		return "", ErrDenied
	}
	saved.Credential = *c
	stored.Data = marshal(saved)
	if err = s.Store.Update(ctx, stored); err != nil {
		return "", err
	}
	return s.newSession(ctx, owner.account, stored.Key)
}

func (s *Service) newSession(ctx context.Context, account opsauth.Account, credentialKey string) (string, error) {
	token, err := randomSecret()
	if err != nil {
		return "", err
	}
	now := s.now()
	session := Session{Owner: account.ID, CredentialKey: credentialKey, AccountVersion: accountVersion(account), AuthenticatedAt: now, LastSeen: now, Expires: now.Add(SessionTTL)}
	if err = s.Accounts.RecordOperatorAuthenticated(ctx, account.ID, now); err != nil {
		return "", err
	}
	err = s.Store.Insert(ctx, Record{Key: Key("session", token), Owner: account.ID, Kind: "session", Data: marshal(session), Expires: session.Expires})
	if err == nil {
		if err = s.recordEvent(ctx, account.ID, "passkey.session.created"); err != nil {
			_, _ = s.Store.Take(ctx, Key("session", token))
		}
	}
	return token, err
}

func (s *Service) Authenticate(ctx context.Context, token string) (opsauth.Account, Session, error) {
	r, err := s.Store.Get(ctx, Key("session", token))
	if err != nil {
		return opsauth.Account{}, Session{}, ErrDenied
	}
	var session Session
	if json.Unmarshal(r.Data, &session) != nil {
		return opsauth.Account{}, Session{}, ErrDenied
	}
	now := s.now()
	if !r.Expires.After(now) || !session.LastSeen.Add(IdleTTL).After(now) {
		return opsauth.Account{}, Session{}, ErrDenied
	}
	a, err := s.Accounts.GetOperator(ctx, session.Owner)
	if err != nil || a.Status != "active" || accountVersion(a) != session.AccountVersion {
		return opsauth.Account{}, Session{}, ErrDenied
	}
	c, err := s.Store.Get(ctx, session.CredentialKey)
	if err != nil || c.Owner != a.ID {
		return opsauth.Account{}, Session{}, ErrDenied
	}
	var cred credentialRecord
	if json.Unmarshal(c.Data, &cred) != nil || cred.RPID != s.WebAuthn.Config.RPID {
		return opsauth.Account{}, Session{}, ErrDenied
	}
	if now.Sub(session.LastSeen) >= time.Minute {
		session.LastSeen = now
		r.Data = marshal(session)
		if err = s.Store.Update(ctx, r); err != nil {
			// Another dashboard request may have touched this session concurrently.
			// Re-read to distinguish that from revocation; never recreate a session.
			if errors.Is(err, ErrConflict) {
				return s.Authenticate(ctx, token)
			}
			return opsauth.Account{}, Session{}, ErrDenied
		}
	}
	return a, session, nil
}
func (s *Service) Fresh(session Session) bool {
	return session.AuthenticatedAt.Add(FreshTTL).After(s.now())
}
func (s *Service) Logout(ctx context.Context, token string) error {
	r, err := s.Store.Take(ctx, Key("session", token))
	if errors.Is(err, ErrDenied) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.recordEvent(ctx, r.Owner, "passkey.session.revoked")
}
func (s *Service) RevokeSessions(ctx context.Context, owner string) error {
	if err := s.Store.DeleteOwner(ctx, "session", owner); err != nil {
		return err
	}
	return s.recordEvent(ctx, owner, "passkey.sessions.revoked")
}

func (s *Service) recordEvent(ctx context.Context, owner, action string) error {
	id, err := randomSecret()
	if err != nil {
		return err
	}
	now := s.now()
	return s.Store.Insert(ctx, Record{Key: Key("audit", id), Owner: owner, Kind: "audit", Data: marshal(map[string]any{"action": action, "at": now, "rp_id": s.WebAuthn.Config.RPID}), Expires: now.Add(90 * 24 * time.Hour)})
}
func (s *Service) HasCredential(ctx context.Context, owner string) bool {
	u, err := s.loadUser(ctx, owner)
	return err == nil && len(u.credentials) > 0
}

// Limit unauthenticated ceremony allocation with a durable fixed-window bucket.
// The caller uses the actual transport peer, never an untrusted forwarded IP.
func (s *Service) AllowCeremony(ctx context.Context, peer string) error {
	now := s.now()
	key := Key("rate", peer+now.Format("200601021504"))
	if err := s.Store.Purge(ctx, now); err != nil {
		return err
	}
	for range 8 {
		r, err := s.Store.Get(ctx, key)
		if errors.Is(err, ErrDenied) {
			err = s.Store.Insert(ctx, Record{Key: key, Kind: "rate", Data: []byte("1"), Expires: now.Add(2 * time.Minute)})
			if errors.Is(err, ErrConflict) {
				continue
			}
			return err
		}
		if err != nil {
			return err
		}
		var n int
		if json.Unmarshal(r.Data, &n) != nil || n >= 120 {
			return ErrRateLimit
		}
		r.Data = marshal(n + 1)
		if err = s.Store.Update(ctx, r); errors.Is(err, ErrConflict) {
			continue
		}
		return err
	}
	return ErrRateLimit
}
