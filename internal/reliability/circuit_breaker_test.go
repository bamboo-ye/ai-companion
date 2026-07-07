package reliability

import (
	"testing"
	"time"
)

func TestCircuitBreakerOpensAndHalfOpensAfterCooldown(t *testing.T) {
	breaker := NewCircuitBreaker(2, time.Minute)
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	breaker.now = func() time.Time { return now }
	if !breaker.Allow() || breaker.State() != CircuitClosed {
		t.Fatalf("initial state = %s", breaker.State())
	}
	breaker.RecordFailure()
	if !breaker.Allow() || breaker.State() != CircuitClosed {
		t.Fatalf("after one failure = %s", breaker.State())
	}
	breaker.RecordFailure()
	if breaker.Allow() || breaker.State() != CircuitOpen {
		t.Fatalf("after threshold = allow:%v state:%s", breaker.Allow(), breaker.State())
	}
	now = now.Add(time.Minute)
	if !breaker.Allow() || breaker.State() != CircuitHalfOpen {
		t.Fatalf("after cooldown = allow:%v state:%s", breaker.Allow(), breaker.State())
	}
	breaker.RecordSuccess()
	if breaker.State() != CircuitClosed {
		t.Fatalf("after success = %s", breaker.State())
	}
}
