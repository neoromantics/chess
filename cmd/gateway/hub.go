package main

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/neoromantics/chess/pkg/eventbus"
)

type Hub struct {
	bus *eventbus.Client

	mu       sync.RWMutex
	gameSubs map[string]map[*Client]bool // gameID -> set of clients
	userSubs map[string]map[*Client]bool // userID -> set of clients

	register   chan *Client
	unregister chan *Client
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	gameID string
	userID int64
}

func NewHub(b *eventbus.Client) *Hub {
	return &Hub{
		bus:        b,
		gameSubs:   make(map[string]map[*Client]bool),
		userSubs:   make(map[string]map[*Client]bool),
		register:   make(chan *Client, 32),
		unregister: make(chan *Client, 32),
	}
}

func (h *Hub) Run(ctx context.Context) {
	// Cross-pod fan-out: every Gateway pod PSUBSCRIBEs to all event patterns.
	// When any service emits an event via EmitEvent() or PublishUserEvent(),
	// every Gateway pod receives it and delivers it to its locally attached clients.
	go h.listenToRedis(ctx, "game.evt.*", kindGame)
	go h.listenToRedis(ctx, "user.evt.*", kindUser)

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

func (h *Hub) listenToRedis(ctx context.Context, pattern string, kind channelKind) {
	pubsub := h.bus.Rdb().PSubscribe(ctx, pattern)
	defer pubsub.Close()

	ch := pubsub.Channel()
	prefix := ""
	if kind == kindGame {
		prefix = "game.evt."
	} else {
		prefix = "user.evt."
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			if msg == nil {
				return
			}
			key := strings.TrimPrefix(msg.Channel, prefix)
			h.deliver(kind, key, []byte(msg.Payload))
		}
	}
}

func (h *Hub) add(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.gameID != "" {
		if h.gameSubs[c.gameID] == nil {
			h.gameSubs[c.gameID] = make(map[*Client]bool)
		}
		h.gameSubs[c.gameID][c] = true
	}
	if c.userID != 0 {
		uidStr := strconv.FormatInt(c.userID, 10)
		if h.userSubs[uidStr] == nil {
			h.userSubs[uidStr] = make(map[*Client]bool)
		}
		h.userSubs[uidStr][c] = true
	}
	slog.Info("ws client registered", "game_id", c.gameID, "user_id", c.userID)
}

func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.gameID != "" {
		if clients, ok := h.gameSubs[c.gameID]; ok {
			delete(clients, c)
			if len(clients) == 0 {
				delete(h.gameSubs, c.gameID)
			}
		}
	}
	if c.userID != 0 {
		uidStr := strconv.FormatInt(c.userID, 10)
		if clients, ok := h.userSubs[uidStr]; ok {
			delete(clients, c)
			if len(clients) == 0 {
				delete(h.userSubs, uidStr)
			}
		}
	}
	close(c.send)
	slog.Info("ws client unregistered", "game_id", c.gameID, "user_id", c.userID)
}

func (h *Hub) deliver(kind channelKind, key string, payload []byte) {
	h.mu.RLock()
	var clients map[*Client]bool
	if kind == kindGame {
		clients = h.gameSubs[key]
	} else {
		clients = h.userSubs[key]
	}
	
	// Copy pointers to avoid holding lock during send
	targets := make([]*Client, 0, len(clients))
	for c := range clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		select {
		case c.send <- payload:
		default:
			go func(c *Client) { h.unregister <- c }(c)
		}
	}
}

type channelKind int

const (
	kindGame channelKind = iota
	kindUser
)

