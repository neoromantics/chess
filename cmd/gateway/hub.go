package main

// Per-game and per-user WebSocket fan-out. Each gateway pod runs one
// Hub; clients register themselves with their game_id / user_id and
// the hub forwards every game.evt.{id} / user.evt.{id} message arriving
// from Redis to the matching local clients.
//
// Scale design note: the obvious "PSUBSCRIBE game.evt.*" pattern means
// every pod receives every game event, regardless of whether it has
// any subscribers for that game. With N gateway pods and M events/sec,
// inbound Redis bandwidth and per-pod parse work both grow N×M. We
// instead SUBSCRIBE per-channel on demand: when the first client for
// game G registers, the hub SUBSCRIBEs game.evt.G; when the last one
// leaves, it UNSUBSCRIBEs. Cross-pod fan-out cost becomes proportional
// to (active games) + (gateway pods with at least one local client per
// game), which converges on the optimal "1 pod gets each event" once
// game ownership becomes sticky to a pod (sticky LBs / consistent hash).
//
// user.evt uses the same pattern. Per-user channels usually have just
// one connected tab, so the unsubscribed-pods savings are even bigger.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neoromantics/chess/pkg/eventbus"
	"github.com/neoromantics/chess/pkg/metrics"
	"github.com/redis/go-redis/v9"
)

// viewerCountTTL caps how long a leaked viewer counter (pod crashed
// mid-session) lingers in Redis. Refreshed on every INCR so an active
// game's counter never expires; only orphans from crashes age out.
const viewerCountTTL = 24 * time.Hour

func viewersKey(gameID string) string { return "game:viewers:" + gameID }

type Hub struct {
	bus *eventbus.Client

	mu       sync.RWMutex
	gameSubs map[string]map[*Client]bool // gameID -> set of clients
	userSubs map[string]map[*Client]bool // userID -> set of clients

	register   chan *Client
	unregister chan *Client

	// One PubSub per kind; both started in Run() and survive the hub's
	// lifetime. Subscribe / Unsubscribe calls are serialized through
	// the hub event loop, so we don't need a lock around the PubSubs.
	gamePubSub *redis.PubSub
	userPubSub *redis.PubSub
}

type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte

	gameID string
	userID int64
	// isSpectator gates participation in the per-game viewer count.
	// Players (set false) watch their own games without inflating the
	// "N watching" chip; only true spectators contribute. Source of
	// truth: X-Is-Player from /api/games/{id}/can_watch at upgrade time.
	isSpectator bool
}

func NewHub(b *eventbus.Client) *Hub {
	return &Hub{
		bus:      b,
		gameSubs: make(map[string]map[*Client]bool),
		userSubs: make(map[string]map[*Client]bool),
		// 1024-deep register/unregister so a reconnect storm (post
		// network blip, pod restart, or rolling deploy) doesn't block
		// the upgrade handlers. Each pending Client is ~200 bytes; even
		// fully queued it's <250 KB per pod.
		register:   make(chan *Client, 1024),
		unregister: make(chan *Client, 1024),
	}
}

func (h *Hub) Run(ctx context.Context) {
	// Open one PubSub per kind with no initial channels; Subscribe is
	// driven on demand by client (un)registration. Closing either also
	// closes its goroutine.
	h.gamePubSub = h.bus.Rdb().Subscribe(ctx)
	h.userPubSub = h.bus.Rdb().Subscribe(ctx)
	defer h.gamePubSub.Close()
	defer h.userPubSub.Close()

	go h.forward(ctx, h.gamePubSub, "game.evt.", kindGame)
	go h.forward(ctx, h.userPubSub, "user.evt.", kindUser)

	for {
		select {
		case <-ctx.Done():
			return
		case c := <-h.register:
			h.add(ctx, c)
		case c := <-h.unregister:
			h.remove(ctx, c)
		}
	}
}

func (h *Hub) forward(ctx context.Context, pubsub *redis.PubSub, prefix string, kind channelKind) {
	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok || msg == nil {
				return
			}
			key := msg.Channel
			if len(key) >= len(prefix) {
				key = key[len(prefix):]
			}
			h.deliver(kind, key, []byte(msg.Payload))
		}
	}
}

// add registers a client. When a game/user transitions from "no local
// subscribers" to "one local subscriber", the hub SUBSCRIBEs the
// matching Redis channel; subsequent registrations for the same key
// are a no-op on Redis.
func (h *Hub) add(ctx context.Context, c *Client) {
	h.mu.Lock()
	var newGameKey, newUserKey string
	if c.gameID != "" {
		if h.gameSubs[c.gameID] == nil {
			h.gameSubs[c.gameID] = make(map[*Client]bool)
			newGameKey = "game.evt." + c.gameID
		}
		h.gameSubs[c.gameID][c] = true
		metrics.WSConnectionsActive.WithLabelValues("gateway", "game").Inc()
	}
	if c.userID != 0 {
		uidStr := strconv.FormatInt(c.userID, 10)
		if h.userSubs[uidStr] == nil {
			h.userSubs[uidStr] = make(map[*Client]bool)
			newUserKey = "user.evt." + uidStr
		}
		h.userSubs[uidStr][c] = true
		metrics.WSConnectionsActive.WithLabelValues("gateway", "user").Inc()
	}
	h.mu.Unlock()
	// Subscribe calls block on a Redis round-trip; do them outside the
	// lock so a slow Redis doesn't stall other registers/unregisters.
	if newGameKey != "" {
		if err := h.gamePubSub.Subscribe(ctx, newGameKey); err != nil {
			slog.Warn("hub: subscribe failed", "channel", newGameKey, "error", err)
		}
	}
	if newUserKey != "" {
		if err := h.userPubSub.Subscribe(ctx, newUserKey); err != nil {
			slog.Warn("hub: subscribe failed", "channel", newUserKey, "error", err)
		}
	}
	// Bump the per-game viewer counter and broadcast the new total.
	// PUBSUB NUMSUB would count pods, not clients; we maintain our own
	// INCR/DECR counter for accuracy. Broadcast goes on the same
	// per-game channel the client just subscribed to so they see their
	// own arrival reflected, and so other pods' subscribers update too.
	// Players watching their own game are excluded so the "N watching"
	// chip reflects audience size, not "is the player here."
	if c.gameID != "" && c.isSpectator {
		h.broadcastViewerCount(ctx, c.gameID, +1)
	}
	slog.Info("ws client registered", "game_id", c.gameID, "user_id", c.userID, "spectator", c.isSpectator)
}

// remove tears down a client. When a game/user transitions to "no
// local subscribers", the hub UNSUBSCRIBEs so other pods stop sending
// those events here.
func (h *Hub) remove(ctx context.Context, c *Client) {
	h.mu.Lock()
	var dropGameKey, dropUserKey string
	if c.gameID != "" {
		if clients, ok := h.gameSubs[c.gameID]; ok {
			if _, present := clients[c]; present {
				delete(clients, c)
				metrics.WSConnectionsActive.WithLabelValues("gateway", "game").Dec()
			}
			if len(clients) == 0 {
				delete(h.gameSubs, c.gameID)
				dropGameKey = "game.evt." + c.gameID
			}
		}
	}
	if c.userID != 0 {
		uidStr := strconv.FormatInt(c.userID, 10)
		if clients, ok := h.userSubs[uidStr]; ok {
			if _, present := clients[c]; present {
				delete(clients, c)
				metrics.WSConnectionsActive.WithLabelValues("gateway", "user").Dec()
			}
			if len(clients) == 0 {
				delete(h.userSubs, uidStr)
				dropUserKey = "user.evt." + uidStr
			}
		}
	}
	h.mu.Unlock()
	close(c.send)
	if dropGameKey != "" {
		if err := h.gamePubSub.Unsubscribe(ctx, dropGameKey); err != nil {
			slog.Warn("hub: unsubscribe failed", "channel", dropGameKey, "error", err)
		}
	}
	if dropUserKey != "" {
		if err := h.userPubSub.Unsubscribe(ctx, dropUserKey); err != nil {
			slog.Warn("hub: unsubscribe failed", "channel", dropUserKey, "error", err)
		}
	}
	// Decrement the per-game counter. Order matters: the local UNSUBSCRIBE
	// happens above so this departing client won't receive its own
	// decrement (good — they're gone). Other pods' subscribers do.
	// Mirrors the add path: only spectators ever incremented, so only
	// spectators decrement.
	if c.gameID != "" && c.isSpectator {
		h.broadcastViewerCount(ctx, c.gameID, -1)
	}
	slog.Info("ws client unregistered", "game_id", c.gameID, "user_id", c.userID, "spectator", c.isSpectator)
}

// broadcastViewerCount adjusts the per-game viewer counter by delta
// (+1 on add, -1 on remove) and publishes the new total on the game
// channel so every subscriber renders an updated "N watching". Best-
// effort — Redis hiccups log and move on; the count will resync the
// next time a client joins/leaves.
//
// We maintain our own counter rather than relying on PUBSUB NUMSUB
// because NUMSUB counts pub/sub connections (i.e. pods with a hub
// that has subscribed), not WS clients. A single pod with 50 viewers
// would show NUMSUB=1, not 50.
func (h *Hub) broadcastViewerCount(ctx context.Context, gameID string, delta int64) {
	key := viewersKey(gameID)
	rdb := h.bus.Rdb()

	var count int64
	if delta > 0 {
		n, err := rdb.IncrBy(ctx, key, delta).Result()
		if err != nil {
			slog.Warn("hub: viewer count incr failed", "game_id", gameID, "error", err)
			return
		}
		count = n
		// Refresh TTL so an active game's counter never expires; only
		// orphaned counters (from a crashed pod that never decremented)
		// age out via this 24h cap.
		_ = rdb.Expire(ctx, key, viewerCountTTL).Err()
	} else {
		n, err := rdb.DecrBy(ctx, key, -delta).Result()
		if err != nil {
			slog.Warn("hub: viewer count decr failed", "game_id", gameID, "error", err)
			return
		}
		if n < 0 {
			// Defensive: a crashed pod can leave us with a negative if
			// its INCRs never got the matching DECRs. Clamp to 0 and
			// repair the key so future broadcasts start sane.
			_ = rdb.Set(ctx, key, 0, viewerCountTTL).Err()
			n = 0
		}
		count = n
	}

	payload, _ := json.Marshal(map[string]int64{"count": count})
	evt := eventbus.Event{
		Type:      eventbus.EvtViewerCount,
		GameID:    gameID,
		Payload:   payload,
		Timestamp: time.Now(),
	}
	data, _ := json.Marshal(evt)
	if err := rdb.Publish(ctx, "game.evt."+gameID, data).Err(); err != nil {
		slog.Warn("hub: viewer count publish failed", "game_id", gameID, "error", err)
	}
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
