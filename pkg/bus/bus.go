package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// Event names
const (
	GameFinishedEventChannel = "game.finished"
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
