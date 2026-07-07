package reliability

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("dependency circuit is open")

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

type CircuitBreaker struct {
	mu          sync.Mutex
	threshold   int
	cooldown    time.Duration
	now         func() time.Time
	state       CircuitState
	failures    int
	openedUntil time.Time
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &CircuitBreaker{threshold: threshold, cooldown: cooldown, now: time.Now, state: CircuitClosed}
}

func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != CircuitOpen {
		return true
	}
	if !b.now().UTC().Before(b.openedUntil) {
		b.state = CircuitHalfOpen
		return true
	}
	return false
}

func (b *CircuitBreaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state, b.failures, b.openedUntil = CircuitClosed, 0, time.Time{}
}

func (b *CircuitBreaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= b.threshold || b.state == CircuitHalfOpen {
		b.state = CircuitOpen
		b.openedUntil = b.now().UTC().Add(b.cooldown)
	}
}

func (b *CircuitBreaker) State() CircuitState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == CircuitOpen && !b.now().UTC().Before(b.openedUntil) {
		return CircuitHalfOpen
	}
	return b.state
}
