package conversation

import (
	"context"
	"errors"

	"github.com/windcry1/ai-companion/internal/character"
	"github.com/windcry1/ai-companion/internal/contextengine"
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
		if !errors.Is(err, context.Canceled) && !errors.Is(err, contextengine.ErrBudgetExceeded) {
			p.Breaker.RecordFailure()
		}
		return "", Usage{}, err
	}
	p.Breaker.RecordSuccess()
	return text, usage, nil
}

func (p CircuitBreakerProvider) GenerateWithTools(ctx context.Context, persona character.Character, history []Message, tools []ModelToolDefinition) (ModelToolTurn, Usage, error) {
	next, ok := p.Next.(ToolCallingProvider)
	if !ok {
		return ModelToolTurn{}, Usage{}, errors.New("model provider does not support tool calling")
	}
	if !p.Breaker.Allow() {
		return ModelToolTurn{}, Usage{}, reliability.ErrCircuitOpen
	}
	turn, usage, err := next.GenerateWithTools(ctx, persona, history, tools)
	if err != nil {
		if !errors.Is(err, context.Canceled) && !errors.Is(err, contextengine.ErrBudgetExceeded) {
			p.Breaker.RecordFailure()
		}
		return ModelToolTurn{}, Usage{}, err
	}
	p.Breaker.RecordSuccess()
	return turn, usage, nil
}
