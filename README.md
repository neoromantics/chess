# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## Distributed Architecture
Six independent microservices, zero-lock consistency via Event Sourcing:

- **gateway** (Go) - Stateless HTTP/WS entrance. Handles JWT validation, command translation, and fan-out.
- **user-service** (Go) - Owns identities, profiles, and authentication.
- **game-service** (Go) - Authoritative domain arbiter. Consumes Commands, emits authoritative Events.
- **matchmaker** (Go) - Manages queues and pairing logic asynchronously.
- **rating-updater** (Go) - Asynchronously updates Glicko-2 ratings after game completion.
- **engine-worker** (Go) - Pure CPU calculation nodes consuming search tasks from Redis Streams.

## Key Invariants
- **Strict Event Sourcing.** All state mutations are processed sequentially via Redis Streams; no distributed locks.
- **Authoritative Backend.** The `game-service` is the sole source of truth for move validation and clocks.
- **Asynchronous Analysis.** Engine results are published to the event bus and automatically applied or broadcast.
- **Horizontally Scalable.** Every pod is stateless and interacts via the Redis operational backbone.

## Operations
| Command | Description |
|---------|-------------|
| just up | Start the entire microservices stack locally (Docker Compose) |
| just logs-gateway | Watch the Gateway/Entrance logs |
| just logs-game | Watch the authoritative Game Service logs |
| just build | Build all production binaries and frontend |
| just deploy-prod | Deploy the fleet to k3s using Kustomize |

## Project Structure
```
.
├── cmd/
│   ├── gateway/            # Entry Point & WebSocket Hub
│   ├── user/               # User Management Microservice
│   ├── game/               # Authoritative Game Logic Microservice
│   ├── matchmaker/         # Queueing & Pairing Microservice
│   ├── rating-updater/     # Asynchronous Glicko-2 Updater
│   └── engine-worker/      # Distributed Calculation Node
├── pkg/
│   ├── eventbus/           # Redis Streams Command/Event protocol
│   ├── core/               # Pure chess engine (search & eval)
│   ├── db/                 # SQLC + Postgres (Authoritative Persistence)
│   ├── game/               # Domain logic (Move rules, Status)
│   ├── auth/               # JWT/bcrypt authentication utilities
│   └── rating/             # Glicko-2 implementation
├── frontend/               # Vue 3 + TypeScript SPA
└── deploy/kustomize/       # Production k3s manifests
```

## Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## License
MIT
