# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A production multiplayer chess platform deployed to `https://vcm-50800.vm.duke.edu` on k3s with Traefik + Let's Encrypt. **Every change must survive multi-replica deployment, rolling restarts, and HTTPS reverse-proxying** — "works locally" is not sufficient.

## High-level architecture

**Three Go services**, one Postgres, one Redis. All cross-service comms via Redis. All three services build from a single multi-stage Dockerfile into one image; the binary to run is selected by the deployment's `command:`.

We were briefly six pods (gateway, user-service, game-service, matchmaker, rating-updater, engine-worker). The split was over-engineered for this scale and most of our bugs were wire-protocol drift across the extra boundaries. Consolidated 6→3 in commits c413513…43d5f8b: user-service folded into gateway, matchmaker + rating-updater folded into game-service. **engine-worker stays separate** because CPU-bound search has a genuinely different scaling profile (HPA on queue depth, can scale out wide).

**This is not a fully event-sourced platform** despite some early docs framing it that way. The Streams layer is intentionally narrow — see the **Streams-vs-HTTP rule** below.

```
                  Browser (Vue 3 SPA, embedded into the gateway binary)
                                  │
                                  ▼  HTTPS + WSS
        ┌────────────────────── gateway ──────────────────────┐
        │ JWT auth, signup/login/profile/search,              │
        │ HTTP routing, WebSocket fan-out (PSUBSCRIBE), intent│
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

### Service responsibilities

- **`gateway` (cmd/gateway)** — Stateless HTTP/WS entry. Validates JWT via `auth.Middleware`, serves the auth + profile surface directly (signup, login, change-password, /api/user/*, /api/users/search), reverse-proxies game endpoints to game-service with `?user_id=N` injection. For the anonymous temp-game surface, mints a `chess-anon` HttpOnly cookie on first hit and proxies `/api/temp/*` with `?anon_id=<uuid>` injected by `injectAnonID` (cookie-as-identity, mirrors the JWT-as-identity pattern). Handles intent dispatch (`POST /api/games/new`, `POST /api/matchmaking/{join,leave}`) by appending Commands to the `game:commands` stream. **Frontend SPA is embedded only here** via `go:embed all:dist`. The hub runs Redis `PSUBSCRIBE game.evt.*` and `user.evt.*` to fan messages out to locally-attached WebSocket clients. Two WS endpoints: `/ws?game_id=X` (per-game stream — branches inside `handleWSGame` on the `temp-` prefix; durable IDs use the JWT path, temp IDs use the cookie path) and `/ws/user` (per-user, for invites/match-found). Opens its own PG pool for the auth + profile reads. Auth surface is hardened: per-IP token-bucket rate limiter (`authLim` 12/min, `signupLim` 6/hr, `probeLim` 10 burst @1/2s for check-username), per-route `http.MaxBytesReader` body caps, and a shared bounded `*http.Transport` reused by both the reverse proxy and explicit upstream calls (`gw.upstream`) so a hung game-service can't OOM the gateway. WS upgrade enforces same-origin + env-driven `ALLOWED_WS_ORIGINS` allow-list (CSWSH defense) in `cmd/gateway/ws.go:checkWSOrigin`.
- **`game-service` (cmd/game)** — Authoritative game state machine + everything else that touches game state. Sync HTTP for the SPA contract: `GET /api/games` (list), `DELETE /api/games/{id}`, and the per-game verbs nested under `/api/games/{id}/<verb>` (`state`, `move`, `resign`, `new`, `undo`, `set_players`, `set_position`, `hint`, `replay`, `pgn`, `load_pgn`, `analyze`, `visibility`, `can_watch`, `draw_*`, `takeback_*`, `rematch_*`), plus `/api/invites/*`, plus the anonymous temp-game surface `/api/temp/*` (Redis-only, 10-minute sliding TTL — see `cmd/game/temp.go`) and the internal `/api/temp/upgrade` the gateway calls during signup. Stream consumers: `game:commands` (game-service-group) for engine-translated MakeMove + JoinQueue/LeaveQueue, `engine:results` for engine-worker outputs (branches on `resp.Context` for `move` / `hint` / `assess`, then on `Metadata["temp"]` to route results to either `CmdMakeMove` or `applyTempEngineMove`). Goroutines started at boot: invite expiry sweeper (30s ticker), matchmaker pairing loop (2s ticker, Redis-leader-elected via `mm:leader`), clock flag-fall sweeper (500ms ticker), Glicko-2 rating updater (consumes `game:events` for `GameFinished`). Every read-modify-write on `games` rows goes through the per-game Redis lock and the hot cache; the same `game:lock:{id}` lock also serializes temp-game mutations. Source layout: `cmd/game/main.go` is boot only (~190 lines); the read surface lives in `state.go`, command-stream consumer + new-game/PvP/MakeMove in `commands.go`, and engine-result fan-in in `engine_results.go`. Test helpers (`panicStore` + `gameStore` in-memory composer, `newTestService`) live in `testhelpers_test.go` and back the `lock_test.go` / `handle_move_test.go` suites. **Note:** `/api/touch` and `/api/touch_move` from the 0.x platform stay deleted — touch-move is now a client-side session toggle. See `pkg/wire/CONTRACT.md` and `ROADMAP.md`.
- **`engine-worker` (cmd/engine-worker)** — CPU-bound search. Reads the `engine:requests` stream (`engine-worker-group`), publishes results to the `engine:results` stream. Runs **exactly one search per pod**: concurrency caps at `runtime.GOMAXPROCS(0)` (cgroup-aware, **not** `runtime.NumCPU()`). To handle more concurrent searches, scale OUT (more pods), don't pack threads. Same binary can run as a UCI CLI via `-uci`; opt-in only, never auto-detected (that bug bit us — every k8s pod tty-check failed and silently EOF'd).

## Wire-protocol contracts (read before adding new events)

**See `pkg/wire/CONTRACT.md` for the canonical source of truth** — every endpoint, every event type, every payload shape, with both the backend constant and the frontend listener. The doc is normative; this section is the high-level summary.

Three Redis "channels" with different durability semantics. Don't conflate them.

| Channel | Type | Durability | Purpose |
|---|---|---|---|
| `game:commands` | Stream + consumer group | Durable, at-least-once via XCLAIM | Intent dispatch from gateway/matchmaker → game-service |
| `game:events` | Stream + consumer group | Durable, replayable | Facts emitted by game-service → rating-updater, audit |
| `engine:requests` | Stream + consumer group | Durable | Search work → engine-worker pool |
| `engine:results` | Stream + consumer group | Durable, at-least-once | Worker → game-service result fan-in (was Pub/Sub; promoted in fa76c2f after a production loss-of-result incident) |
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
- **Only gateway gets `JWT_SECRET`** (see infra/deploy.yaml). Gateway is the only place JWTs are signed/verified; game-service and engine-worker trust the gateway-injected `?user_id=N` query param instead.
- **Frontend is embedded only in the gateway binary** (`cmd/gateway/dist/` via `go:embed`). Other services that need a built asset (e.g. game-service's replay JSON) compose with the gateway, which substitutes templates.
- **`pkg/core` is zero-dependency, pure Go.** The chess engine search is the core IP; do not introduce deps there.
- **Postgres is durable truth; Redis is the hot cache.** Game rows live in `game:state:{id}` as a Redis hash, write-through to PG. Reads go Redis-first with PG fallback. Don't store game state in process memory — that breaks multi-replica. See `cmd/game/cache.go`.
- **Any HTTP middleware that wraps `http.ResponseWriter` MUST forward `Hijack()` and `Flush()`.** `gorilla/websocket` needs `Hijack()` to take over the TCP connection during Upgrade; SSE/streaming responses need `Flush()`. Forgetting this is silent at compile time and breaks every WebSocket handler at runtime with `websocket: response does not implement http.Hijacker`. See `pkg/metrics/metrics.go:statusRecorder` for the canonical pattern.
- **Gateway hub uses per-channel SUBSCRIBE, not PSUBSCRIBE.** When the first local WS client for game G connects, the hub `SUBSCRIBE`s `game.evt.G`; on the last disconnect it `UNSUBSCRIBE`s. This keeps cross-pod fan-out cost proportional to "pods with a live subscriber" instead of "every pod gets every event". A regression to PSUBSCRIBE wildcards would silently work but blow up the bandwidth bill at scale. See `cmd/gateway/hub.go`.
- **Gateway pulls the matchmaker rating from the DB, never from the request body.** `handleJoinQueue` looks up `dbUser.Rating` so a 1200 player can't queue as 2400. The SPA's `api.joinQueue` no longer sends a rating field.
- **Register HTTP routes with Go 1.22 `Method /path/{id}` patterns.** The metrics middleware (`pkg/metrics/metrics.go:HTTPMiddleware`) labels by `r.Pattern`; anything that arrives without a Pattern gets bucketed as `<unknown>` to keep cardinality bounded. A `<unknown>` spike on the Grafana panel = someone registered a handler without a Method+Pattern declaration. Prefix handlers (`mux.Handle("/api/invites/", …)`) also populate `r.Pattern` with the registered prefix, so they're fine — what's NOT fine is hand-routed dispatchers that wrap a handler without going through ServeMux's pattern matching.
- **Confirm dialogs use the singleton Pinia modal, not `window.confirm`.** Mounted once in `App.vue` via `<ConfirmModal />`; everywhere else call `useConfirmStore().ask({title, message, confirmLabel, danger}) → Promise<boolean>`. Browser-native confirm doesn't match the theme, can't be styled, and gets blocked on some embedded contexts. See `frontend/src/components/ConfirmModal.vue` + `frontend/src/stores/confirm.ts`.

## Common operations

There is **no Justfile** despite some lingering doc references. Direct commands only:

| What | Command |
|---|---|
| Format check (CI gate) | `gofmt -l .` — must be empty |
| Lint | `golangci-lint run --config infra/.golangci.yml` |
| Tests | `go test -v ./pkg/... ./cmd/...` (CI runs both) |
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
- Editing the schema: prefer additive `ADD COLUMN IF NOT EXISTS`. Column drops or renames need to be deliberate and idempotent — the canonical pattern is `ALTER TABLE … DROP COLUMN IF EXISTS …;` appended after the `CREATE TABLE`, so the next boot's schema-apply removes the column on any cluster that still has it. Only do this when nothing reads or writes the column anywhere in the code (grep first).
- After editing `pkg/db/queries/queries.sql`, run `sqlc generate -f infra/sqlc.yaml` to regenerate `pkg/db/gen/*`. Never hand-edit generated code.

## Secrets & deployment

- **Secrets live in k3s**, owned by the cluster. Bootstrap a fresh cluster with `./infra/bootstrap-secrets.sh` (random openssl-generated values). Rotate via `kubectl edit secret chess-secrets -n chess` then `kubectl rollout restart …`.
- **CI never sees prod secrets.** The deploy job is `kubectl apply -k infra/` + `kubectl rollout restart`. The self-hosted runner runs on the VM.
- All manifests live in `infra/deploy.yaml` (a single file with all three services + ingress + PVCs). Kustomize at `infra/kustomization.yaml` sets `namespace: chess`.
- **Postgres `max_connections` is raised to 500** via `args: ["-c", "max_connections=500"]` on the chess-db Deployment. Engine-worker doesn't open a PG pool, so the live ceiling is `(gateway HPA max 8) + (game-service HPA max 6) = 14 pods × MaxOpenConns=30 = 420 client conns`, leaving ~80 for autovacuum + superuser reservations. Per-pod sizing is env-tunable via `PG_MAX_OPEN_CONNS` / `PG_MAX_IDLE_CONNS`. We tried PGBouncer but burned four deploy cycles on broken Docker Hub tags; tuning PG itself is the simpler win at this scale.
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
- **Every WebSocket call fails silently** ("doesn't update live", "refresh required", browser DevTools shows WS connections in red) — almost always an HTTP middleware wrapping `http.ResponseWriter` without forwarding `Hijack()`. Gateway logs show `websocket: response does not implement http.Hijacker`. Fix: every wrapper in the middleware chain must implement `http.Hijacker` (and `http.Flusher`). The metrics middleware bit us this way once; see `pkg/metrics/metrics.go:statusRecorder`.
- `502 Bad Gateway` on `/api/auth/*` — gateway pod crash-looping (it owns auth now, not a separate user-service)
- All Postgres-touching services crash-looping with `28P01` — credential drift; usually means the chess-db PVC was initialized with different credentials than `chess-secrets` now holds. Fix: wipe PVC + redeploy
- Worker stuck in `Completed` — pre-fix this was `runtime.NumCPU()` oversubscribing or tty-auto-detect entering UCI mode. Both are fixed; if it returns, something in cmd/engine-worker/main.go is exiting cleanly without an error
- 401 "unauthorized" everywhere after signup works — gateway is missing `JWT_SECRET` env, so its `loadSecret()` falls back to an ephemeral random key
- Engine plays but its move never reaches the SPA — historically because `engine:results` was Pub/Sub (lossy under restart); now a durable Stream. If it regresses, check the consumer-group reader in `cmd/game/engine_results.go:listenToEngineResults`
- Image pull failures pinning a public pgbouncer / bitnami / ... tag — Docker Hub's tag publishing is unreliable. Either pin a verified digest, switch images, or in our case (low scale) skip the dependency entirely. See the in-line comment under "PGBOUNCER" in `infra/deploy.yaml`

## Things explicitly left as follow-ups

Full status board: `ROADMAP.md`. The high-impact open items at a glance:

**Product:**
- _(no open product items right now — move-assessment phases 1–3 all shipped.)_

**Production hardening:**
- **Redis Sentinel** — single Redis is a full-platform SPOF; AOF gives durability but not failover. Requires a multi-node cluster to be meaningful.
- _(KEDA on `engine:requests` pending depth + CPU backstop shipped 2026-05-16; see `infra/keda.yaml`.)_
- **PG read replicas** for ListGames / search / replay queries.

**Already shipped (kept here so they're easy to find again):**
- Spectator mode (`/watch/:id`, `is_public` column, `userMayRead` predicate).
- Prometheus + Grafana via `infra/observability.yaml` (Grafana at `/grafana/`, admin password in `chess-secrets:GRAFANA_ADMIN_PASSWORD`).
- Wire-contract drift check (`infra/check-wire-contract.sh` + CI job).
- Business metrics (`MovesAppliedTotal`, `EngineSearchDuration`, `MatchmakerQueueDepth`, `MatchmakerWaitSeconds`, `GamesFinishedTotal`, `RatingUpdateDuration`) + matching Grafana panels.
- Auth surface hardening: per-IP rate limiter + body-size caps on signup/login/profile/check-username + WS origin allow-list (env `ALLOWED_WS_ORIGINS`).
- Change-password UI on `/profile`; signup username minimum bumped to 5 chars.
- In-theme confirm modal singleton replacing every `window.confirm` call site.

See `ROADMAP.md` "Scale design notes" for the 10k-pair-scale walk-through (per-channel SUBSCRIBE, PG indices, env-tunable pool already shipped; the remaining items are deliberately deferred with reasons).
