# 2. The shape of the system

```
Browser (Vue 3 SPA, embedded in gateway binary)
        │ HTTPS / WSS
        ▼
   gateway  ──── auth, profiles, HTTP routing, WebSocket fan-out ────┐
        │                                                            │
        │ sync HTTP for game endpoints                               │
        │ Redis Streams for intent dispatch (matchmaker, engine)     │
        ▼                                                            │
  game-service  ──── moves, invites, matchmaking, ratings ──────┐    │
        │                                                       │    │
        ▼                                                       ▼    ▼
  engine-worker  ──── CPU search, HPA on queue depth ──────  Postgres  Redis
                                                             (truth)   (cache+bus)
```

Three Go services. One Postgres. One Redis. That's the whole system. The reason it isn't *six* services (gateway + user + game + matchmaker + rating-updater + engine) is that we tried that and most of the bugs were wire-protocol drift across service boundaries. **Splitting a system into more services doesn't make it simpler; it makes the boundaries more important and more brittle.** Default to consolidation.

`engine-worker` stays separate for a real reason: each pod does *one search at a time* (CPU-bound, parallelism = `runtime.GOMAXPROCS(0)`), and we autoscale on queue depth. If you want more search throughput, you scale *out* (more pods), not threads inside a pod. The other services can be replicated freely; they're stateless web servers.

## The service responsibilities, briefly

| Service | What it owns |
|---|---|
| **gateway** (`cmd/gateway`) | JWT auth, signup/login/profile, anonymous-cookie session for unsigned-in players, WebSocket fan-out, reverse-proxy to game-service, serves the SPA. The only service with `JWT_SECRET`. |
| **game-service** (`cmd/game`) | All authoritative game state. Move validation, invites, matchmaking, Glicko-2 rating updates, draw/takeback/rematch protocols, replay generation. |
| **engine-worker** (`cmd/engine-worker`) | Reads search requests from a Redis Stream, runs the engine search, writes results back. Also has a CLI mode (`-uci`) so the same binary can be plugged into chess GUIs like Arena. |

Reference depth: [`../docs/architecture/overview.md`](../docs/architecture/overview.md).

## Why Postgres *and* Redis

You probably learned them as alternatives. In a real system they do different jobs:

- **Postgres = durable truth.** If the cluster vanishes and you bring it back, Postgres is what survives. Every `games` row, every `users` row, every result of every move lives here. Use it for anything you can't reconstruct.
- **Redis = hot cache + message bus + lock store.** Fast (in-memory), but a single instance — if it dies, the *cache* is cold but the *truth* is fine. Used for: write-through caching of hot game rows, distributed locks (`SETNX`), Pub/Sub for ephemeral browser updates, and Streams for durable cross-service messaging.

A move flows like this: the handler locks the game in Redis → reads the game row from Redis cache (or falls back to Postgres) → applies the move in memory → writes back to **Postgres first**, then updates the Redis cache → publishes the event to Pub/Sub for live browsers. Postgres is the source of truth; Redis is the speed layer.

---

Next: [`03-mental-jumps/`](03-mental-jumps/) — the eleven "CS to production" jumps you need to internalize.
