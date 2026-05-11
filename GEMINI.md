# Gemini Mandates: Chess Platform

## 🏗 Architectural Foundation (Commercial Scale)
- **Modular Monolith to Microservices**: Prepare for horizontal scaling by keeping the API stateless. Engine search must eventually move to a Worker Pool pattern.
- **Stateless API Layer**: API pods must not rely on local in-memory state. Use Redis for active sessions and PostgreSQL for persistence in production.
- **Pure Go Core**: Keep `pkg/core` free of external dependencies to maximize search performance on worker nodes.
- **TypeScript First**: Strict typing in `frontend/src/types.ts` is the contract between decoupled frontend and backend services.
- **Anti-Cheat Readiness**: Design all game logic to support telemetry collection (move times, focus events) for future statistical fraud detection.

## 🚀 Deployment & Ops (Enterprise Grade)
- **Kubernetes Native**: Use the manifests in `deploy/k8s` as the source of truth for cloud topology.
- **Observability**: Implement structured `slog` logging and prepare for OpenTelemetry tracing across the API-to-Worker boundary.
- **Env-Driven Config**: Never hardcode secrets. Use K8s Secrets mapped to environment variables (`JWT_SECRET`, `DB_URL`).
- **Health-Checks**: Always maintain the `/health` endpoint for liveness/readiness probes.

## 🛠 Development Workflow
- **One-Button Dev**: `just dev` is the canonical way to develop. It handles concurrent Go/Vite execution.
- **Embedded Assets**: Production builds must embed frontend assets into the Go binary for single-file portability.
- **Type Safety**: Run `npm run build` (which triggers `vue-tsc`) before Go compilation to catch frontend errors early.
