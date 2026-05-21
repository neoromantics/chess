# Architecture overview

A production multiplayer chess platform deployed to `https://vcm-50800.vm.duke.edu` on k3s with Traefik + Let's Encrypt. **Every change must survive multi-replica deployment, rolling restarts, and HTTPS reverse-proxying** — "works locally" is not sufficient.

## The picture

```
                  Browser (Vue 3 SPA, embedded into the gateway binary)
                                  │
                                  ▼  HTTPS + WSS
        ┌────────────────────── gateway ──────────────────────┐
        │ JWT auth, signup/login/profile/search,              │
        │ HTTP routing, WebSocket fan-out (SUBSCRIBE), intent │
        │ dispatch (Commands) to game-service, replay HTML.   │
        └──────┬──────────────────────────────┬───────────────┘
               │                              │
               ▼                              ▼
         game-service (HPA 2-6)         engine-worker (HPA 2-8)
         · sync HTTP: /api/move,        · CPU-bound search
           /api/state, /api/games,      · engine:requests stream
           /api/invites/*, /api/resign  · engine:results stream
         · stream consumers: game       · concurrency = GOMAXPROCS(0)
           commands, engine results       (one search per pod)
         · goroutines (singletons via
           leader election): matchmaker
           pairing, invite expiry sweep,
           rating-updater (Glicko-2)
                │                                ▲
                ▼                                │
              Postgres  ◀──── shared cache + bus ──── Redis
              (durable truth)                   (hot cache, streams,
                                                 pub/sub, locks)
```

**Three Go services**, one Postgres, one Redis. All cross-service comms via Redis. All three services build from a single multi-stage Dockerfile into one image; the binary to run is selected by the deployment's `command:`.

## Why three services and not six

We were briefly six pods (gateway, user-service, game-service, matchmaker, rating-updater, engine-worker). The split was over-engineered for this scale and most of our bugs were wire-protocol drift across the extra boundaries. Consolidated 6 → 3 in commits `c413513…43d5f8b`: user-service folded into gateway, matchmaker + rating-updater folded into game-service. **engine-worker stays separate** because CPU-bound search has a genuinely different scaling profile (HPA on queue depth, can scale out wide).

**This is not a fully event-sourced platform** despite some early docs framing it that way. The Streams layer is intentionally narrow — see `architecture/redis-patterns.md` for the Streams-vs-HTTP rule.

## Service responsibilities

### `gateway` (`cmd/gateway`)

Stateless HTTP/WS entry. Validates JWT via `auth.Middleware`, serves the auth + profile surface directly (signup, login, change-password, `/api/user/*`, `/api/users/search`), reverse-proxies game endpoints to game-service with `?user_id=N` injection. For the anonymous temp-game surface, mints a `chess-anon` HttpOnly cookie on first hit and proxies `/api/temp/*` with `?anon_id=<uuid>` injected by `injectAnonID` (cookie-as-identity, mirrors the JWT-as-identity pattern). Handles intent dispatch (`POST /api/games/new`, `POST /api/matchmaking/{join,leave}`) by appending Commands to the `game:commands` stream.

**Frontend SPA is embedded only here** via `go:embed all:dist`. The hub runs Redis `SUBSCRIBE game.evt.{id}` / `user.evt.{id}` on demand to fan messages out to locally-attached WebSocket clients. Two WS endpoints: `/ws?game_id=X` (per-game stream — branches inside `handleWSGame` on the `temp-` prefix; durable IDs use the JWT path, temp IDs use the cookie path) and `/ws/user` (per-user, for invites/match-found). Opens its own PG pool for the auth + profile reads.

Auth surface is hardened: per-IP token-bucket rate limiter (`authLim` 12/min, `signupLim` 6/hr, `probeLim` 10 burst @1/2s for check-username), per-route `http.MaxBytesReader` body caps, and a shared bounded `*http.Transport` reused by both the reverse proxy and explicit upstream calls (`gw.upstream`) so a hung game-service can't OOM the gateway. WS upgrade enforces same-origin + env-driven `ALLOWED_WS_ORIGINS` allow-list (CSWSH defense) in `cmd/gateway/ws.go:checkWSOrigin`.

### `game-service` (`cmd/game`)

Authoritative game state machine + everything else that touches game state. Sync HTTP for the SPA contract: `GET /api/games` (list), `DELETE /api/games/{id}`, and the per-game verbs nested under `/api/games/{id}/<verb>` (`state`, `move`, `resign`, `new`, `undo`, `set_players`, `set_position`, `hint`, `replay`, `pgn`, `load_pgn`, `analyze`, `visibility`, `can_watch`, `draw_*`, `takeback_*`, `rematch_*`), plus `/api/invites/*`, plus the anonymous temp-game surface `/api/temp/*` (Redis-only, 10-minute sliding TTL — see `cmd/game/temp.go`) and the internal `/api/temp/upgrade` the gateway calls during signup.

Stream consumers: `game:commands` (game-service-group) for engine-translated MakeMove + JoinQueue/LeaveQueue, `engine:results` for engine-worker outputs (branches on `resp.Context` for `move` / `hint` / `assess`, then on `Metadata["temp"]` to route results to either `CmdMakeMove` or `applyTempEngineMove`).

Goroutines started at boot: invite expiry sweeper (30s ticker), matchmaker pairing loop (2s ticker, Redis-leader-elected via `mm:leader`), clock flag-fall sweeper (500ms ticker), Glicko-2 rating updater (consumes `game:events` for `GameFinished`). Every read-modify-write on `games` rows goes through the per-game Redis lock and the hot cache; the same `game:lock:{id}` lock also serializes temp-game mutations.

Source layout: `cmd/game/main.go` is boot only (~190 lines); the read surface lives in `state.go`, command-stream consumer + new-game/PvP/MakeMove in `commands.go`, and engine-result fan-in in `engine_results.go`. Test helpers (`panicStore` + `gameStore` in-memory composer, `newTestService`) live in `testhelpers_test.go` and back the `lock_test.go` / `handle_move_test.go` suites.

**Note:** `/api/touch` and `/api/touch_move` from the 0.x platform stay deleted — touch-move is now a client-side session toggle. See `pkg/wire/CONTRACT.md`.

### `engine-worker` (`cmd/engine-worker`)

CPU-bound search. Reads the `engine:requests` stream (`engine-worker-group`), publishes results to the `engine:results` stream. Runs **exactly one search per pod**: concurrency caps at `runtime.GOMAXPROCS(0)` (cgroup-aware, **not** `runtime.NumCPU()`). To handle more concurrent searches, scale OUT (more pods), don't pack threads. Same binary can run as a UCI CLI via `-uci`; opt-in only, never auto-detected (that bug bit us — every k8s pod tty-check failed and silently EOF'd).

## Why Postgres *and* Redis

You probably learned them as alternatives. In a real system they do different jobs:

- **Postgres = durable truth.** If the cluster vanishes and you bring it back, Postgres is what survives. Every `games` row, every `users` row, every result of every move lives here. Use it for anything you can't reconstruct.
- **Redis = hot cache + message bus + lock store.** Fast (in-memory), but a single instance — if it dies, the *cache* is cold but the *truth* is fine. Used for: write-through caching of hot game rows, distributed locks (`SETNX`), Pub/Sub for ephemeral browser updates, and Streams for durable cross-service messaging.

A move flows like this: the handler locks the game in Redis → reads the game row from Redis cache (or falls back to Postgres) → applies the move in memory → writes back to **Postgres first**, then updates the Redis cache → publishes the event to Pub/Sub for live browsers. Postgres is the source of truth; Redis is the speed layer.
