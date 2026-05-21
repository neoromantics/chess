# CLAUDE.md

Guidance for Claude Code (claude.ai/code) working in this repository. Topic-organized depth lives in [`docs/`](docs/); this file is the slim always-loaded index.

## What this is

A production multiplayer chess platform deployed to `https://vcm-50800.vm.duke.edu` on k3s with Traefik + Let's Encrypt. **Every change must survive multi-replica deployment, rolling restarts, and HTTPS reverse-proxying** — "works locally" is not sufficient.

## Architecture in 30 seconds

```
                  Browser (Vue 3 SPA, embedded in gateway binary)
                                  │  HTTPS + WSS
                                  ▼
        ┌────────────────────── gateway ──────────────────────┐
        │ JWT auth, HTTP routing, WS fan-out,                 │
        │ intent dispatch to game-service                     │
        └──────┬──────────────────────────────┬───────────────┘
               │                              │
               ▼                              ▼
         game-service (HPA 2-6)         engine-worker (HPA 2-8)
         · sync HTTP for game state     · CPU-bound search
         · stream consumers             · engine:requests / :results
         · leader-elected singletons    · one search per pod
                │                                ▲
                ▼                                │
              Postgres  ◀── shared cache + bus ── Redis
              (durable truth)                (cache, streams, pub/sub, locks)
```

Three Go services + Postgres + Redis. Single multi-stage Dockerfile produces one image; the binary is selected by the deployment's `command:`. Deep dive: [`docs/architecture/overview.md`](docs/architecture/overview.md).

## Critical invariants (always-on for Claude)

Violating any of these silently corrupts data or opens a security hole. The full list with full explanations is in [`docs/invariants.md`](docs/invariants.md); the subset below is what Claude should know without needing to read further.

- **Streams vs HTTP rule** — Streams only for CPU-asymmetric workloads + cross-service intent. Single-game mutations stay sync HTTP through the gateway. See [`docs/architecture/redis-patterns.md`](docs/architecture/redis-patterns.md).
- **Per-game lock** — Every read-modify-write on a game row holds `game:lock:{id}` (Redis SETNX + token + Lua release). See `cmd/game/lock.go`.
- **Gateway injects `?user_id=N`** — Downstream services trust the gateway-set param. Never accept user IDs from the frontend.
- **Per-game authz returns 404, not 403** — Existence leak avoidance. `userOwnsGame(uid, rec)` is the predicate.
- **Only gateway has `JWT_SECRET`** — Game-service and engine-worker never verify JWTs.
- **`pkg/core` is zero-dependency Go** — Engine search is the core IP; no third-party deps in there.
- **Postgres is truth; Redis is cache** — Write-through. Never in-memory game state (breaks multi-replica).
- **Middleware must forward `Hijack()` and `Flush()`** — WebSocket upgrades and SSE break silently otherwise. See `pkg/metrics/metrics.go:statusRecorder` for the canonical wrapper.

## Where things live

| Topic | File |
|---|---|
| Architecture overview, service responsibilities | [`docs/architecture/overview.md`](docs/architecture/overview.md) |
| Redis patterns (locks, leader election, streams vs pub/sub) | [`docs/architecture/redis-patterns.md`](docs/architecture/redis-patterns.md) |
| Wire surface summary | [`docs/architecture/wire.md`](docs/architecture/wire.md) |
| Wire contract (normative) | [`pkg/wire/CONTRACT.md`](pkg/wire/CONTRACT.md) |
| Full invariants list | [`docs/invariants.md`](docs/invariants.md) |
| Dev commands | [`docs/operations/commands.md`](docs/operations/commands.md) |
| Deployment & secrets | [`docs/operations/deployment.md`](docs/operations/deployment.md) |
| Debugging cheatsheet | [`docs/operations/debugging.md`](docs/operations/debugging.md) |
| Database & sqlc workflow | [`docs/operations/database.md`](docs/operations/database.md) |
| Roadmap & shipped log | [`docs/roadmap.md`](docs/roadmap.md) |

## Critical commands

| What | Command |
|---|---|
| Format (CI gate) | `gofmt -l .` — must be empty |
| Tests | `go test ./pkg/... ./cmd/...` |
| Build everything | `go build ./...` |
| Frontend | `cd frontend && npm run build` |

Full table + pre-commit gate: [`docs/operations/commands.md`](docs/operations/commands.md).

**Backend-only build prerequisite** — gateway's `go:embed all:dist` needs `cmd/gateway/dist/*` to exist:
```bash
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html
```

## Skills

`.claude/skills/` has three Claude Code skills for this repo:

- **`chess-precommit`** — runs the full local CI gate (gofmt, golangci-lint, tests, wire-contract drift, sqlc) before committing.
- **`investigate`** — separates analysis from implementation. Producing a prioritized punch-list first, then shipping one item per explicit "Yes / Ship it / Do it" approval.
- **`update-chess-docs`** — refreshes the right docs after shipping a feature.

## Doing tasks

- Read this file + the relevant `docs/` subsection before suggesting changes that touch invariants.
- The wire contract ([`pkg/wire/CONTRACT.md`](pkg/wire/CONTRACT.md)) is normative — update it in the same commit as any new wire surface. CI (`infra/check-wire-contract.sh`) enforces drift.
- Don't introduce new doc files at the repo root; edit the ones organized in `docs/`. New topics get their own file inside the matching `docs/<area>/` folder.
