package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neoromantics/chess/pkg/auth"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 1 << 16
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // TODO: production origin allowlist
	},
}

func (gw *Gateway) handleWSGame(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}

	// Temp games take a different auth path: the chess-anon cookie
	// must match the game's owner. Routed inside this handler so the
	// SPA only needs one /ws endpoint regardless of game flavor.
	if strings.HasPrefix(gameID, "temp-") {
		gw.handleWSTempGame(w, r, gameID)
		return
	}

	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}

	// Per-game authorization at upgrade time. Without this any signed-in
	// user could subscribe to anyone's game.evt.{id} stream by guessing
	// UUIDs. Pre-flight: hit game-service /api/state with the user_id
	// injected; 200 = participant, 404 = not (or game doesn't exist).
	// One ~1ms internal RTT per upgrade, negligible.
	if !gw.userMayWatchGame(r.Context(), user.UserID, gameID) {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:    gw.hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		gameID: gameID,
		userID: user.UserID,
	}

	gw.hub.register <- client
	go client.writePump()
	client.readPump()
}

// handleWSTempGame is the cookie-authenticated WS path for anonymous
// temp games. Verifies the chess-anon cookie matches the temp game's
// OwnerAnonID by reading the JSON-encoded record straight out of
// Redis (same key that game-service writes). Subscribes to the same
// game.evt.{id} channel so hub fan-out is unchanged.
func (gw *Gateway) handleWSTempGame(w http.ResponseWriter, r *http.Request, gameID string) {
	anonID := readAnonCookie(r)
	if anonID == "" {
		http.Error(w, "no anon session", http.StatusUnauthorized)
		return
	}
	if !gw.tempGameOwnedBy(r.Context(), gameID, anonID) {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("temp ws upgrade failed", "error", err)
		return
	}
	client := &Client{
		hub:    gw.hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		gameID: gameID,
	}
	gw.hub.register <- client
	go client.writePump()
	client.readPump()
}

// tempGameOwnedBy answers "is this anon allowed to subscribe to this
// temp game?". Reads the same key game-service writes; only checks
// the OwnerAnonID field, not the rest of the record.
func (gw *Gateway) tempGameOwnedBy(ctx context.Context, gameID, anonID string) bool {
	v, err := gw.bus.Rdb().Get(ctx, "tempgame:state:"+gameID).Result()
	if err != nil {
		return false
	}
	var rec struct {
		OwnerAnonID string `json:"owner_anon_id"`
	}
	if err := json.Unmarshal([]byte(v), &rec); err != nil {
		return false
	}
	return rec.OwnerAnonID == anonID
}

func (gw *Gateway) handleWSUser(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.GetUser(r.Context())
	if !ok {
		http.Error(w, "unauthorized", 401)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:    gw.hub,
		conn:   conn,
		send:   make(chan []byte, 256),
		userID: user.UserID,
	}

	gw.hub.register <- client
	go client.writePump()
	client.readPump()
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

	// gorilla/websocket needs an active reader to honour pong handlers
	// and surface disconnect — but the SPA never sends WS messages
	// (every mutation is sync HTTP per the Streams-vs-HTTP rule), so
	// we drain reads and discard the payload. The original protocol
	// translated `move` / `resign` / `offer_draw` etc. into Commands
	// here; that path was deleted along with the inbound-WS surface.
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
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
