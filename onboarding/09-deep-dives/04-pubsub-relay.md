# 9.4 Pub/Sub: the gateway-as-relay topology

[§3.4](../03-mental-jumps/04-streams-vs-pubsub.md) covered "ephemeral vs durable." Two further details are load-bearing.

**Browsers are not Redis clients.** "Client" in Redis-speak means anything that holds a TCP connection to `redis-server` and speaks the wire protocol — a Go service, `redis-cli` from a debug shell, your laptop. In production, the only Redis clients are pods inside the cluster. Browsers connect to *gateway pods* via WebSocket; each gateway pod opens **one** long-lived Pub/Sub connection to Redis (the hub goroutine) and acts as a *relay* between Redis Pub/Sub and per-browser WebSockets.

```
                                       Pub/Sub conn         WebSocket
game-service ──PUBLISH game.evt.42──▶ Redis ──────▶  gateway pod  ──────▶  browser
                                                  (Redis client)         (WS client)
```

This indirection earns its keep: Redis isn't exposed to the public internet; connection counts stay sane (one Redis conn per gateway pod fans out to thousands of browser sockets, instead of one Redis conn per browser); per-channel auth (only deliver `game.evt.42` to a browser whose JWT proves it may watch game 42) lives in the gateway because Redis has no concept of "users"; and the per-channel ref-counted subscription pattern below only works because the gateway is the unit that subscribes.

**Per-channel SUBSCRIBE, never PSUBSCRIBE wildcards.** The obvious shortcut — each pod does `PSUBSCRIBE game.evt.*` at boot, filters locally — works perfectly at small scale and silently destroys you at large scale. Pub/Sub fan-out cost is `O(messages × subscribers)`. With wildcards, *every gateway pod is a subscriber to every channel*, even ones nobody local cares about. At 5 pods × 1000 active games × 2 moves/sec, that's 10K Pub/Sub messages/sec — most of them delivered to pods that immediately throw them away.

The harder, correct thing: when the first local WebSocket for game 42 connects to pod A, the hub bumps a ref count and (if it hit 1) issues `SUBSCRIBE game.evt.42`. When the last local browser for game 42 on pod A disconnects, the count drops to 0 and the hub issues `UNSUBSCRIBE`. Fan-out becomes `O(messages × pods-with-a-live-subscriber)` instead of `O(messages × all-pods)`. The wiring is in `cmd/gateway/hub.go` — the comment at the top of the file calls this out as a scale-design note.

---

← [`03-structure-is-index.md`](03-structure-is-index.md) · Back to [`README.md`](README.md)
