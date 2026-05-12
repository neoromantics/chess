package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/core"
	"github.com/neoromantics/chess/pkg/db"
)

const (
	// writeWait is the deadline for a single write.
	writeWait = 10 * time.Second
	// pongWait is how long we wait for a pong before declaring the conn dead.
	pongWait = 60 * time.Second
	// pingPeriod must be less than pongWait so we catch dead conns reliably.
	pingPeriod = (pongWait * 9) / 10
	// maxMessageSize caps inbound frames to protect the server.
	maxMessageSize int64 = 1 << 16
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // TODO: production origin allowlist
	},
}

// channelKind tags a Client subscription so the hub knows which keyspace
// (game vs user) the routing key lives in. Game and user IDs share no
// keyspace today, but tagging makes the hub robust to that ever changing
// — and makes the registry's debug output legible.
type channelKind int

const (
	kindGame channelKind = iota
	kindUser
)

// Client represents a single WebSocket connection. It is tied to exactly
// one routing key (gameID or userID); a browser tab with both an open
// game and a logged-in user header opens two separate WS connections.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	kind channelKind
	key  string // gameID for kindGame, userID-as-string for kindUser
}

// Hub fans out cross-pod Redis pub/sub messages to locally-connected WS
// clients. State is per-pod: every pod's hub has its own set of clients,
// but every pod's hub also subscribes to the same Redis channels, so
// publishing a single event reaches every client across the fleet.
//
// The hub does NOT hold any game state — it is purely a connection
// registry plus a pub/sub bridge. This is what makes us multi-replica safe.
type Hub struct {
	bus *bus.Client

	mu       sync.RWMutex
	gameSubs map[string]map[*Client]bool // gameID -> set of clients
	userSubs map[string]map[*Client]bool // userID -> set of clients

	register   chan *Client
	unregister chan *Client
}

func NewHub(b *bus.Client) *Hub {
	return &Hub{
		bus:        b,
		gameSubs:   make(map[string]map[*Client]bool),
		userSubs:   make(map[string]map[*Client]bool),
		register:   make(chan *Client, 32),
		unregister: make(chan *Client, 32),
	}
}

// Run starts the registry pump and both Redis pattern subscribers.
// Run blocks until ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	// Cross-pod fan-out: every pod's hub subscribes to the same patterns,
	// every publisher reaches every pod, each pod delivers to its own
	// locally-attached WS clients only.
	_ = h.bus.SubscribePattern(ctx, bus.GameEventGlob, func(channel string, payload []byte) {
		id := strings.TrimPrefix(channel, bus.GameEventPrefix)
		h.deliver(kindGame, id, payload)
	})
	_ = h.bus.SubscribePattern(ctx, bus.UserEventGlob, func(channel string, payload []byte) {
		id := strings.TrimPrefix(channel, bus.UserEventPrefix)
		h.deliver(kindUser, id, payload)
	})

	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.add(c)
		case c := <-h.unregister:
			h.remove(c)
		}
	}
}

func (h *Hub) add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bucket := h.bucketLocked(c.kind)
	if bucket[c.key] == nil {
		bucket[c.key] = make(map[*Client]bool)
	}
	bucket[c.key][c] = true
	WebSocketConnectionsActive.WithLabelValues(string(c.kind)).Inc()
	slog.Info("ws client registered", "kind", c.kind, "key", c.key, "subs", len(bucket[c.key]))
}

func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bucket := h.bucketLocked(c.kind)
	if clients, ok := bucket[c.key]; ok {
		if _, ok := clients[c]; ok {
			delete(clients, c)
			close(c.send)
			WebSocketConnectionsActive.WithLabelValues(string(c.kind)).Dec()
			if len(clients) == 0 {
				delete(bucket, c.key)
			}
		}
	}
	slog.Info("ws client unregistered", "kind", c.kind, "key", c.key)
}

func (h *Hub) bucketLocked(k channelKind) map[string]map[*Client]bool {
	if k == kindUser {
		return h.userSubs
	}
	return h.gameSubs
}

// deliver pushes the payload to every locally-subscribed Client for the
// given routing key. Non-blocking: if a client's send buffer is full,
// it's evicted async — never block the bus subscriber goroutine.
func (h *Hub) deliver(k channelKind, key string, payload []byte) {
	h.mu.RLock()
	bucket := h.bucketLocked(k)
	clients := bucket[key]
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.send <- payload:
		default:
			// Slow client; evict so a stuck WS can't back-pressure the bus.
			go func(c *Client) { h.unregister <- c }(c)
		}
	}
}

// Event is the cross-pod WS envelope. Type names are stable wire-protocol
// names (state, hint, assess, invite, match_found, ...) — adding a new
// type is additive; renaming breaks clients.
type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

// PublishGame broadcasts a typed event to all clients (across all pods)
// currently subscribed to gameID. This replaces the old in-process
// Hub.BroadcastState which only reached this pod.
func (h *Hub) PublishGame(ctx context.Context, gameID, eventType string, payload any) {
	ch := bus.GameEventChannel(gameID)
	if err := h.bus.Publish(ctx, ch, Event{Type: eventType, Payload: payload}); err != nil {
		slog.Warn("ws publish failed", "channel", ch, "error", err)
	}
}

// PublishUser broadcasts to all WS connections for a specific user, across
// pods. Used for invites, match-found, friend events.
func (h *Hub) PublishUser(ctx context.Context, userID int64, eventType string, payload any) {
	ch := bus.UserEventChannel(userID)
	if err := h.bus.Publish(ctx, ch, Event{Type: eventType, Payload: payload}); err != nil {
		slog.Warn("ws publish failed", "channel", ch, "error", err)
	}
}

// Shorthand: previous code called BroadcastState(gameID, snapshot) for
// the dominant case of a "state" event. Keep the helper to minimise
// handler churn during the migration.
func (h *Hub) BroadcastState(gameID string, state any) {
	h.PublishGame(context.Background(), gameID, "state", state)
}

func (h *Hub) BroadcastEvent(gameID, eventType string, payload any) {
	h.PublishGame(context.Background(), gameID, eventType, payload)
}

// === HTTP entrypoints ===

func (s *Server) handleWSGame(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", http.StatusBadRequest)
		return
	}
	// Authz: confirm the requesting user actually owns this game before
	// subscribing them to its event stream. /api/state and friends already
	// do this; without it here, a determined caller could watch any game.
	rec, err := s.db.GetGame(gameID)
	if err != nil {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	user, ok := auth.GetUser(r.Context())
	if !ok || !userOwnsGame(user.UserID, rec) {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	// Track connection in Redis (distributed)
	s.bus.TrackConnection(context.Background(), gameID, user.UserID, s.podID)
	// Player reconnected, clear any grace period
	side := core.White
	if rec.BlackUserID != nil && *rec.BlackUserID == user.UserID {
		side = core.Black
	}
	s.bus.DelGracePeriod(context.Background(), gameID, side)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}
	c := &Client{hub: s.hub, conn: conn, send: make(chan []byte, 256), kind: kindGame, key: gameID}
	s.hub.register <- c

	// Handle distributed cleanup
	go func() {
		c.readPump()
		count, _ := s.bus.UntrackConnection(context.Background(), gameID, user.UserID, s.podID)
		if count == 0 {
			// Authoritative status check
			freshRec, err := s.db.GetGame(gameID)
			if err == nil && freshRec.Status == "ongoing" {
				slog.Info("player fully disconnected, starting grace period", "game_id", gameID, "user_id", user.UserID)
				s.bus.SetGracePeriod(context.Background(), gameID, side, 60*time.Second)
			}
		}
	}()
	go c.writePump()
}

func (s *Server) handleWSUser(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}
	c := &Client{hub: s.hub, conn: conn, send: make(chan []byte, 256), kind: kindUser, key: strconv.FormatInt(user.UserID, 10)}
	s.hub.register <- c
	go c.writePump()
	go c.readPump()
}

// userOwnsGame is the unified ownership predicate. Will simplify when
// migration 000004 drops the legacy user_id column.
func userOwnsGame(userID int64, rec *db.GameRecord) bool {
	if rec.UserID == userID {
		return true
	}
	if rec.WhiteUserID != nil && *rec.WhiteUserID == userID {
		return true
	}
	if rec.BlackUserID != nil && *rec.BlackUserID == userID {
		return true
	}
	return false
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
