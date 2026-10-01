package arbitration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/windcry1/ai-companion/internal/semantic"
	"sync"
	"time"
)

type arbitrationModel interface {
	SummariesEnabled() bool
	SummaryIdentity() string
	JSONWithUsage(context.Context, string, any, int, any) (int64, error)
}

// The caller scopes storage to the original owner and source versions. Missing
// data returns nil,nil; storage failures must not silently start a fresh budget.
type DebatePersistence struct {
	Load func(context.Context, string) ([]byte, error)
	Save func(context.Context, string, []byte) error
}
type cachedModelCall struct {
	Status string          `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	Tokens int64           `json:"tokens"`
}
type debateCheckpoint struct {
	Deadline time.Time                  `json:"deadline"`
	Calls    map[string]cachedModelCall `json:"calls"`
	Result   *Record                    `json:"result,omitempty"`
}
type checkpointModel struct {
	client *semantic.Client
	store  DebatePersistence
	key    string
	state  debateCheckpoint
	mu     sync.Mutex
}

func persistedModel(ctx context.Context, client *semantic.Client, input Input, stores []DebatePersistence) (arbitrationModel, time.Time, error) {
	deadline := time.Now().Add(60 * time.Second)
	if len(stores) == 0 {
		return client, deadline, nil
	}
	store := stores[0]
	if store.Load == nil || store.Save == nil {
		return nil, deadline, fmt.Errorf("missing checkpoint adapter")
	}
	key := inputHash([]any{"wiki-debate-v1", Version, client.SummaryIdentity(), input})
	data, err := store.Load(ctx, key)
	if err != nil {
		return nil, deadline, err
	}
	state := debateCheckpoint{Deadline: deadline, Calls: map[string]cachedModelCall{}}
	if len(data) > 0 {
		if len(data) > 256000 || json.Unmarshal(data, &state) != nil || state.Deadline.IsZero() || len(state.Calls) > 6 || state.Calls == nil {
			return nil, deadline, fmt.Errorf("invalid debate checkpoint")
		}
		deadline = state.Deadline
	} else {
		data, _ = json.Marshal(state)
		if err = store.Save(ctx, key, data); err != nil {
			return nil, deadline, err
		}
	}
	return &checkpointModel{client: client, store: store, key: key, state: state}, deadline, nil
}
func (c *checkpointModel) SummariesEnabled() bool  { return c.client.SummariesEnabled() }
func (c *checkpointModel) SummaryIdentity() string { return c.client.SummaryIdentity() }
func (c *checkpointModel) finish(ctx context.Context, result Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Result = &result
	return c.save(ctx)
}
func (c *checkpointModel) usage() (int, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tokens int64
	for _, call := range c.state.Calls {
		tokens += call.Tokens
	}
	return len(c.state.Calls), tokens
}
func (c *checkpointModel) save(ctx context.Context) error {
	data, err := json.Marshal(c.state)
	if err != nil || len(data) > 256000 {
		return fmt.Errorf("checkpoint too large")
	}
	return c.store.Save(ctx, c.key, data)
}
func (c *checkpointModel) JSONWithUsage(ctx context.Context, prompt string, packet any, budget int, out any) (int64, error) {
	key := inputHash([]any{prompt, packet, budget})
	c.mu.Lock()
	if previous, ok := c.state.Calls[key]; ok {
		c.mu.Unlock()
		if previous.Status != "completed" {
			return previous.Tokens, fmt.Errorf("previous call failed or was interrupted")
		}
		return previous.Tokens, json.Unmarshal(previous.Output, out)
	}
	if len(c.state.Calls) >= 6 || time.Now().After(c.state.Deadline) {
		c.mu.Unlock()
		return 0, fmt.Errorf("persisted debate budget exhausted")
	}
	// Reserve before dispatch. A process crash cannot restart an uncertain call.
	c.state.Calls[key] = cachedModelCall{Status: "pending"}
	err := c.save(ctx)
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	var output json.RawMessage
	tokens, callErr := c.client.JSONWithUsage(ctx, prompt, packet, budget, &output)
	status := "completed"
	if callErr != nil {
		status = "failed"
		output = nil
	}
	c.mu.Lock()
	c.state.Calls[key] = cachedModelCall{Status: status, Output: output, Tokens: tokens}
	saveErr := c.save(ctx)
	c.mu.Unlock()
	if callErr != nil {
		return tokens, callErr
	}
	if saveErr != nil {
		return tokens, saveErr
	}
	return tokens, json.Unmarshal(output, out)
}
