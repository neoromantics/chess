package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neoromantics/chess/pkg/auth"
	"github.com/neoromantics/chess/pkg/eventbus"
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

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			break
		}

		// Translate WS message to Command
		var raw struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(message, &raw); err != nil {
			continue
		}

		// We only translate specific types into Commands.
		// Others might be purely for the Gateway or discarded.
		cmdType := ""
		switch raw.Type {
		case "move":
			cmdType = eventbus.CmdMakeMove
		case "resign":
			cmdType = eventbus.CmdResign
		case "offer_draw":
			cmdType = eventbus.CmdOfferDraw
		case "hint":
			// We can dispatch these directly or via Command
			cmdType = "Hint"
		case "assess":
			cmdType = "Assess"
		case "new_game":
			cmdType = "NewGame"
		}

		if cmdType != "" {
			cmd := eventbus.Command{
				Type:    cmdType,
				GameID:  c.gameID,
				UserID:  c.userID,
				Payload: raw.Payload,
			}
			if _, err := c.hub.bus.SendCommand(context.Background(), cmd); err != nil {
				slog.Error("failed to dispatch command from ws", "game_id", c.gameID, "error", err)
			}
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
