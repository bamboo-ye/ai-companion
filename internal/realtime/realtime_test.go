package realtime

import (
	"context"
	"testing"
	"time"
)

func TestMemoryRateLimitAndPresence(t *testing.T) {
	gateway := NewMemoryGateway()
	allowed, remaining, _, err := gateway.Allow(context.Background(), "user", 1, time.Minute)
	if err != nil || !allowed || remaining != 0 {
		t.Fatalf("first allow = %v %d %v", allowed, remaining, err)
	}
	allowed, _, _, _ = gateway.Allow(context.Background(), "user", 1, time.Minute)
	if allowed {
		t.Fatal("second request should be limited")
	}
	if err = gateway.Touch(context.Background(), "u1", "s1", time.Minute); err != nil {
		t.Fatal(err)
	}
	online, err := gateway.IsOnline(context.Background(), "u1")
	if err != nil || !online {
		t.Fatalf("online = %v, err = %v", online, err)
	}
}
