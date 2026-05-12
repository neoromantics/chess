# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## Distributed Architecture
Three pods, three responsibilities, every cross-pod handoff over Redis:

- **api** (Go, HPA 2-8) - Stateless HTTP + WebSocket entry. Auth, routing, game lifecycle, matchmaking, rating updates. Holds zero game state in memory.
- **engine-worker** (Go, HPA 2-8) - CPU-bound search. Pulls jobs via Redis Streams (durable), publishes results via Redis pub/sub.
- **Redis** - Operational backbone. Pub/sub, Streams (engine queue), distributed locks, leader election, and transient state (thinking flag, authoritative clocks).
- **Postgres** - Durable source of truth. sqlc-generated queries, golang-migrate for schema management.

## Key Invariants
- **No in-memory game state.** gameEntry is built per-request from Postgres.
- **All cross-pod fan-out via Redis pub/sub.** The WS Hub is a pub/sub-driven connection registry.
- **Durable Engine Queue.** Uses Redis Streams with consumer groups and auto-recovery (XCLAIM).
- **Authoritative Clocks.** Timers managed authoritative on the backend via Redis and a leader-elected clock-manager.
- **Idempotency.** state-mutating requests (moves, resignations) are protected by Idempotency-Key headers.

## Operations
| Command | Description |
|---------|-------------|
| just up | Build and start API, Worker, Redis, and DB locally (Docker Compose) |
| just logs-api | Watch the API orchestrator logs |
| just logs-worker | Watch the engine calculation nodes |
| just build | Production-ready local build (Go + embedded frontend) |
| just db-generate | Regenerate sqlc code (run after editing SQL) |
| just deploy-prod | Deploy to k3s cluster using Kustomize |

## Project Structure
```
.
├── cmd/
│   ├── chess/                # API Server (Orchestrator)
│   └── engine-worker/        # Distributed Calculation Node
├── pkg/
│   ├── api/                  # HTTP + WebSocket entry, Hub, Metrics, Idempotency
│   ├── bus/                  # Redis pub/sub, Streams, Distributed locks
│   ├── leader/               # Redis-backed singleton leader election
│   ├── rating/               # Glicko-2 competitive rating implementation
│   ├── core/                 # Pure chess engine (search & eval)
│   ├── db/                   # sqlc + golang-migrate (Postgres only)
│   ├── game/                 # Authoritative game logic
│   └── auth/                 # JWT/bcrypt authentication
├── frontend/                 # Vue 3 + TypeScript SPA
└── deploy/kustomize/         # Production k3s manifests
```

## Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## License
MIT
