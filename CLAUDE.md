# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A production multiplayer chess platform deployed to `https://vcm-50800.vm.duke.edu` on k3s with Traefik + Let's Encrypt. **Every change must survive multi-replica deployment, rolling restarts, and HTTPS reverse-proxying** — "works locally" is not sufficient.

## High-level architecture

Six Go services, one Postgres, one Redis. All cross-service comms via Redis. All six services build from a single multi-stage Dockerfile into one image; the binary to run is selected by the deployment's `command:`.

**This is not a fully event-sourced platform** despite some early docs framing it that way. The Streams layer is intentionally narrow — see the **Streams-vs-HTTP rule** below.

```
                       Browser (Vue 3 SPA, embedded into the gateway binary)
                                       │
                                       ▼  HTTPS + WSS
              ┌──────────────────── gateway ────────────────────┐
              │ JWT auth, HTTP routing, WebSocket fan-out,      │
              │ reverse-proxy to user/game services, intent     │
              │ dispatch (Commands) to matchmaker/game.         │
              └──┬─────────────┬─────────────┬───────────────┬──┘
                 ▼             ▼             ▼               ▼
            user-service  game-service  matchmaker      engine-worker
            (auth, PG)    (auth game    (queue +        (CPU search,
                          state, PG)     pairing)        Redis Streams
                                                         consumer group)
                 │             │             │               │
                 └──── PG ─────┘             └───── Redis ───┘
                       │
                  rating-updater (Glicko-2 on game finish, leaderless ticker)
```

### Service responsibilities

- **`gateway` (cmd/gateway)** — Stateless HTTP/WS entry. Validates JWT via `auth.Middleware`, reverse-proxies `/api/auth/`, `/api/user/`, `/api/users/`, `/api/state`, `/api/games`, `/api/invites/` to the right service; handles intent dispatch (`POST /api/games/new`, `POST /api/matchmaking/{join,leave}`) by appending Commands to the `game:commands` stream. **Frontend SPA is embedded only here** via `go:embed all:dist`. The hub runs Redis `PSUBSCRIBE game.evt.*` and `user.evt.*` to fan messages out to locally-attached WebSocket clients. Two WS endpoints: `/ws?game_id=X` (per-game stream) and `/ws/user` (per-user, for invites/match-found).
- **`user-service` (cmd/user)** — Owns identities. Routes: signup/login/logout/me/profile/password/stats and `/api/users/search`. Reads/writes `users` table. Validates JWTs.
- **`game-service` (cmd/game)** — Authoritative game state machine. Consumes the `game:commands` stream (consumer group `game-service-group`), validates moves, mutates the `games` row, emits typed events to `game:events` stream **and** publishes to `game.evt.{id}` Pub/Sub for realtime push. Also serves synchronous HTTP for `/api/state`, `/api/games`, `/api/invites/*`, `/api/replay`. Hosts the invite expiry sweeper goroutine.
- **`matchmaker` (cmd/matchmaker)** — Consumes the same `game:commands` stream (its own group). Maintains per-time-control sorted sets `mm:queue:{tc}`; pairs adjacent entries on a 2s ticker; dispatches a `CreatePvPGame` Command to game-service and publishes `MatchFound` on both users' `user.evt.{id}` channels.
- **`rating-updater` (cmd/rating-updater)** — Reads `game:events` stream (own consumer group). On `GameFinished`, applies Glicko-2 (`pkg/rating`) to both sides and writes back via `db.UpdateUserRating`.
- **`engine-worker` (cmd/engine-worker)** — CPU-bound search. Reads the `engine:requests` stream (`engine-worker-group`), publishes results to the `engine:results` Pub/Sub channel. Runs **exactly one search per pod**: concurrency caps at `runtime.GOMAXPROCS(0)` (cgroup-aware, **not** `runtime.NumCPU()`). To handle more concurrent searches, scale OUT (more pods), don't pack threads. Same binary can run as a UCI CLI via `-uci`; opt-in only, never auto-detected (that bug bit us — every k8s pod tty-check failed and silently EOF'd).

## Wire-protocol contracts (read before adding new events)

Three Redis "channels" with different durability semantics. Don't conflate them.

| Channel | Type | Durability | Purpose |
|---|---|---|---|
| `game:commands` | Stream + consumer group | Durable, at-least-once via XCLAIM | Intent dispatch from gateway/matchmaker → game-service |
| `game:events` | Stream + consumer group | Durable, replayable | Facts emitted by game-service → rating-updater, audit |
| `engine:requests` | Stream + consumer group | Durable | Search work → engine-worker pool |
| `engine:results` | Pub/Sub channel | Ephemeral | Worker → game-service result fan-in |
| `game.evt.{id}` | Pub/Sub channel | Ephemeral | game-service → gateway hub → per-game WS clients |
| `user.evt.{id}` | Pub/Sub channel | Ephemeral | any service → gateway hub → per-user WS clients |

**Two delivery tiers, never mix them:**
- Ephemeral events (moves, hints, clock ticks): Pub/Sub only. Reconnecting clients re-sync via `GET /api/state`.
- Durable events (invites, match-found, game-end): Postgres row first, **then** publish to `user.evt.{id}`. Reconnecting clients fetch outstanding via REST (e.g. `GET /api/invites/pending`).

All Command/Event types live in `pkg/eventbus/eventbus.go`. Adding a new type is additive; renaming an existing one breaks the wire.

## Key invariants

- **Streams vs HTTP rule.** Redis Streams are used for **(1) CPU-asymmetric workloads** (engine search dispatch + result delivery on `engine:requests` / `engine:results`) and **(2) cross-service intent** (matchmaker pairing on `game:commands`). Everything else — single-game mutations, invites, profile changes — uses synchronous HTTP through the gateway. **Do not put a user-initiated chess action behind a Stream**; the SPA expects each button to round-trip a new `StateJSON`. See cmd/game/handlers.go for the pattern.
- **Per-game lock (Redis `game:lock:{id}`, SETNX + token + Lua release).** Every code path that reads-then-writes a game's row must hold this lock for the duration. With N replicas of game-service consuming the same Redis Stream, the round-robin delivery does NOT partition by game_id; two MakeMove commands for the same game could otherwise race. See `acquireGameLock` in cmd/game/lock.go.
- **Gateway injects `?user_id=N` into proxied requests.** Downstream services trust this query param and do not re-validate JWTs. See `injectAuthedUser` in cmd/gateway/main.go. Letting the frontend supply `user_id` would let any caller read anyone's games.
- **Per-game authorization at every game-keyed endpoint.** `userOwnsGame(uid, rec)` is the predicate; non-participants get 404 (not 403) so existence doesn't leak. The WS upgrade gate pre-flights /api/state with the user_id injected to enforce the same check.
- **Only gateway and user-service get `JWT_SECRET`** (see infra/deploy.yaml). Other services don't validate tokens; they're behind the gateway.
- **Frontend is embedded only in the gateway binary** (`cmd/gateway/dist/` via `go:embed`). Other services that need a built asset (e.g. game-service's replay JSON) compose with the gateway, which substitutes templates.
- **`pkg/core` is zero-dependency, pure Go.** The chess engine search is the core IP; do not introduce deps there.
- **Postgres is durable truth; Redis is the hot cache.** (When the cache layer lands — currently in progress.) Reads go Redis-first with PG fallback; writes go write-through. Don't store game state in process memory — that breaks multi-replica.

## Common operations

There is **no Justfile** despite some lingering doc references. Direct commands only:

| What | Command |
|---|---|
| Format check (CI gate) | `gofmt -l .` — must be empty |
| Lint | `golangci-lint run --config infra/.golangci.yml` |
| Tests | `go test -v ./pkg/...` (CI only runs `pkg/`, not `cmd/`) |
| Run one test | `go test -run TestSingleLeader ./pkg/leader/...` |
| Build everything | `go build ./...` |
| Build a single service | `go build -o /tmp/gateway ./cmd/gateway` |
| Regenerate sqlc | `sqlc generate -f infra/sqlc.yaml` (config is **not** at repo root) |
| Frontend build | `cd frontend && npm run build` (runs both `build:main` and `build:replay`) |
| UCI smoke (matches CI) | `printf 'uci\nposition startpos\ngo depth 4\nquit\n' \| ./chess-worker -uci` |

**Backend-only build needs a dummy frontend dir** (the gateway's `go:embed` requires `cmd/gateway/dist/*` to exist):
```bash
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html
```
CI does this in the backend job to avoid an npm round-trip when only Go changed.

## Database & schema

- Schema lives in **one file**: `pkg/db/schema.sql`. There is no `migrations/` directory.
- `OpenPostgres` applies the schema on every service boot under a Postgres advisory lock (`schemaLockID`), so racing replicas serialize the apply safely. The schema is idempotent (`CREATE TABLE IF NOT EXISTS`).
- Editing the schema: prefer additive `ADD COLUMN IF NOT EXISTS`. Column drops or renames need a deliberate one-off applied via `kubectl exec` against the live DB, since there's no migration runner.
- After editing `pkg/db/queries/queries.sql`, run `sqlc generate -f infra/sqlc.yaml` to regenerate `pkg/db/gen/*`. Never hand-edit generated code.

## Secrets & deployment

- **Secrets live in k3s**, owned by the cluster. Bootstrap a fresh cluster with `./infra/bootstrap-secrets.sh` (random openssl-generated values). Rotate via `kubectl edit secret chess-secrets -n chess` then `kubectl rollout restart …`.
- **CI never sees prod secrets.** The deploy job is `kubectl apply -k infra/` + `kubectl rollout restart`. The self-hosted runner runs on the VM.
- All manifests live in `infra/deploy.yaml` (a single file with all six services + ingress + PVCs + ConfigMaps). Kustomize at `infra/kustomization.yaml` sets `namespace: chess`.
- **Rotating Postgres credentials requires also wiping `chess-db-pvc`**, since Postgres only honors `POSTGRES_USER`/`PASSWORD` on the first init of the data dir.

## Production debugging cheatsheet

```bash
# Switch namespace once per shell so we stop passing -n
kubectl config set-context --current --namespace=chess

# Pod state across all services
kubectl get pods

# Why a service is crash-looping (gets the LAST exit's logs)
kubectl logs -l app=chess-<service> --tail=80 --previous

# Live logs of a specific service
kubectl logs -l app=chess-gateway --tail=80 -f

# Verify env var injection on a Deployment
kubectl get deploy chess-gateway -o jsonpath='{.spec.template.spec.containers[0].env[*].name}'
```

Common failure modes and where to look:
- `502 Bad Gateway` on `/api/auth/*` — user-service crash-looping or its Service has no endpoints
- All Postgres-touching services crash-looping with `28P01` — credential drift; usually means the chess-db PVC was initialized with different credentials than `chess-secrets` now holds. Fix: wipe PVC + redeploy
- Worker stuck in `Completed` — pre-fix this was `runtime.NumCPU()` oversubscribing or tty-auto-detect entering UCI mode. Both are fixed; if it returns, something in cmd/engine-worker/main.go is exiting cleanly without an error
- 401 "unauthorized" everywhere after signup works — gateway is missing `JWT_SECRET` env, so its `loadSecret()` falls back to an ephemeral random key

## Things explicitly left as follow-ups

Real gaps surfaced during debugging or load reasoning:

**Production hardening (queued, prioritized):**
- **PGBouncer** in front of Postgres. Current connection-pool math: 6 services × 2 replicas × 25 max conns = 300 vs PG's default 100. Sustained load → `too many connections`. Drop-in fix via `edoburu/pgbouncer` deployment + `DATABASE_URL` rewrite.
- **Redis Sentinel** (3 sentinels + primary + replica). Single Redis today = full-platform SPOF. AOF helps durability, doesn't help failover.
- **Redis hot cache** for game state (`game:state:{id}` hash). Eliminates the PG round-trip on `/api/state` and the GetGame inside every mutation. Drops read latency ~10×; halves PG load.
- **KEDA** on `engine:requests` stream length as the engine-worker HPA signal (currently CPU utilization, which is a proxy).
- **PG read replicas** for ListGames / search / replay queries.

**Product / chess features:**
- Server-authoritative clocks for PvP (the `Thinking` flag is now a Redis sentinel key; clocks need their own per-game keys)
- Matchmaker uses naïve `ZRange 0 2`; needs expanding rating window over time
- `rating-updater` has minimal Glicko-2 wiring; verify against the reference paper's worked example before trusting it

**Architecture review (queued for discussion):**
- The 6-pod split may be over-engineered for current scale. user-service is largely a thin auth wrapper that could fold into the gateway. matchmaker + rating-updater are < 200 lines of consumer-loop code each. Worth revisiting whether 3 pods (gateway+user merged, game, engine-worker) plus matchmaker/rating as goroutines would be more honest. Defer until the hardening items above land.
