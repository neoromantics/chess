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

	// Channel-pattern prefixes used by the WS hub on every pod to fan out
	// game and per-user events to locally-connected clients. The hub
	// PSUBSCRIBE's the prefix once and demultiplexes by suffix.
	GameEventPrefix = "game.evt."
	UserEventPrefix = "user.evt."
	GameEventGlob   = GameEventPrefix + "*"
	UserEventGlob   = UserEventPrefix + "*"
)

// GameEventChannel returns the per-game pub/sub channel name.
// Every move/state update flows through this channel so any pod with
// WS clients for the game can fan out — solving the cross-pod-fanout
// problem the in-process Hub had.
func GameEventChannel(gameID string) string { return GameEventPrefix + gameID }

// UserEventChannel returns the per-user pub/sub channel name. Used for
// invites, match-found, and friend events that need to reach a specific
// user regardless of which pod their WS lives on.
func UserEventChannel(userID int64) string {
	return fmt.Sprintf("%s%d", UserEventPrefix, userID)
}

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

// SubscribePattern is the PSUBSCRIBE-flavoured Subscribe. The handler is
// invoked with both the resolved channel name (e.g. "game.evt.abc-123")
// and the payload, so callers can demultiplex by gameID/userID without
// re-parsing the prefix.
func (c *Client) SubscribePattern(ctx context.Context, pattern string, handler func(channel string, payload []byte)) error {
	pubsub := c.rdb.PSubscribe(ctx, pattern)

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
				handler(msg.Channel, []byte(msg.Payload))
			}
		}
	}()

	return nil
}

// Ping checks the Redis connection.
func (c *Client) Ping(ctx context.Context) error {
	return c.rdb.Ping(ctx).Err()
}

// SetSortedSet adds a member to a Redis sorted set with a score.
func (c *Client) SetSortedSet(ctx context.Context, key string, member string, score float64) error {
	return c.rdb.ZAdd(ctx, key, redis.Z{Score: score, Member: member}).Err()
}

// RemoveFromSortedSet removes one or more members from a Redis sorted set.
func (c *Client) RemoveFromSortedSet(ctx context.Context, key string, members ...any) error {
	return c.rdb.ZRem(ctx, key, members...).Err()
}

type ZMember struct {
	Member string
	Score  float64
}

// GetSortedSetRangeWithScores retrieves a range of members from a sorted set with their scores.
func (c *Client) GetSortedSetRangeWithScores(ctx context.Context, key string, start, stop int64) ([]ZMember, error) {
	zs, err := c.rdb.ZRangeWithScores(ctx, key, start, stop).Result()
	if err != nil {
		return nil, err
	}
	res := make([]ZMember, len(zs))
	for i, z := range zs {
		res[i] = ZMember{
			Member: z.Member.(string),
			Score:  z.Score,
		}
	}
	return res, nil
}

// GameClock represents the authoritative time remaining in a game.
type GameClock struct {
	WhiteMS        int64 `json:"white_ms"`
	BlackMS        int64 `json:"black_ms"`
	TurnStartedAt  int64 `json:"turn_started_at"` // Unix MS
}

// SetClock initializes the authoritative clock in Redis.
func (c *Client) SetClock(ctx context.Context, gameID string, clock GameClock) error {
	key := "gameclock:" + gameID
	data, _ := json.Marshal(clock)
	return c.rdb.Set(ctx, key, data, 24*time.Hour).Err()
}

// GetClock retrieves the authoritative clock from Redis.
func (c *Client) GetClock(ctx context.Context, gameID string) (*GameClock, error) {
	key := "gameclock:" + gameID
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var clock GameClock
	if err := json.Unmarshal([]byte(val), &clock); err != nil {
		return nil, err
	}
	return &clock, nil
}

// DelClock removes the clock from Redis.
func (c *Client) DelClock(ctx context.Context, gameID string) error {
	return c.rdb.Del(ctx, "gameclock:"+gameID).Err()
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

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// Rdb exposes the underlying *redis.Client for callers that need to use
// primitives the bus wrapper doesn't surface (currently pkg/leader for
// SET NX EX + script-based release). Use sparingly — most callers
// should stay on the typed Publish/Subscribe/LockGame surface.
func (c *Client) Rdb() *redis.Client { return c.rdb }

// DrawOffer represents an active draw offer.
type DrawOffer struct {
	OfferedBy core.Color `json:"offered_by"`
}

// SetDrawOffer stores a draw offer in Redis.
func (c *Client) SetDrawOffer(ctx context.Context, gameID string, side core.Color) error {
	key := "drawoffer:" + gameID
	data, _ := json.Marshal(DrawOffer{OfferedBy: side})
	return c.rdb.Set(ctx, key, data, 24*time.Hour).Err()
}

// GetDrawOffer retrieves a draw offer from Redis.
func (c *Client) GetDrawOffer(ctx context.Context, gameID string) (*DrawOffer, error) {
	key := "drawoffer:" + gameID
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var offer DrawOffer
	if err := json.Unmarshal([]byte(val), &offer); err != nil {
		return nil, err
	}
	return &offer, nil
}

// DelDrawOffer removes a draw offer from Redis.
func (c *Client) DelDrawOffer(ctx context.Context, gameID string) error {
	return c.rdb.Del(ctx, "drawoffer:"+gameID).Err()
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
