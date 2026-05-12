# CLAUDE.md - Development Guide

## Build & Run (Production-Scale)
- **Launch Distributed Stack**: `just up` (API, Worker, Redis, Postgres)
- **Watch Orchestrator Logs**: `just logs-api`
- **Watch Engine Logs**: `just logs-worker`
- **Local Embedded Build**: `just build` (Go binary with embedded frontend)
- **Stop services**: `just down`
- **Reset Environment**: `just reset` (Warning: wipes DB)

## Distributed Architecture
- **API (Orchestrator)**: `pkg/api` handles I/O, WS events, and routes `EngineRequest` to Redis.
- **Worker (Analyzer)**: `cmd/engine-worker` performs Iterative Deepening and returns `EngineResponse`.
- **Event Bus**: `pkg/bus` wraps `go-redis` for cross-service communication.
- **Core Engine**: `pkg/core` contains pure movegen, search, and evaluation.
- **Frontend**: Vue 3 Reactive SPA (`frontend/`) acts as a dumb terminal.

## Engineering Standards
- **Go**: `gofmt` compliant, structured `slog` logging, explicit module path `github.com/neoromantics/chess`.
- **Reactive Flow**: WebSocket events use `{ "type": "...", "payload": { ... } }` format.
- **Non-Blocking**: API handlers must never perform CPU-intensive searches. Always offload to the Worker pool.
- **Authoritative**: Backend manages the game clock and engine turns.

## Command Reference
- `just dev`: Host-native concurrent development.
- `just test`: Run all Go unit tests.
- `just check`: Lint and verify code standards.
