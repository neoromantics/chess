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
	// StreamEngineResults replaced the old ChannelEngineResults
	// Pub/Sub channel after a production incident: pub/sub drops
	// messages when no subscriber is connected (game-service mid
	// rolling restart, OOM, etc.). engine:results is now a durable
	// stream with a consumer group so at-least-once delivery applies
	// in both directions of the engine round-trip.
	StreamEngineResults = "engine:results"
	ChannelUserEvents   = "user:events:" // Prefix: user:events:{userID}
)

// EngineRequest represents a request for the engine to calculate a move.
type EngineRequest struct {
	GameID   string            `json:"game_id"`
	FEN      string            `json:"fen"`
	History  map[uint64]int    `json:"history"`
	MoveTime time.Duration     `json:"movetime"`
	Context  string            `json:"context"` // "move", "hint"
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
	CmdMakeMove      = "MakeMove"
	CmdResign        = "Resign"
	CmdOfferDraw     = "OfferDraw"
	CmdAcceptDraw    = "AcceptDraw"
	CmdDeclineDraw   = "DeclineDraw"
	CmdHint          = "Hint"
	CmdNewGame       = "NewGame"
	CmdCreatePvPGame = "CreatePvPGame"

	// Matchmaking Commands
	CmdJoinQueue     = "JoinQueue"
	CmdLeaveQueue    = "LeaveQueue"
	CmdSendInvite    = "SendInvite"
	CmdAcceptInvite  = "AcceptInvite"
	CmdDeclineInvite = "DeclineInvite"
	CmdCancelInvite  = "CancelInvite"
)

// Command payloads
type MakeMoveCmd struct {
	Move string `json:"move"` // UCI format
}

type ResignCmd struct{}
type OfferDrawCmd struct{}
type AcceptDrawCmd struct{}
type DeclineDrawCmd struct{}

// NewGameCmd carries the human-vs-engine creation params from the
// dashboard's "Engine think" dropdown. EngineWhite/EngineBlack default
// to false/true (you play white). ThinkTimeMS bounded to a sane range
// in the handler.
type NewGameCmd struct {
	EngineWhite bool `json:"engine_white"`
	EngineBlack bool `json:"engine_black"`
	ThinkTimeMS int  `json:"think_time_ms"`
}

type CreatePvPGameCmd struct {
	WhiteUserID int64  `json:"white_user_id"`
	BlackUserID int64  `json:"black_user_id"`
	TimeControl string `json:"time_control"`
	Rated       bool   `json:"rated"`
}

type JoinQueueCmd struct {
	TimeControl string `json:"time_control"`
	Rating      int    `json:"rating"`
}

type LeaveQueueCmd struct {
	TimeControl string `json:"time_control"`
}

type SendInviteCmd struct {
	ToUserID    int64  `json:"to_user_id"`
	TimeControl string `json:"time_control"`
	Rated       bool   `json:"rated"`
}

type AcceptInviteCmd struct {
	InviteID string `json:"invite_id"`
}

type DeclineInviteCmd struct {
	InviteID string `json:"invite_id"`
}

type CancelInviteCmd struct {
	InviteID string `json:"invite_id"`
}

// Event types.
//
// Naming convention: events published on the per-user pub/sub channels
// (user.evt.{id}) use snake_case so the SPA's Pinia stores can listen
// for them directly. Events on per-game channels (game.evt.{id}) and
// durable streams (game:events) use CamelCase historically; the SPA's
// GameView accepts both forms for those.
const (
	// Game-channel events (game.evt.{id} + game:events stream)
	EvtMovePlayed   = "MovePlayed"
	EvtGameFinished = "GameFinished"
	EvtGameStarted  = "GameStarted"
	EvtStateUpdated = "StateUpdated"

	// User-channel events (user.evt.{id}). snake_case matches what
	// every Pinia store .on()s for. Renaming any of these is a
	// wire-protocol break across backend services and the SPA stores.
	EvtMatchFound      = "match_found"
	EvtInviteCreated   = "invite_created"
	EvtInviteSent      = "invite_sent"
	EvtInviteAccepted  = "invite_accepted"
	EvtInviteDeclined  = "invite_declined"
	EvtInviteCancelled = "invite_cancelled"
	EvtInviteExpired   = "invite_expired"
)

// MatchFoundEvt is the payload published on each paired user's
// user.evt.{id} channel. color is the recipient's color in the new
// game so the SPA can flip the board orientation immediately.
type MatchFoundEvt struct {
	GameID      string `json:"game_id"`
	WhiteUserID int64  `json:"white_user_id"`
	BlackUserID int64  `json:"black_user_id"`
	Color       string `json:"color"` // "white" or "black", from the recipient's POV
}

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

type InviteSentEvt struct {
	InviteID    string `json:"invite_id"`
	FromUserID  int64  `json:"from_user_id"`
	ToUserID    int64  `json:"to_user_id"`
	TimeControl string `json:"time_control"`
}

// Command is an intent to change state. Dispatched by Gateway, consumed by services.
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

// SendEngineResponse appends a calculation result to the engine:results
// stream. Durable — game-service can pick it up after a restart instead
// of losing the move forever (which is what happened when this was a
// Pub/Sub channel).
func (c *Client) SendEngineResponse(ctx context.Context, resp EngineResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = c.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamEngineResults,
		Values: map[string]interface{}{"data": data},
	}).Result()
	return err
}

// ReadEngineResults blocks until an engine result is available. Used
// by game-service to consume worker outputs as a durable stream.
func (c *Client) ReadEngineResults(ctx context.Context, group, consumer string, timeout time.Duration) ([]redis.XMessage, error) {
	return c.readStream(ctx, StreamEngineResults, group, consumer, timeout)
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
