# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## Enterprise Distributed Architecture
This platform is engineered as a highly available, strictly consistent microservices ecosystem:
- **API Gateway (Go)**: A strictly 100% stateless, event-driven orchestrator. Holds ZERO game state in memory. Clocks and game state are derived dynamically from Postgres timestamps, allowing infinite scaling behind standard load balancers without sticky sessions.
- **Engine Worker Pool (Go)**: CPU-intensive search and evaluation offloaded to a horizontally scalable pool of dedicated worker nodes reading from Redis `BLPOP` queues.
- **Redis (State Sync & Concurrency)**: Serves as the operational backbone. Provides distributed locking (preventing race conditions) and acts as the global `ws.broadcast` bus, allowing any pod to stream state instantly to all clients.
- **Postgres (Persistence)**: The authoritative durable store for game lifecycles and analysis records.

## Key Features
- **Strictly Consistent State:** Redis-backed distributed locks (token + Lua compare-and-delete release) guarantee absolute consistency across active games, even during concurrent mutations across different K8s nodes.
- **Reactive Stateless Broadcasts:** Redis Pub/Sub (`ws.broadcast`) streams board state from any pod to all WebSocket clients. Heartbeated WS connections cull half-open clients in ~60s.
- **Standardized GitOps:** Automated CI/CD pipeline building multi-stage immutable containers, seamlessly rolled out to a K8s cluster using standard `Kustomize`.
- **Authoritative Headless Engine:** The backend is the sole arbiter of time and legality. The Vue 3 frontend is a decoupled, reactive terminal.
- **Schema-Versioned Persistence:** `sqlc`-generated type-safe queries against Postgres; `golang-migrate` runs embedded SQL migrations on boot from every replica (advisory-locked, so only one applies).

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
│   ├── api/                  # Headless API, WebSockets, middleware (rate limit, recovery, CORS)
│   ├── bus/                  # Redis pub/sub, BLPOP queue, token-based distributed lock
│   ├── core/                 # Pure Chess Engine (Search & Eval) — zero deps
│   ├── db/
│   │   ├── migrations/       # golang-migrate up/down SQL, embedded into the binary
│   │   ├── queries/          # sqlc source-of-truth SQL
│   │   ├── gen/              # sqlc-generated (DO NOT hand-edit — `just db-generate`)
│   │   ├── store.go          # Storage interface
│   │   └── postgres.go       # sqlc-backed implementation + connection pool
│   ├── game/                 # Authoritative Game Logic
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
