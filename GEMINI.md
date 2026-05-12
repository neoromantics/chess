# Gemini Mandates: neoromantics Chess Platform

## Distributed Microservices (3-pod platform: api, engine-worker, infra)
- Three pods, not five. New "logical services" (matchmaking, invite sweeper, rating updater, notifications) live as leader-elected goroutines inside `api` via `pkg/leader` — not as separate pods. The carve-out trigger for a pod split is a measurable scaling profile / failure domain difference, not "separation of concerns."
- Decoupled Engine Hub: All CPU-intensive search resides in `cmd/engine-worker`. The API server remains a lightweight orchestrator. Worker concurrency = `runtime.GOMAXPROCS(0)` (cgroup-aware); scale OUT (more pods) not IN (more threads per pod).
- Event-Driven Communication: Use `pkg/bus` (Redis pub/sub + BLPOP queue + distributed lock) for all cross-service communication. Channel naming via `bus.GameEventChannel(id)` / `bus.UserEventChannel(id)` helpers.
- WebSocket fan-out is Redis-driven: every API pod `PSUBSCRIBE`s `game.evt.*` and `user.evt.*`, demultiplexes to locally-attached WS clients. The Hub is a connection registry plus a pub/sub bridge — never a state store.
- Two WS channels per signed-in user: `/ws/game?game_id=...` (game events) and `/ws/user` (invites, match-found, friend events).
- Reactive WebSocket Events: Structured envelopes (`{type, payload}`). Renaming a type is a wire-protocol break; only add new types.
- Authoritative Backend: The backend is the sole source of truth for the game lifecycle. The frontend is a reactive terminal.

## Notification delivery contract (read this before adding any new event)
Two delivery tiers, never mix them:
- **Realtime (ephemeral):** move broadcasts, hints, assessments. Redis pub/sub only. Reconnect re-syncs via `/api/state`.
- **Durable (must-not-lose):** invites, match-found, game-end. Postgres row FIRST, **then** publish for live push. Reconnect fetches outstanding via REST (`/api/invites/pending`, etc.).
Idempotency keys on every state-mutating POST so retries don't double-create.

## Scaling & Resilience
- Horizontally Scalable & 100% Stateless: API Pods hold ZERO in-memory game state. `gameEntry` is per-request, hydrated from Postgres.
- Redis as the Operational Backbone: pub/sub (game + user channels), BLPOP queue (engine work), SET-NX leases (game lock, leader election), KV (thinking flag). AOF on (`appendfsync everysec`); Sentinel/HA is Phase-5 hardening.
- Fail-Safe Engine Processing: every Enqueue call is paired with `scheduleEngineTimeout(2*movetime + 3s)` that clears the `thinking` flag and broadcasts fresh state if the worker dies mid-search. Phase 5 replaces this with Redis Streams + XCLAIM.
- HPA: api on CPU+memory (2–8), engine-worker on CPU (2–8). PDBs `minAvailable=1` on both. KEDA-on-queue-depth is the Phase-5 upgrade.
- Asynchronous Analysis: Analysis features (Hints, Assess) are asynchronous by design. API returns 202 Accepted and streams results via WebSockets.

## Engineering Standards
- Pure Go Search: Keep pkg/core zero-dependency and high-performance. It is the core IP of the platform.
- Service Purity: Remove all GUI terminology from the backend. It is a headless data manager.
- Kubernetes-Native GitOps: The canonical deployment uses Kubernetes (k3s) managed via `kustomize`. Ad-hoc bash scripts are strictly prohibited for production deployments. Docker Compose is maintained strictly as a local development reference, NOT for VM deployments.
- Security First: Passwords and tokens must be stored in Kubernetes Secrets. We advocate for proper Secret Management Services (e.g., SealedSecrets, SOPS, or External Secrets Operator) rather than raw `.env` files for long-term production. The platform must be served over HTTPS.
- Libraries over Reinvention: Infrastructure concerns (rate limiting, distributed locking, terminal/browser detection, WS heartbeats, schema migrations, query type-safety) MUST use maintained libraries — `golang.org/x/time/rate`, Lua-script CAS releases via go-redis, `golang.org/x/term`, `pkg/browser`, gorilla heartbeat pattern, `golang-migrate`, `sqlc`. The carve-out is pkg/core (engine) and pkg/uci (protocol parser) which are the product itself.
- Database Discipline: Postgres only — no SQLite branch. Schema lives in `pkg/db/migrations/` (append-only, versioned, applied at boot via embedded `golang-migrate`). Queries live in `pkg/db/queries/` and regenerate `pkg/db/gen/` via `just db-generate`. Never hand-edit generated code.
- neoromantics Branding: Ensure all module paths (github.com/neoromantics/chess) and metadata reflect the neoromantics identity.
