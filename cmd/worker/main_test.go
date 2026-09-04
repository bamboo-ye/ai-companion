package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

type notificationStoreStub struct {
	results []bool
	err     error
	calls   int
	now     time.Time
}

func (s *notificationStoreStub) EnqueueDueNotifications(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func (s *notificationStoreStub) DeliverNextNotification(_ context.Context, now time.Time) (bool, error) {
	s.calls++
	s.now = now
	if s.err != nil {
		return false, s.err
	}
	if len(s.results) == 0 {
		return false, nil
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func TestDeliverAvailableNotificationsDrainsReadyRows(t *testing.T) {
	now := time.Date(2026, time.September, 4, 10, 30, 0, 0, time.UTC)
	store := &notificationStoreStub{results: []bool{true, true, false}}

	delivered, err := deliverAvailableNotifications(context.Background(), store, now)
	if err != nil {
		t.Fatalf("deliver available notifications: %v", err)
	}
	if delivered != 2 || store.calls != 3 {
		t.Fatalf("expected two deliveries and one empty check, got delivered=%d calls=%d", delivered, store.calls)
	}
	if !store.now.Equal(now) {
		t.Fatalf("expected delivery timestamp %s, got %s", now, store.now)
	}
}

func TestDeliverAvailableNotificationsReturnsStoreError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	store := &notificationStoreStub{err: wantErr}

	delivered, err := deliverAvailableNotifications(context.Background(), store, time.Now())
	if delivered != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("expected the store error before delivery, got delivered=%d err=%v", delivered, err)
	}
}
