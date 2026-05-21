# neoromantics Chess

**Live:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(k3s + Traefik + Let's Encrypt on a Duke VCM)

A multiplayer chess platform built in Go + Vue 3 + Postgres + Redis, deployed to Kubernetes. Three backend services, one durable database, one in-memory bus. The Vue 3 SPA is embedded into the gateway binary, so a single image carries the full stack except the engine workers.

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

Three pods scale horizontally; `engine-worker` has its own autoscaling profile because chess search is CPU-asymmetric. Everything else (profiles, invites, matchmaking, ratings) shares the same data and lives in the `gateway` or `game-service` binary.

## What works today

- **Auth + profile.** Sign up / log in / log out with JWT cookies; profile + stats + password change; live Glicko-2 rating chip that updates over WS when a rated game finalizes.
- **Anonymous play.** Land on `/`, pick "Play vs Engine" without signing in — you get a 10-minute sliding-TTL temp game. If you sign up mid-game, the gateway carries the temp game over into a durable row owned by your new account.
- **Engine play.** Pick per-side think time, change it mid-game, swap human ↔ engine on either color, even let two engines play each other.
- **Human vs human.** Invite by username, or **Find Game** matchmaking on two time controls (3+0 Blitz, 10+0 Rapid). Expanding rating-window pairing (±50 grows to ±400). Board auto-flips for the black player.
- **Server-authoritative clocks.** `clock:{id}` Redis hash + 500ms flag-fall sweeper; SPA extrapolates locally between snapshots for smooth ticks.
- **Live everything.** Move + last-move highlight + thinking spinner + clock all push over WebSocket; no refresh during a game.
- **Draw / takeback.** Both round-trip via short-lived SETNX-protected offers; takeback is PvP-casual only, draw is PvP-only.
- **Resign + replay.** Resign at any time; finished games replay frame-by-frame.
- **Board editor + PGN.** Engine games can be set up from any FEN, downloaded as PGN, or replaced by pasting a PGN. PGN encoder/decoder is round-trip tested.
- **Move assessment.** Click "Analyze game" — backend dispatches a per-ply engine search; per-ply ✓ / ★ / ? markers stream into the move list over WS and persist on the game row so a reopen shows the verdicts without re-running the engine.
- **Spectator mode.** Owner flips `is_public` on a game; anyone can watch live at `/watch/:id` with read-only WS subscription.
- **Move-list scrub + fork.** In any finished game, click a SAN span to jump the board to that ply (or use ←/→/Home/End/Esc). "Fork" opens that position in a new engine-game row so you can play hypothetical lines without disturbing the original.
- **Studies.** Save any position or game-as-line to `/study/`. The save-setup button lives in the position editor; "Save as study" lives in the side panel of finished games. Each study renders the start position + the linear move list, with "Play from here" to drop into a fresh game at that FEN.
- **Observability.** Prometheus + Grafana at `/grafana/`; business metrics (moves/sec, engine search p95, queue depth, matchmaker wait p95, games finished/min, Glicko-2 update p95) wired alongside HTTP/WS metrics.
- **Touch-move rule.** Client-side session toggle in the SidePanel; enforces FIDE 4.3 when ON.
- **Glicko-2 ratings.** Numerically verified against the paper's worked example (`pkg/rating/glicko2_test.go`).

## Documentation

- **[CLAUDE.md](CLAUDE.md)** — entry point for Claude Code (and humans). Slim orienting index.
- **[docs/architecture/overview.md](docs/architecture/overview.md)** — services, responsibilities, why we consolidated 6 → 3.
- **[docs/architecture/redis-patterns.md](docs/architecture/redis-patterns.md)** — distributed locks, leader election, streams vs pub/sub.
- **[docs/invariants.md](docs/invariants.md)** — the rules every change must respect.
- **[docs/operations/](docs/operations/)** — dev commands, deployment, debugging, database.
- **[pkg/wire/CONTRACT.md](pkg/wire/CONTRACT.md)** — every HTTP route, WebSocket event, and JSON payload (normative).
- **[docs/roadmap.md](docs/roadmap.md)** — shipped, queued, deferred.

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
├── docs/              # topic-organized documentation
└── Dockerfile         # Single multi-stage build, three binaries out
```

## License
MIT
