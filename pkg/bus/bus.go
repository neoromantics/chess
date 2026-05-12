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
	EngineRequestChannel      = "engine.request"
	EngineResponseChannel     = "engine.response"
	EngineAbortChannel        = "engine.abort"
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

// Close closes the Redis client.
func (c *Client) Close() error {
	return c.rdb.Close()
}
