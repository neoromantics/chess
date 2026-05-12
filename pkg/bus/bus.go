package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// releaseLockScript implements compare-and-delete for distributed locks.
// A plain DEL would let a slow holder whose TTL expired blow away a
// successor's lock — this script guarantees we only release what we own.
var releaseLockScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
	return redis.call("del", KEYS[1])
else
	return 0
end
`)

// Event names
const (
	GameFinishedEventChannel = "game.finished"
	EngineRequestChannel     = "engine.request"
	EngineResponseChannel    = "engine.response"
	EngineAbortChannel       = "engine.abort"
	GameUpdatedChannel       = "game.updated"
)

// GameFinishedEvent represents the data sent when a game ends.
type GameFinishedEvent struct {
	GameID      string `json:"game_id"`
	Status      string `json:"status"`
	FEN         string `json:"fen"`
	EngineWhite bool   `json:"engine_white"`
	EngineBlack bool   `json:"engine_black"`
	UserID      int64  `json:"user_id,omitempty"`
}

// EngineRequest represents a request for the engine to calculate a move.
type EngineRequest struct {
	GameID   string            `json:"game_id"`
	FEN      string            `json:"fen"`
	History  map[uint64]int    `json:"history"`
	MoveTime time.Duration     `json:"movetime"`
	Context  string            `json:"context"` // "move", "hint", "assess"
	Metadata map[string]string `json:"metadata,omitempty"`
}

// EngineResponse represents the result of an engine calculation.
type EngineResponse struct {
	GameID   string            `json:"game_id"`
	BestMove string            `json:"best_move"` // UCI format
	Score    int               `json:"score"`
	Depth    int               `json:"depth"`
	Context  string            `json:"context"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// EngineAbort represents a request to cancel any active search for a game.
type EngineAbort struct {
	GameID string `json:"game_id"`
}

// GameUpdatedEvent represents an event fired when a game state is modified.
type GameUpdatedEvent struct {
	GameID string `json:"game_id"`
}

// Client wraps the Redis client for pub/sub.
type Client struct {
	rdb *redis.Client
}

// NewClient creates a new Redis bus client.
func NewClient(addr string) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
	})
	return &Client{rdb: rdb}
}

// Publish sends a JSON-encoded payload to the specified channel.
func (c *Client) Publish(ctx context.Context, channel string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	err = c.rdb.Publish(ctx, channel, data).Err()
	if err != nil {
		return fmt.Errorf("failed to publish to channel %s: %w", channel, err)
	}

	slog.Debug("published event", "channel", channel)
	return nil
}

// Subscribe listens for messages on a channel and calls the handler.
func (c *Client) Subscribe(ctx context.Context, channel string, handler func(payload []byte)) error {
	pubsub := c.rdb.Subscribe(ctx, channel)

	go func() {
		defer pubsub.Close()
		ch := pubsub.Channel()

		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-ch:
				if msg == nil {
					return
				}
				handler([]byte(msg.Payload))
			}
		}
	}()

	return nil
}

// SetState stores a transient value in Redis with a TTL.
func (c *Client) SetState(ctx context.Context, key string, value string, ttl time.Duration) error {
	return c.rdb.Set(ctx, key, value, ttl).Err()
}

// GetState retrieves a transient value from Redis.
func (c *Client) GetState(ctx context.Context, key string) (string, error) {
	return c.rdb.Get(ctx, key).Result()
}

// DelState removes a transient value from Redis.
func (c *Client) DelState(ctx context.Context, key string) error {
	return c.rdb.Del(ctx, key).Err()
}

// Ping checks the Redis connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// Enqueue pushes a JSON-encoded payload onto a Redis List (RPUSH).
// This guarantees exactly-once delivery: only one consumer will BLPOP the task.
func (c *Client) Enqueue(ctx context.Context, queue string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	err = c.rdb.RPush(ctx, queue, data).Err()
	if err != nil {
		return fmt.Errorf("failed to enqueue to %s: %w", queue, err)
	}

	slog.Debug("enqueued task", "queue", queue)
	return nil
}

// Dequeue blocks until a task is available on the Redis List (BLPOP).
// Returns the raw JSON payload. The timeout controls how long to wait;
// use 0 for an indefinite block. Returns nil payload on timeout or context cancellation.
func (c *Client) Dequeue(ctx context.Context, queue string, timeout time.Duration) ([]byte, error) {
	result, err := c.rdb.BLPop(ctx, timeout, queue).Result()
	if err != nil {
		return nil, err
	}
	// BLPOP returns [key, value]
	if len(result) < 2 {
		return nil, fmt.Errorf("unexpected BLPOP result")
	}
	return []byte(result[1]), nil
}

// GameLock represents an acquired distributed lock. The opaque token
// inside is used by Release to verify ownership before deleting the key.
type GameLock struct {
	client *Client
	key    string
	token  string
}

// Release returns the lock if (and only if) we still own it. Safe to call
// after TTL expiry — it will no-op rather than punt a successor's lock.
func (l *GameLock) Release(ctx context.Context) error {
	if l == nil || l.client == nil {
		return nil
	}
	return releaseLockScript.Run(ctx, l.client.rdb, []string{l.key}, l.token).Err()
}

// LockGame acquires a distributed lock for a specific game to prevent
// concurrent mutations. Returns a *GameLock on success; nil if the lock
// is held by another process.
func (c *Client) LockGame(ctx context.Context, gameID string, ttl time.Duration) (*GameLock, error) {
	token, err := newLockToken()
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("lock:game:%s", gameID)
	ok, err := c.rdb.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &GameLock{client: c, key: key, token: token}, nil
}

func newLockToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
