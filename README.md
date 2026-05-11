# Chess Engine & GUI

A modern chess engine and web-based platform written in Go and Vue 3 (TypeScript).

## 🚀 Enterprise Ready
This platform is architected for commercial scale, ready to serve thousands of concurrent players.
- **User Accounts**: Secure signup and login with Bcrypt and JWT.
- **Multi-Game Support**: Manage multiple sessions with sub-millisecond state transitions.
- **Kubernetes Native**: Includes foundational manifests in `deploy/k8s` for cloud-scale horizontal scaling.
- **Dockerized**: Optimized multi-stage production builds for minimal image size.
- **Responsive UI**: Professional TypeScript Vue 3 SPA for a smooth cross-device experience.

## Features
- **Engine**: Bitboard-based (0x88) engine with alpha-beta, quiescence, and tapered evaluation.
- **Web UI**: Modern Vue 3 / Vite SPA with full TypeScript support.
- **Analysis**: Real-time move assessment and hints.
- **History**: Permanent storage for users and sessions (SQLite).

## Project Structure
```
.
├── cmd/chess/          # Main entry point
├── pkg/
│   ├── api/            # Multi-session web server & auth handlers
│   ├── auth/           # JWT & Password security
│   ├── core/           # Pure chess logic
│   ├── db/             # User persistence (SQLite)
│   ├── game/           # Game state management
│   └── uci/            # UCI protocol
├── frontend/           # Vue 3 TypeScript SPA
└── Dockerfile          # Multi-stage production build
```

## Quick Start (Docker)
```bash
docker-compose up -d
```
Visit `http://localhost:8080`.

## Development
To run in dev mode with Hot Reloading:
```bash
just dev
```

## Repository
[github.com/neoromantics/chess](https://github.com/neoromantics/chess)

## License
MIT
