# Gemini Mandates: Chess Platform

## 🏗 Architectural Foundation
- **Modular Monolith**: Maintain the Go package structure strictly. Business logic belongs in `pkg/core`. API/HTTP logic belongs in `pkg/api`.
- **Pure Go Core**: Keep `pkg/core` free of external dependencies. This ensures the engine remains fast, portable, and easy to test.
- **TypeScript First**: All frontend code must use TypeScript. Explicit interfaces in `frontend/src/types.ts` are the source of truth for backend communication.
- **Stateless Auth**: Use JWT for user sessions to ensure the backend can scale horizontally (K8s ready).
- **SQLite Persistence**: Use the pure-Go SQLite driver for all database operations to maintain containerization simplicity.

## 🚀 Deployment Standards
- **Docker First**: The `Dockerfile` is the primary deployment artifact. Ensure multi-stage builds remain optimized.
- **Env Awareness**: Backend must read configuration from environment variables (`PORT`, `DB_PATH`, `JWT_SECRET`).
- **CORS Support**: Maintain CORS middleware in `pkg/api/gui.go` to support decoupled frontend/backend hosting.

## 🛠 Development Workflow
- **One-Button Dev**: `just dev` is the canonical way to develop. It handles concurrent Go/Vite execution.
- **Embedded Assets**: Production builds must embed frontend assets into the Go binary for single-file portability.
- **Type Safety**: Run `npm run build` (which triggers `vue-tsc`) before Go compilation to catch frontend errors early.
