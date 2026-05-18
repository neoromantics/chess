package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
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

// allowedWSOrigins enumerates Origins beyond same-origin that may open
// a WebSocket. Loaded lazily from $ALLOWED_WS_ORIGINS (comma-separated)
// the first time CheckOrigin runs. Same-origin always passes, so this
// only needs entries when the SPA and gateway live on different hosts
// (typically: a Vite dev server proxying to a localhost gateway).
var (
	allowedWSOnce    sync.Once
	allowedWSOrigins map[string]bool
)

func loadAllowedWSOrigins() {
	allowedWSOnce.Do(func() {
		allowedWSOrigins = make(map[string]bool)
		raw := os.Getenv("ALLOWED_WS_ORIGINS")
		for _, o := range strings.Split(raw, ",") {
			if o = strings.TrimSpace(o); o != "" {
				allowedWSOrigins[strings.ToLower(o)] = true
			}
		}
	})
}

// checkWSOrigin allows the connection if any of:
//   - no Origin header (non-browser callers — curl, native apps — never
//     send one; CSRF/CSWSH is a browser-only concern, so this is safe)
//   - Origin's host matches the request's Host header (same-origin —
//     the SPA we serve always hits its own gateway)
//   - Origin appears in $ALLOWED_WS_ORIGINS (dev / cross-origin SPAs)
//
// Anything else (an attacker site embedding ws://our-host/) is denied.
// Without this, the user's auth cookie would auto-attach and let the
// attacker subscribe to game.evt.* on their behalf — classic CSWSH.
func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	loadAllowedWSOrigins()
	return allowedWSOrigins[strings.ToLower(origin)]
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkWSOrigin,
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

	// Spectator-aware auth: signed-in users always pass the can_watch
	// preflight for their own games + every public game; anonymous
	// viewers pass only for public games (the gate returns 404 for
	// private ones, mirroring the existence-leak rule). Allowing the
	// unauthenticated path is what makes shareable spectator links work
	// without forcing a signup.
	var userID int64
	if user, ok := auth.GetUser(r.Context()); ok {
		userID = user.UserID
	}
	may, isPlayer := gw.userMayWatchGame(r.Context(), userID, gameID)
	if !may {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}

	client := &Client{
		hub:         gw.hub,
		conn:        conn,
		send:        make(chan []byte, 256),
		gameID:      gameID,
		userID:      userID,
		isSpectator: !isPlayer,
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
		// Temp games have a single anonymous owner and no spectators;
		// the connecting client is always the player.
		isSpectator: false,
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
