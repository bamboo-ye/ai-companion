package identity

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

type MemoryStore struct {
	mu              sync.RWMutex
	users           map[string]User
	userIDByEmail   map[string]string
	devices         map[string]Device
	deviceIDByKey   map[string]string
	sessions        map[string]Session
	sessionIDByHash map[string]string
	auditLogs       []AuditLog
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{users: map[string]User{}, userIDByEmail: map[string]string{}, devices: map[string]Device{}, deviceIDByKey: map[string]string{}, sessions: map[string]Session{}, sessionIDByHash: map[string]string{}}
}

func (s *MemoryStore) CreateUser(_ context.Context, user User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.userIDByEmail[user.Email]; exists {
		return ErrConflict
	}
	s.users[user.ID] = user
	s.userIDByEmail[user.Email] = user.ID
	return nil
}

func (s *MemoryStore) FindUserByEmail(_ context.Context, email string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.userIDByEmail[email]
	if !ok {
		return User{}, ErrNotFound
	}
	user := s.users[id]
	if user.Status != "" && user.Status != "active" {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (s *MemoryStore) GetUser(_ context.Context, userID string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[userID]
	if !ok {
		return User{}, ErrNotFound
	}
	if user.Status != "" && user.Status != "active" {
		return User{}, ErrNotFound
	}
	return user, nil
}

func (s *MemoryStore) UpsertDevice(_ context.Context, device Device) (Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := device.UserID + ":" + device.DeviceKey
	if currentID, ok := s.deviceIDByKey[key]; ok {
		device.ID = currentID
	}
	s.devices[device.ID] = device
	s.deviceIDByKey[key] = device.ID
	return device, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	s.sessionIDByHash[session.TokenHash] = session.ID
	return nil
}

func (s *MemoryStore) GetSession(_ context.Context, id string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return session, nil
}

func (s *MemoryStore) FindSessionByTokenHash(_ context.Context, hash string) (Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.sessionIDByHash[hash]
	if !ok {
		return Session{}, ErrNotFound
	}
	return s.sessions[id], nil
}

func (s *MemoryStore) RotateSession(_ context.Context, currentID string, next Session, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[currentID]
	if !ok || current.RevokedAt != nil {
		return ErrUnauthorized
	}
	current.RevokedAt = &now
	current.RotatedToID = next.ID
	current.LastUsedAt = now
	s.sessions[currentID] = current
	s.sessions[next.ID] = next
	s.sessionIDByHash[next.TokenHash] = next.ID
	return nil
}

func (s *MemoryStore) RevokeSession(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if session.RevokedAt == nil {
		session.RevokedAt = &now
		s.sessions[id] = session
	}
	return nil
}

func (s *MemoryStore) RevokeUserSessions(_ context.Context, userID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, session := range s.sessions {
		if session.UserID == userID && session.RevokedAt == nil {
			session.RevokedAt = &now
			s.sessions[key] = session
		}
	}
	return nil
}

func (s *MemoryStore) ListUserAccounts(_ context.Context, filter UserAccountFilter) ([]User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	query := strings.ToLower(filter.Query)
	items := make([]User, 0)
	for _, user := range s.users {
		status := user.Status
		if status == "" {
			status = "active"
			user.Status = status
		}
		if filter.Status != "" && status != filter.Status {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(user.Email), query) && !strings.Contains(strings.ToLower(user.DisplayName), query) {
			continue
		}
		items = append(items, user)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func (s *MemoryStore) GetUserAccount(_ context.Context, userID string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[userID]
	if !ok {
		return User{}, ErrNotFound
	}
	if user.Status == "" {
		user.Status = "active"
	}
	return user, nil
}

func (s *MemoryStore) SetUserStatus(_ context.Context, input UserModerationInput) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[input.UserID]
	if !ok {
		return User{}, ErrNotFound
	}
	if user.Status == input.Status {
		return user, nil
	}
	user.Status, user.UpdatedAt = input.Status, input.Now
	s.users[input.UserID] = user
	if input.Status == "disabled" {
		for key, session := range s.sessions {
			if session.UserID == input.UserID && session.RevokedAt == nil {
				session.RevokedAt = &input.Now
				session.LastUsedAt = input.Now
				s.sessions[key] = session
			}
		}
	}
	metadata, _ := json.Marshal(map[string]string{"reason": input.Reason, "status": input.Status})
	s.auditLogs = append(s.auditLogs, AuditLog{
		ID: uint64(len(s.auditLogs) + 1), ActorType: "operator", ActorLabel: input.Actor, Action: "user.status.update",
		ResourceType: "user", ResourceID: input.UserID, Metadata: metadata, OccurredAt: input.Now,
	})
	return user, nil
}

func (s *MemoryStore) ListAuditLogs(_ context.Context, filter AuditLogFilter) ([]AuditLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AuditLog, 0)
	for _, record := range s.auditLogs {
		if filter.ResourceType != "" && record.ResourceType != filter.ResourceType {
			continue
		}
		if filter.ResourceID != "" && record.ResourceID != filter.ResourceID {
			continue
		}
		if filter.ActorType != "" && record.ActorType != filter.ActorType {
			continue
		}
		if filter.Action != "" && record.Action != filter.Action {
			continue
		}
		record.Metadata = append([]byte(nil), record.Metadata...)
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].OccurredAt.After(items[j].OccurredAt) })
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}
