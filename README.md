# neoromantics Chess Platform

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## 🏛 Distributed Architecture
This platform is engineered as a high-performance microservices ecosystem:
- **API Orchestrator (Go)**: A lightweight, event-driven hub that manages WebSockets, authentication, and game state.
- **Engine Worker Pool (Go)**: CPU-intensive move calculations and game analysis offloaded to dedicated worker nodes.
- **Message Broker (Redis)**: Facilitates asynchronous communication between services via Pub/Sub and task queues.
- **Persistent Storage (PostgreSQL)**: Durable storage for user profiles, game history, and analysis records.

## 🚀 Key Features
- **Authoritative Engine**: Backend automatically schedules and manages engine turns, ensuring game integrity.
- **Reactive Analysis**: Real-time move assessments (Brilliant, Blunder, etc.) and hints streamed via event-driven WebSockets.
- **Multi-User Scale**: Horizontal scaling support for both the API gateway and the calculation worker pool.
- **Headless Backend**: Pure data/model management with a decoupled, reactive frontend terminal.

## 🛠 Quick Start (Docker)
The entire stack is containerized for professional environment parity.

```bash
# Build and launch the full distributed stack
just up
```
Visit `http://localhost:8080`.

## 📦 Operations
| Command | Description |
|---------|-------------|
| `just up` | Build and start API, Worker, Redis, and DB |
| `just logs-api` | Watch the API orchestrator logs |
| `just logs-worker` | Watch the engine calculation nodes |
| `just build` | Production-ready local build (Go + embedded frontend) |
| `just down` | Stop all services |
| `just reset` | Fully wipe the environment and restart |

## 🏗 Project Structure
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

## 🛠 Development
For rapid host-native development (concurrent Go/Vite):
```bash
just dev
```

## 🔗 Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## 📄 License
MIT
