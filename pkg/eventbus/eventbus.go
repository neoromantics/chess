package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Stream/Channel names
const (
	StreamGameCommands   = "game:commands"
	StreamGameEvents     = "game:events"
	StreamEngineRequests = "engine:requests"
	ChannelEngineResults = "engine:results"
	ChannelUserEvents    = "user:events:" // Prefix: user:events:{userID}
)

// EngineRequest represents a request for the engine to calculate a move.
type EngineRequest struct {
	GameID   string         `json:"game_id"`
	FEN      string         `json:"fen"`
	History  map[uint64]int `json:"history"`
	MoveTime time.Duration  `json:"movetime"`
	Context  string         `json:"context"` // "move", "hint", "assess"
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


// Command types
const (
	CmdMakeMove    = "MakeMove"
	CmdResign      = "Resign"
	CmdOfferDraw   = "OfferDraw"
	CmdAcceptDraw  = "AcceptDraw"
	CmdDeclineDraw = "DeclineDraw"
	CmdHint        = "Hint"
	CmdAssess      = "Assess"
	CmdNewGame     = "NewGame"
)

// Command payloads
type MakeMoveCmd struct {
	Move string `json:"move"` // UCI format
}

type ResignCmd struct{}

type OfferDrawCmd struct{}

type AcceptDrawCmd struct{}

type DeclineDrawCmd struct{}

// Event types
const (
	EvtMovePlayed   = "MovePlayed"
	EvtGameFinished = "GameFinished"
	EvtMatchFound   = "MatchFound"
	EvtInviteSent   = "InviteSent"
)

// Event payloads
type MovePlayedEvt struct {
	Move       string   `json:"move"` // UCI
	SAN        string   `json:"san"`
	FEN        string   `json:"fen"`
	History    []string `json:"history"`
	HistorySAN []string `json:"history_san"`
	WhiteTime  int64    `json:"white_time"`
	BlackTime  int64    `json:"black_time"`
}

type GameFinishedEvt struct {
	Status string `json:"status"`
	Result string `json:"result"`
}

// Command is an intent to change state. Dispatched by Gateway, consumed by Game service.
type Command struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	GameID    string          `json:"game_id,omitempty"`
	UserID    int64           `json:"user_id"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// Event is a fact that has happened. Emitted by services, consumed by Gateway and others.
type Event struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	GameID    string          `json:"game_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}


// Client wraps the Redis client for Command/Event sourcing.
type Client struct {
	rdb *redis.Client
}

func NewClient(addr string) *Client {
	return &Client{
		rdb: redis.NewClient(&redis.Options{
			Addr: addr,
		}),
	}
}

// SendCommand appends a Command to the game:commands stream.
func (c *Client) SendCommand(ctx context.Context, cmd Command) (string, error) {
	if cmd.Timestamp.IsZero() {
		cmd.Timestamp = time.Now()
	}
	data, err := json.Marshal(cmd)
	if err != nil {
		return "", err
	}

	return c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamGameCommands,
		Values: map[string]interface{}{"data": data},
	}).Result()
}

// EmitEvent appends an Event to the game:events stream AND publishes for realtime.
func (c *Client) EmitEvent(ctx context.Context, evt Event) (string, error) {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return "", err
	}

	// 1. Append to durable stream
	id, err := c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamGameEvents,
		Values: map[string]interface{}{"data": data},
	}).Result()
	if err != nil {
		return "", err
	}

	// 2. Publish to Pub/Sub for immediate WebSocket broadcast
	channel := "game.evt." + evt.GameID
	if err := c.rdb.Publish(ctx, channel, data).Err(); err != nil {
		slog.Warn("failed to publish event for realtime", "channel", channel, "error", err)
	}

	return id, nil
}

// PublishUserEvent sends a targeted event to a specific user's channel.
func (c *Client) PublishUserEvent(ctx context.Context, userID int64, evt Event) error {
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("user.evt.%d", userID)
	return c.rdb.Publish(ctx, channel, data).Err()
}

// ReadCommands blocks until a command is available in the game:commands stream.
func (c *Client) ReadCommands(ctx context.Context, group, consumer string, timeout time.Duration) ([]redis.XMessage, error) {
	return c.readStream(ctx, StreamGameCommands, group, consumer, timeout)
}

// ReadEvents blocks until an event is available in the game:events stream.
func (c *Client) ReadEvents(ctx context.Context, group, consumer string, timeout time.Duration) ([]redis.XMessage, error) {
	return c.readStream(ctx, StreamGameEvents, group, consumer, timeout)
}

// SendEngineRequest appends a calculation task to the engine:requests stream.
func (c *Client) SendEngineRequest(ctx context.Context, req EngineRequest) (string, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	return c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamEngineRequests,
		Values: map[string]interface{}{"data": data},
	}).Result()
}

// ReadEngineRequests blocks until a task is available in the engine:requests stream.
func (c *Client) ReadEngineRequests(ctx context.Context, group, consumer string, timeout time.Duration) ([]redis.XMessage, error) {
	return c.readStream(ctx, StreamEngineRequests, group, consumer, timeout)
}

// SendEngineResponse publishes a calculation result to the engine:results channel.
func (c *Client) SendEngineResponse(ctx context.Context, resp EngineResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return c.rdb.Publish(ctx, ChannelEngineResults, data).Err()
}

func (c *Client) readStream(ctx context.Context, stream, group, consumer string, timeout time.Duration) ([]redis.XMessage, error) {
	err := c.rdb.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, err
	}

	entries, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    group,
		Consumer: consumer,
		Streams:  []string{stream, ">"},
		Count:    1,
		Block:    timeout,
	}).Result()

	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	if len(entries) == 0 {
		return nil, nil
	}
	return entries[0].Messages, nil
}

func (c *Client) Ack(ctx context.Context, stream, group, id string) error {
	return c.rdb.XAck(ctx, stream, group, id).Err()
}

func (c *Client) Rdb() *redis.Client {
	return c.rdb
}

func (c *Client) Close() error {
	return c.rdb.Close()
}
