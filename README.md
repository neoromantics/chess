# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## Distributed Architecture
Three pods, three responsibilities, every cross-pod handoff over Redis:

- **`api` (Go, HPA 2–8)** — Stateless HTTP + WebSocket entry. Auth, routing, game lifecycle, matchmaking pairing loop (leader-elected goroutine), invite delivery, rating updates (leader-elected goroutine). Holds zero game state in memory; every request fetches fresh from Postgres.
- **`engine-worker` (Go, HPA 2–8)** — CPU-bound search. Pulls jobs via Redis `BLPOP` (exactly-once), publishes results via Redis pub/sub, listens for abort signals. Concurrency per pod = `runtime.GOMAXPROCS(0)` so cgroup CPU limits are respected (a Phase-1 fix — `runtime.NumCPU()` ignores them).
- **Redis** — Pub/sub (game + per-user channels), BLPOP queue (engine work), SET-NX leases (distributed game lock, leader election), KV (thinking flag, presence). AOF persistence (`appendfsync everysec`); Sentinel/HA is a Phase-5 hardening task.
- **Postgres** — Durable truth. sqlc-generated queries, golang-migrate (advisory-locked across replicas).

## Event topology
Two Redis pub/sub keyspaces drive every realtime push:
- `game.evt.{game_id}` — moves, hints, assessments, status changes, game-end. Every API pod `PSUBSCRIBE`s `game.evt.*` and fans out to its locally-attached WebSocket clients on `/ws/game`. This is what makes two players on different pods see each other's moves.
- `user.evt.{user_id}` — invites, match-found, friend events. Fans out the same way to `/ws/user` subscribers.

**Realtime vs durable delivery:**
- Ephemeral events (move broadcasts, clock ticks) live only in pub/sub. Reconnect re-syncs via `GET /api/state`.
- Durable events (invites, match results) write to Postgres first **and** publish for live push. Reconnect fetches outstanding via REST (`/api/invites/pending` etc.), so a user who was offline when invited still sees it.

## Key invariants
- **No in-memory game state.** `gameEntry` is built per-request from Postgres; in-pod `gameRegistry` does not exist.
- **All cross-pod fan-out via Redis pub/sub.** The WS Hub is a pub/sub-driven connection registry — never a source of truth.
- **Engine search is one-per-pod.** Concurrency caps at `GOMAXPROCS(0)`. Scale horizontally (more worker pods), not vertically (more goroutines per pod).
- **Engine requests have a timeout.** API schedules `AfterFunc(2*movetime + 3s)` to clear the `thinking:{id}` flag and broadcast a fresh state if the worker never responds — so a worker crash recovers in one move-time, not the legacy 2-minute eternity.
- **Distributed game lock is correct.** SET-NX with a random token; release runs a Lua compare-and-delete so a slow holder past TTL cannot blow away its successor's lock.

## Schema (post-000003)
- `users` — Glicko-2 rating (`rating`, `rd`, `volatility`) + `wins/losses/draws/games_played` counters. Legacy `elo` column retained until 000004.
- `games` — Dual ownership: `white_user_id`, `black_user_id` (nullable; null = engine plays that side). `time_control`, `rated`, `result` (`*`/`1-0`/`0-1`/`1/2-1/2`). Legacy `user_id` retained for Phase-1 transition.
- `invites` — Direct user-to-user challenges. Status enum (`pending`/`accepted`/`declined`/`expired`/`cancelled`), TTL via `expires_at`, optional `game_id` back-reference once accepted.

## Phased roadmap
| # | Phase | Status |
|---|---|---|
| 1 | Foundation: Redis-driven WS fan-out, schema 000003, leader election, engine resilience | **Done** |
| 2 | Direct invites (user-to-user) | Next |
| 3 | Matchmaking queue + Glicko-2 rating updates | After 2 |
| 4 | PvP polish: server-authoritative clocks, draw/resign/takeback | After 3 |
| 5 | Hardening: Redis Sentinel, KEDA queue-depth HPA, Redis Streams for engine queue, observability | After 4 |

## Quick Start (Docker Compose)
Docker Compose is maintained strictly as a local development reference to ensure parity. It is **not** used for VM or production deployment.


```bash
# Build and launch the full distributed stack
just up
```
Visit http://localhost:8080.

## Operations
| Command | Description |
|---------|-------------|
| just up | Build and start API, Worker, Redis, and DB. Auto-bootstraps `.env` on first run. |
| just logs-api | Watch the API orchestrator logs |
| just logs-worker | Watch the engine calculation nodes |
| just build | Production-ready local build (Go + embedded frontend) |
| just down | Stop all services |
| just reset | Fully wipe environment (containers, `./postgres-data`, `.env`) and restart |
| just db-generate | Regenerate `pkg/db/gen/*` from `pkg/db/queries/*.sql` (run after editing SQL) |
| just secrets-init | Generate a fresh strong `.env` (Postgres creds + JWT) |
| just deploy-prod | `kubectl apply -k deploy/kustomize/overlays/prod` |

## Project Structure
```
.
├── cmd/
│   ├── chess/                # API Server (Orchestrator)
│   └── engine-worker/        # Distributed Calculation Node
├── pkg/
│   ├── api/                  # HTTP + WebSocket entry, middleware, Hub (cross-pod fanout)
│   ├── bus/                  # Redis pub/sub, BLPOP queue, distributed lock, channel helpers
│   ├── leader/               # Redis-backed singleton leader election (matchmaker, sweepers)
│   ├── core/                 # Pure chess engine (search & eval) — zero deps
│   ├── db/
│   │   ├── migrations/       # golang-migrate up/down SQL, embedded into the binary
│   │   ├── queries/          # sqlc source-of-truth SQL
│   │   ├── gen/              # sqlc-generated (DO NOT hand-edit — `just db-generate`)
│   │   ├── store.go          # Storage interface (users, games, invites, ratings)
│   │   └── postgres.go       # sqlc-backed implementation + connection pool
│   ├── game/                 # Authoritative game logic
│   ├── auth/                 # JWT/bcrypt; lazy secret resolution
│   └── uci/                  # UCI protocol parser (stdio mode)
├── frontend/                 # Vue 3 + TypeScript SPA, embedded via //go:embed
├── deploy/kustomize/         # Base + prod overlay for k3s
└── sqlc.yaml                 # sqlc generator config
```

## Development
For local development, we use Docker Compose to run the entire distributed stack:
```bash
just up
```

## Production Deployment
The production deployment uses a **GitOps** methodology. 
Pushing to the `main` branch triggers a GitHub Actions pipeline that:
1. Builds multi-stage Docker images.
2. Pushes the immutable images to `ghcr.io/neoromantics`.
3. Connects to the remote **k3s** Kubernetes cluster via SSH.
4. Applies the manifests using native `Kustomize` (`deploy/kustomize/overlays/prod`) and performs a zero-downtime rollout.

**Note on Secret Management**: For the highest production standard, we advocate migrating from Kustomize's `secretGenerator` to a dedicated Secret Management Service (e.g., SealedSecrets, SOPS, or External Secrets Operator) in the future.
See [DEPLOY.md](DEPLOY.md) for detailed cluster setup instructions.

## Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## License
MIT
