package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

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

// LockGame acquires a distributed lock for a specific game to prevent concurrent mutations.
// Returns true if the lock was acquired, false if it is currently held by another process.
func (c *Client) LockGame(ctx context.Context, gameID string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf("lock:game:%s", gameID)
	return c.rdb.SetNX(ctx, key, "locked", ttl).Result()
}

// UnlockGame releases the distributed lock for a specific game.
func (c *Client) UnlockGame(ctx context.Context, gameID string) error {
	key := fmt.Sprintf("lock:game:%s", gameID)
	return c.rdb.Del(ctx, key).Err()
}

// Enqueue adds a task to a Redis list (queue).
func (c *Client) Enqueue(ctx context.Context, queue string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}
	return c.rdb.RPush(ctx, queue, data).Err()
}

// Dequeue blocks until a task is available in the Redis list (queue).
func (c *Client) Dequeue(ctx context.Context, queue string) ([]byte, error) {
	// BLPop returns [key, value]
	res, err := c.rdb.BLPop(ctx, 0, queue).Result()
	if err != nil {
		return nil, err
	}
	if len(res) < 2 {
		return nil, fmt.Errorf("invalid blpop result")
	}
	return []byte(res[1]), nil
}
