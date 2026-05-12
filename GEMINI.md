# Gemini Mandates: neoromantics Chess Platform

## Distributed Microservices (Million-Dollar Architecture)
- Decoupled Engine Hub: All CPU-intensive search and analysis must reside in cmd/engine-worker. The API server must remain a lightweight orchestrator.
- Event-Driven Communication: Use pkg/bus (Redis Pub/Sub) for all cross-service communication. API dispatches engine.request and reacts to engine.response.
- Reactive WebSocket Events: WebSockets must follow the structured event pattern (type, payload). Avoid streaming raw board state; push transient insights (hints, assessments) via specific event types.
- Authoritative Backend: The backend is the sole source of truth for the game lifecycle. The frontend is a reactive terminal that displays state but never dictates engine scheduling.

## Scaling & Resilience
- Horizontally Scalable & 100% Stateless: API Pods must hold ZERO in-memory game state. All game lifecycles and clocks are calculated dynamically via Postgres timestamps. Design all logic to support infinite API replicas without sticky sessions.
- Redis as the Operational Backbone: Use Redis for all cross-pod communication (`ws.broadcast`), task queuing (`BLPOP`), and distributed locking.
- Fail-Safe Processing: Use defer and atomic flags to ensure the system never gets stuck in a Thinking state.
- Asynchronous Analysis: Analysis features (Hints, Assess) are asynchronous by design. API returns 202 Accepted and streams results via WebSockets.

## Engineering Standards
- Pure Go Search: Keep pkg/core zero-dependency and high-performance. It is the core IP of the platform.
- Service Purity: Remove all GUI terminology from the backend. It is a headless data manager.
- Kubernetes-Native GitOps: The canonical deployment uses Kubernetes (k3s) managed via `kustomize`. Ad-hoc bash scripts are strictly prohibited for production deployments. Docker Compose is maintained strictly as a local development reference, NOT for VM deployments.
- Security First: Passwords and tokens must be stored in Kubernetes Secrets. We advocate for proper Secret Management Services (e.g., SealedSecrets, SOPS, or External Secrets Operator) rather than raw `.env` files for long-term production. The platform must be served over HTTPS.
- Libraries over Reinvention: Infrastructure concerns (rate limiting, distributed locking, terminal/browser detection, WS heartbeats, schema migrations, query type-safety) MUST use maintained libraries — `golang.org/x/time/rate`, Lua-script CAS releases via go-redis, `golang.org/x/term`, `pkg/browser`, gorilla heartbeat pattern, `golang-migrate`, `sqlc`. The carve-out is pkg/core (engine) and pkg/uci (protocol parser) which are the product itself.
- Database Discipline: Postgres only — no SQLite branch. Schema lives in `pkg/db/migrations/` (append-only, versioned, applied at boot via embedded `golang-migrate`). Queries live in `pkg/db/queries/` and regenerate `pkg/db/gen/` via `just db-generate`. Never hand-edit generated code.
- neoromantics Branding: Ensure all module paths (github.com/neoromantics/chess) and metadata reflect the neoromantics identity.
