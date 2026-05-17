# neoromantics Chess

**Live:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(k3s + Traefik + Let's Encrypt on a Duke VCM)

A multiplayer chess platform built in Go + Vue 3 + Postgres + Redis, deployed to Kubernetes. Three backend services, one durable database, one in-memory bus. Frontend builds into the same binary as the gateway so a single image carries everything except the engine.

## Architecture in one picture

```
           Browser (Vue 3 SPA, embedded in gateway binary)
                                │ HTTPS / WSS
                                ▼
                        ┌──── gateway ────┐
                        │ auth, profiles, │
                        │ HTTP routing,   │
                        │ WS fan-out      │
                        └─┬───────────────┘
                          │ sync HTTP for game endpoints
                          │ Redis Streams for intent dispatch
                          ▼
                  ┌── game-service ──┐    ┌─ engine-worker ──┐
                  │ moves, invites,  │◀──▶│ CPU search       │
                  │ matchmaking,     │    │ HPA on queue     │
                  │ Glicko ratings   │    │ depth            │
                  └───┬──────────────┘    └──────────────────┘
                      │ shared state
                      ▼
                ┌── Postgres ──┐ ┌── Redis ─────┐
                │ durable      │ │ hot cache,   │
                │ truth        │ │ streams,     │
                └──────────────┘ │ pub/sub,     │
                                 │ locks        │
                                 └──────────────┘
```

Three pods scale horizontally; engine-worker has its own autoscaling profile because chess search is CPU-asymmetric. Everything else (profiles, invites, matchmaking, ratings) shares the same data and lives in the gateway or game-service binary.

## What works today

- **Auth + profile.** Sign up / log in / log out with JWT cookies; profile + stats + password change; live Glicko-2 rating chip that updates over WS when a rated game finalizes.
- **Anonymous play.** Land on `/`, pick "Play vs Engine" without signing in — you get a 10-minute sliding-TTL temp game. If you sign up mid-game, the gateway carries the temp game over into a durable row owned by your new account.
- **Engine play.** Pick per-side think time, change it mid-game, swap human ↔ engine on either color, even let two engines play each other.
- **Human vs human.** Invite by username, or **Find Game** matchmaking on two time controls (3+0 Blitz, 10+0 Rapid — trimmed from ten on 2026-05-16 because the fan-out diluted the queue at our playerbase). Expanding rating-window pairing (±50 grows to ±400). Board auto-flips for the black player.
- **Server-authoritative clocks.** `clock:{id}` Redis hash + 500ms flag-fall sweeper; SPA extrapolates locally between snapshots for smooth ticks.
- **Live everything.** Move + last-move highlight + thinking spinner + clock all push over WebSocket; no refresh during a game.
- **Draw / takeback.** Both round-trip via short-lived SETNX-protected offers; takeback is PvP-casual only, draw is PvP-only.
- **Resign + replay.** Resign at any time; finished games replay frame-by-frame.
- **Board editor + PGN.** Engine games can be set up from any FEN (`/api/set_position`), downloaded as PGN, or replaced by pasting a PGN. PGN encoder/decoder is round-trip tested.
- **Move assessment.** Click "Analyze game" — backend dispatches a per-ply engine search; per-ply ✓ / ★ / ? markers stream into the move list over WS and persist on the game row so a reopen shows the verdicts without re-running the engine.
- **Spectator mode.** Owner flips `is_public` on a game; anyone can watch live at `/watch/:id` with read-only WS subscription.
- **Observability.** Prometheus + Grafana at `/grafana/`; business metrics (moves/sec, engine search p95, queue depth, matchmaker wait p95, games finished/min, Glicko-2 update p95) wired alongside HTTP/WS metrics.
- **Touch-move rule.** Client-side session toggle in the SidePanel; enforces FIDE 4.3 when ON.
- **Glicko-2 ratings.** Numerically verified against the paper's worked example (`pkg/rating/glicko2_test.go`).

## What's missing (see [ROADMAP.md](ROADMAP.md) for the full list)

- **PG read replicas** for `ListGames` / search / replay queries.
- **Frontend hosted off the gateway** (CDN-served `dist/`) so SPA tweaks don't rebuild the gateway image.
- **Redis HA via Sentinel** — deferred until the cluster has a second node.
- **Postgres HA via Patroni** — same reasoning: single VM, single failure domain.

## Wire-protocol contract

Every HTTP endpoint, WebSocket event type, and JSON payload shape is enumerated in **`pkg/wire/CONTRACT.md`**. The convention is to edit that doc in the same commit as any new wire surface; backend constants and frontend listeners reference it.

## Key invariants

- **Per-game lock.** Every read-modify-write on the `games` table goes through `acquireGameLock` in `cmd/game/lock.go` (Redis SETNX + token + Lua release). Concurrent moves on the same game serialize across replicas.
- **Postgres is durable truth; Redis is the hot cache.** `cmd/game/cache.go` wraps `game:state:{id}` as a write-through hash. Reads hit Redis, fall through to PG on miss.
- **Gateway injects `X-User-ID` (and `X-Anon-ID` for temp games) on proxied requests.** Downstream services trust the gateway-set header; nothing else re-validates the JWT. Only `gateway` gets `JWT_SECRET`.
- **Auth surface is rate-limited and body-capped.** Per-IP token-bucket on signup/login/profile/check-username + `http.MaxBytesReader` per route + a shared bounded upstream `http.Transport` so a hung game-service can't OOM the gateway. WS upgrade enforces same-origin + `ALLOWED_WS_ORIGINS` allow-list.
- **Engine results are durable.** `engine:results` is a Stream with a consumer group, not Pub/Sub. A game-service restart no longer loses an in-flight move.
- **No in-memory game state.** Every request hydrates from PG (via the cache). Multi-replica safety has no shared mutable state.
- **`pkg/core` is zero-dependency Go.** The chess search is the core IP; don't add third-party deps.

See `CLAUDE.md` for the full set of invariants and the Streams-vs-HTTP rule.

## Common operations

There is no Justfile. Direct commands:

| What | Command |
|---|---|
| Format check (CI gates on this) | `gofmt -l .` |
| Vet | `go vet ./...` |
| Tests | `go test -v ./pkg/...` |
| Build everything | `go build ./...` |
| Build one service | `go build -o /tmp/gateway ./cmd/gateway` |
| Frontend build | `cd frontend && npm run build` |
| Regenerate sqlc | `sqlc generate -f infra/sqlc.yaml` |
| UCI smoke | `printf 'uci\ngo depth 4\nquit\n' \| ./chess-worker -uci` |

Backend-only builds need a dummy frontend dir so `go:embed` finds something:
```bash
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html
```

## Deployment

GitHub Actions builds the unified image and pushes to `ghcr.io/neoromantics/chess`. A self-hosted runner on the VM does `kubectl apply -k infra/` + `kubectl rollout restart` for the three Deployments.

### Bootstrap secrets

**Secrets live in k3s, not in CI.** On a fresh cluster, run once on the VM:
```bash
./infra/bootstrap-secrets.sh
```
That creates `chess-secrets` with random Postgres credentials + a JWT signing key. CI never sees these values; rotation is `kubectl edit secret chess-secrets -n chess` + `kubectl rollout restart …`.

Rotating Postgres credentials additionally requires wiping `chess-db-pvc` (Postgres only honors `POSTGRES_USER` / `POSTGRES_PASSWORD` on first init of the data dir).

## Production debugging

```bash
# Set namespace once per shell
kubectl config set-context --current --namespace=chess

# State of the fleet
kubectl get pods

# Why a service is crash-looping (LAST exit's logs)
kubectl logs -l app=chess-<service> --tail=80 --previous

# Live logs
kubectl logs -l app=chess-gateway --tail=80 -f
```

Common failure modes and where to look are in `CLAUDE.md`.

## Repository layout

```
├── cmd/
│   ├── gateway/       # HTTP/WS edge + auth + profiles (absorbed user-service)
│   ├── game/          # Game state + invites + matchmaking + ratings
│   └── engine-worker/ # CPU search, queue consumer
├── pkg/
│   ├── core/          # Pure chess engine; zero deps
│   ├── auth/          # JWT + bcrypt
│   ├── db/            # sqlc-generated types + Postgres impl + schema.sql
│   ├── eventbus/      # Redis Streams + Pub/Sub primitives
│   ├── game/          # Game state machine
│   ├── metrics/       # Prometheus instrumentation
│   ├── pgn/           # PGN encode + decode (Seven-Tag-Roster, SAN replay)
│   ├── rating/        # Glicko-2 (numerically verified against the paper)
│   ├── uci/           # UCI protocol (CLI mode)
│   └── wire/          # CONTRACT.md — the wire-protocol source of truth
├── frontend/          # Vue 3 + TS SPA, embedded into gateway via //go:embed
├── infra/             # k8s manifests, sqlc.yaml, .golangci.yml
└── Dockerfile         # Single multi-stage build, three binaries out
```

## License
MIT
