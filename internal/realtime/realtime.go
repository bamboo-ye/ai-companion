package realtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Gateway interface {
	Allow(context.Context, string, int, time.Duration) (bool, int, time.Duration, error)
	Touch(context.Context, string, string, time.Duration) error
	IsOnline(context.Context, string) (bool, error)
	Close() error
}

type RedisGateway struct{ client *redis.Client }

func OpenRedis(ctx context.Context, addr, password string) (*RedisGateway, error) {
	client := redis.NewClient(&redis.Options{Addr: addr, Password: password})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &RedisGateway{client: client}, nil
}
func (g *RedisGateway) Close() error { return g.client.Close() }

var rateScript = redis.NewScript(`local n=redis.call('INCR',KEYS[1]);if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end;local ttl=redis.call('PTTL',KEYS[1]);return {n,ttl}`)

func (g *RedisGateway) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	values, err := rateScript.Run(ctx, g.client, []string{"rate:" + key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, 0, err
	}
	count := int(values[0])
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	return count <= limit, remaining, time.Duration(values[1]) * time.Millisecond, nil
}
func (g *RedisGateway) Touch(ctx context.Context, userID, sessionID string, ttl time.Duration) error {
	return g.client.Set(ctx, "presence:user:"+userID+":"+sessionID, "online", ttl).Err()
}
func (g *RedisGateway) IsOnline(ctx context.Context, userID string) (bool, error) {
	iterator := g.client.Scan(ctx, 0, "presence:user:"+userID+":*", 1).Iterator()
	if iterator.Next(ctx) {
		return true, nil
	}
	return false, iterator.Err()
}

type MemoryGateway struct {
	mu       sync.Mutex
	rates    map[string]memoryRate
	presence map[string]time.Time
	now      func() time.Time
}
type memoryRate struct {
	count   int
	expires time.Time
}

func NewMemoryGateway() *MemoryGateway {
	return &MemoryGateway{rates: map[string]memoryRate{}, presence: map[string]time.Time{}, now: time.Now}
}
func (g *MemoryGateway) Close() error { return nil }
func (g *MemoryGateway) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, int, time.Duration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	item := g.rates[key]
	if !now.Before(item.expires) {
		item = memoryRate{expires: now.Add(window)}
	}
	item.count++
	g.rates[key] = item
	remaining := limit - item.count
	if remaining < 0 {
		remaining = 0
	}
	return item.count <= limit, remaining, time.Until(item.expires), nil
}
func (g *MemoryGateway) Touch(_ context.Context, userID, sessionID string, ttl time.Duration) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.presence[userID+":"+sessionID] = g.now().Add(ttl)
	return nil
}
func (g *MemoryGateway) IsOnline(_ context.Context, userID string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	for key, expiry := range g.presence {
		if !now.Before(expiry) {
			delete(g.presence, key)
			continue
		}
		if strings.HasPrefix(key, userID+":") {
			return true, nil
		}
	}
	return false, nil
}
