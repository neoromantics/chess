package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // In production, we should check origin
	},
}

// Client represents a single WebSocket connection.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	gameID string
}

// Hub manages all active WebSocket connections.
type Hub struct {
	// Registered clients, keyed by gameID then by client.
	gameClients map[string]map[*Client]bool

	broadcast  chan broadcastMessage
	register   chan *Client
	unregister chan *Client

	mu sync.RWMutex
}

type broadcastMessage struct {
	gameID string
	data   []byte
}

func NewHub() *Hub {
	return &Hub{
		gameClients: make(map[string]map[*Client]bool),
		broadcast:   make(chan broadcastMessage),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if h.gameClients[client.gameID] == nil {
				h.gameClients[client.gameID] = make(map[*Client]bool)
			}
			h.gameClients[client.gameID][client] = true
			h.mu.Unlock()
			slog.Info("ws client registered", "game_id", client.gameID)

		case client := <-h.unregister:
			h.mu.Lock()
			if clients, ok := h.gameClients[client.gameID]; ok {
				if _, ok := clients[client]; ok {
					delete(clients, client)
					close(client.send)
					if len(clients) == 0 {
						delete(h.gameClients, client.gameID)
					}
				}
			}
			h.mu.Unlock()
			slog.Info("ws client unregistered", "game_id", client.gameID)

		case message := <-h.broadcast:
			h.mu.RLock()
			if clients, ok := h.gameClients[message.gameID]; ok {
				for client := range clients {
					select {
					case client.send <- message.data:
					default:
						// If send buffer is full, unregister client non-blockingly
						// Doing this inline would deadlock the Hub's Run() loop.
						go func(c *Client) {
							h.unregister <- c
						}(client)
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	gameID := r.URL.Query().Get("game_id")
	if gameID == "" {
		http.Error(w, "missing game_id", 400)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "error", err)
		return
	}

	client := &Client{hub: s.hub, conn: conn, send: make(chan []byte, 256), gameID: gameID}
	client.hub.register <- client

	// Start read/write pumps
	go client.writePump()
	go client.readPump()
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (c *Client) writePump() {
	defer c.conn.Close()
	for {
		message, ok := <-c.send
		if !ok {
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return
		}
		err := c.conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			return
		}
	}
}

// Event represents a structured WebSocket message.
type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

func (h *Hub) BroadcastState(gameID string, state any) {
	h.BroadcastEvent(gameID, "state", state)
}

func (h *Hub) BroadcastEvent(gameID string, eventType string, payload any) {
	event := Event{Type: eventType, Payload: payload}
	data, _ := json.Marshal(event)
	h.broadcast <- broadcastMessage{gameID: gameID, data: data}
}
