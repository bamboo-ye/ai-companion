package billing

import (
	"context"
	"encoding/json"
)

func cloneQuota(p QuotaPolicy) QuotaPolicy {
	data, _ := json.Marshal(p)
	var copy QuotaPolicy
	_ = json.Unmarshal(data, &copy)
	return copy
}

func (s *MemoryStore) GetQuotaPolicies(_ context.Context, userID string) (QuotaPolicies, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p := QuotaPolicies{Global: cloneQuota(s.quotaPolicies[quotaKey("")]), User: QuotaPolicy{UserID: userID}}
	if userID != "" {
		if user, ok := s.quotaPolicies[quotaKey(userID)]; ok {
			p.User = cloneQuota(user)
		}
	}
	return p, nil
}

func (s *MemoryStore) SaveQuotaPolicy(_ context.Context, p QuotaPolicy, expected int64) (QuotaPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.quotaPolicies[quotaKey(p.UserID)].Revision != expected {
		return QuotaPolicy{}, ErrConflict
	}
	s.quotaPolicies[quotaKey(p.UserID)] = cloneQuota(p)
	return cloneQuota(p), nil
}
