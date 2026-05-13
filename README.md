# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## 🏗 Distributed Architecture
Six independent microservices coordinate via **Event Sourcing** and **Redis Streams**, ensuring zero-lock consistency and horizontal scalability.

- **gateway** (Go) - Stateless HTTP/WS entrance. Handles JWT validation, command translation, and fan-out.
- **user-service** (Go) - Owns identities, profiles, and authentication.
- **game-service** (Go) - Authoritative domain arbiter. Consumes Commands, emits authoritative Events.
- **matchmaker** (Go) - Manages queues and pairing logic asynchronously.
- **rating-updater** (Go) - Asynchronously updates Glicko-2 ratings after game completion.
- **engine-worker** (Go) - Pure CPU calculation nodes consuming search tasks from Redis Streams.

## 🚀 Key Invariants
- **Sequential Command Processing**: All state mutations (moves, resigns) are processed via Redis Streams; no distributed locks required.
- **Authoritative Backend**: The `game-service` is the sole source of truth for move validation and clocks.
- **Durable Tasking**: Calculation nodes use acknowledged streams with consumer groups for crash recovery.
- **Optimistic Concurrency**: Database consistency is managed via Postgres MVCC.

## 🛠 Operations
| Command | Description |
|---------|-------------|
| `just up` | Start the entire microservices stack locally (Docker Compose) |
| `just build` | Build all production binaries and the Vue frontend |
| `just deploy-prod` | Deploy the 6-pod fleet to k3s using Kustomize |
| `just logs-gateway` | Watch the primary entrance logs |
| `just logs-game` | Watch the authoritative game logic logs |

### Production Deployment
The platform uses **GitHub Actions** with a **Self-Hosted Runner** to deploy directly to the k3s cluster. Manifests in `infra/kustomize` are rendered and applied locally on the VM without requiring exposed inbound ports or SSH keys. Secrets are managed via a local `.env` and injected into the cluster manifests during deployment.

## 🗺 Roadmap
- ✅ **6-Pod Microservices Fleet**: Successfully transitioned from legacy monolith.
- ✅ **Event-Sourced Core**: Eliminated locks in favor of sequential streams.
- ⬜ **OpenTelemetry**: Trace commands across the distributed fleet.
- ⬜ **Spectator Mode**: Read-only WebSocket subscriptions for public games.
- ⬜ **KEDA Autoscaling**: Scale workers based on real-time stream depth.
- ⬜ **Anti-Cheat**: Asynchronous engine correlation scans over move history.

## 📂 Repository Structure
```
.
├── cmd/                # Microservice Entry Points
├── pkg/                # Shared Domain Logic & Packages
├── infra/              # K8s Manifests, Docker Compose, SQLC Config
├── frontend/           # Vue 3 + TypeScript SPA
└── pkg/db/schema.sql   # Authoritative Postgres Schema
```

## 📜 License
MIT
