package identity

import (
	"context"
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
	return s.users[id], nil
}

func (s *MemoryStore) GetUser(_ context.Context, userID string) (User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[userID]
	if !ok {
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
