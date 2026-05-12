# Chess Platform

A modern chess engine and web-based platform written in Go and Vue 3 (TypeScript).

## 🚀 Enterprise Ready
This platform is architected for commercial scale, ready to serve thousands of concurrent players.
- **User Accounts**: Secure signup and login with Bcrypt and JWT.
- **Multi-Game Support**: Manage multiple sessions with sub-millisecond state transitions.
- **Kubernetes Native**: Includes foundational manifests in `deploy/k8s` for cloud-scale horizontal scaling.
- **Docker Only**: Pure containerized workflow ensures environment parity from local to cloud.
- **Responsive UI**: Professional TypeScript Vue 3 SPA for a smooth cross-device experience.

## Quick Start (Docker)
The entire platform is containerized for consistency and ease of deployment.

```bash
just up
```
Visit `http://localhost:8080`.

## Operations
| Command | Description |
|---------|-------------|
| `just up` | Build and start all services (API, Worker, DB) |
| `just logs` | View real-time logs from all containers |
| `just status` | Check container health |
| `just down` | Stop all services |
| `just reset` | Wipe database and restart from scratch |

## Features
- **Engine**: Bitboard-based (0x88) engine with alpha-beta, quiescence, and tapered evaluation.
- **Web UI**: Modern Vue 3 / Vite SPA with full TypeScript support.
- **Analysis**: Real-time move assessment and hints.
- **History**: Permanent storage for users and sessions (PostgreSQL).

## Project Structure
```
.
├── cmd/chess/          # Main API server entry point
├── cmd/engine-worker/  # Decoupled engine worker service
├── pkg/
│   ├── api/            # Multi-session web server & auth handlers
│   ├── auth/           # JWT & Password security
│   ├── core/           # Pure chess logic (search & eval)
│   ├── db/             # Abstracted storage layer (Postgres/SQLite)
│   ├── game/           # Game state management
│   └── uci/            # UCI protocol
├── frontend/           # Vue 3 TypeScript SPA
└── Dockerfile          # Multi-stage production build
```

## Development
For rapid frontend/backend coding without containers:
```bash
just dev
```

## Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## License
MIT
