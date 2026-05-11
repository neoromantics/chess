# Chess Engine & GUI

A modern chess engine and web-based platform written in Go and Vue 3 (TypeScript).

## 🚀 Web Launch Ready
This project has been upgraded from a local tool to a full web platform.
- **User Accounts**: Secure signup and login with Bcrypt and JWT.
- **Multi-Game Support**: Start and manage multiple games in one window.
- **Dockerized**: Ready for containerized deployment (K8s/Cloud).
- **Responsive UI**: Plays perfectly in browsers and on mobile.

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
