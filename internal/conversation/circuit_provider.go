package conversation

import (
	"context"
	"errors"

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/reliability"
)

type CircuitBreakerProvider struct {
	Next    Provider
	Breaker *reliability.CircuitBreaker
}

func NewCircuitBreakerProvider(next Provider, breaker *reliability.CircuitBreaker) Provider {
	if next == nil || breaker == nil {
		return next
	}
	return CircuitBreakerProvider{Next: next, Breaker: breaker}
}

func (p CircuitBreakerProvider) Generate(ctx context.Context, persona character.Character, history []Message) (string, Usage, error) {
	if !p.Breaker.Allow() {
		return "", Usage{}, reliability.ErrCircuitOpen
	}
	text, usage, err := p.Next.Generate(ctx, persona, history)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.Breaker.RecordFailure()
		}
		return "", Usage{}, err
	}
	p.Breaker.RecordSuccess()
	return text, usage, nil
}
