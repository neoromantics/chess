# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## Enterprise Distributed Architecture
This platform is engineered as a highly available, strictly consistent microservices ecosystem:
- **API Gateway (Go)**: A strictly stateless, event-driven orchestrator managing WebSockets and Auth. Can be scaled infinitely behind standard load balancers.
- **Engine Worker Pool (Go)**: CPU-intensive search and evaluation offloaded to a horizontally scalable pool of dedicated worker nodes.
- **Redis (State Sync & Concurrency)**: Serves as the operational backbone. Provides distributed locking (preventing race conditions) and cross-pod cache invalidation (solving split-brain state issues).
- **Postgres (Persistence)**: The authoritative durable store for game lifecycles and analysis records.

## Key Features
- **Strictly Consistent State:** Redis-backed distributed locks guarantee absolute consistency across active games, even during concurrent mutations across different K8s nodes.
- **Reactive Cache Hydration:** Pub/Sub cache invalidation ensures that WebSockets instantly stream the latest board state regardless of which pod processes the move.
- **Zero-Downtime GitOps:** Automated CI/CD pipeline building multi-stage immutable containers, seamlessly rolled out to a k3s cluster.
- **Authoritative Headless Engine:** The backend is the sole arbiter of time and legality. The Vue 3 frontend is a decoupled, reactive terminal.

## Quick Start (Docker)
The entire stack is containerized for professional environment parity.

```bash
# Build and launch the full distributed stack
just up
```
Visit http://localhost:8080.

## Operations
| Command | Description |
|---------|-------------|
| just up | Build and start API, Worker, Redis, and DB |
| just logs-api | Watch the API orchestrator logs |
| just logs-worker | Watch the engine calculation nodes |
| just build | Production-ready local build (Go + embedded frontend) |
| just down | Stop all services |
| just reset | Fully wipe the environment and restart |

## Project Structure
```
.
├── cmd/
│   ├── chess/          # API Server (Orchestrator)
│   └── engine-worker/  # Distributed Calculation Node
├── pkg/
│   ├── api/            # Headless API, WebSockets & Event Hub
│   ├── bus/            # Redis Pub/Sub Event Bus
│   ├── core/           # Pure Chess Engine (Search & Eval)
│   ├── db/             # Multi-provider persistence (PG/SQLite)
│   ├── game/           # Authoritative Game Logic
│   └── auth/           # Secure JWT/Bcrypt authentication
├── frontend/           # Vue 3 Reactive SPA
└── deploy/             # Kubernetes & Production Manifests
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
4. Applies the raw manifests in `deploy/k8s.yaml` and performs a zero-downtime rolling restart.

See [DEPLOY.md](DEPLOY.md) for detailed cluster setup instructions.

## Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## License
MIT
