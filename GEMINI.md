# Gemini Mandates: neoromantics Chess Platform

## Distributed Microservices (3-pod platform: api, engine-worker, infra)
- Three pods, not five. New "logical services" (matchmaking, invite sweeper, rating updater, notifications, clock manager) live as leader-elected goroutines inside api via pkg/leader — not as separate pods. The carve-out trigger for a pod split is a measurable scaling profile / failure domain difference, not "separation of concerns."
- Decoupled Engine Hub: All CPU-intensive search resides in cmd/engine-worker. The API server remains a lightweight orchestrator. Worker concurrency = runtime.GOMAXPROCS(0) (cgroup-aware); scale OUT (more pods) not IN (more threads per pod).
- Event-Driven Communication: Use pkg/bus (Redis pub/sub + Streams + distributed lock) for all cross-service communication. Channel naming via bus.GameEventChannel(id) / bus.UserEventChannel(id) helpers.
- WebSocket fan-out is Redis-driven: every API pod PSUBSCRIBEs game.evt.* and user.evt.*, demultiplexes to locally-attached WS clients. The Hub is a connection registry plus a pub/sub bridge — never a state store.
- Two WS channels per signed-in user: /ws/game?game_id=... (game events) and /ws/user (invites, match-found, friend events).
- Reactive WebSocket Events: Structured envelopes ({type, payload}). Renaming a type is a wire-protocol break; only add new types.
- Authoritative Backend: The backend is the sole source of truth for the game lifecycle. The frontend is a reactive terminal.

## Notification delivery contract
Two delivery tiers, never mix them:
- Realtime (ephemeral): move broadcasts, hints, assessments, clock sync. Redis pub/sub only. Reconnect re-syncs via /api/state.
- Durable (must-not-lose): invites, match-found, game-end. Postgres row FIRST, then publish for live push. Reconnect fetches outstanding via REST (/api/invites/pending, etc.).
Idempotency keys enforced on state-mutating POSTs via Idempotency-Key header.

## Scaling & Resilience
- Horizontally Scalable & 100% Stateless: API Pods hold ZERO in-memory game state. gameEntry is per-request, hydrated from Postgres.
- Redis as the Operational Backbone: pub/sub (game + user channels), Streams (durable engine queue), SET-NX leases (game lock, leader election), KV (thinking flag, presence). AOF on (appendfsync everysec).
- Fail-Safe Engine Processing: every StreamAdd call is monitored by a leader-elected janitor using XCLAIM to recover stale tasks if a worker crashes mid-search.
- Distributed Clocks: authoritative timers stored in Redis gameclock:{id} and managed by a leader-elected clock-manager. Reconnection grace period (60s) pauses clocks authoritative on the backend.
- HPA: api on CPU+memory (2–8), engine-worker on CPU (2–8). PDBs minAvailable=1 on both.

## Engineering Standards
- Pure Go Search: Keep pkg/core zero-dependency and high-performance. It is the core IP of the platform.
- Service Purity: Remove all GUI terminology from the backend. It is a headless data manager.
- No Emojis: All documentation and log messages must use professional, high-signal technical tone without emojis.
- Kubernetes-Native GitOps: The canonical deployment uses Kubernetes (k3s) managed via Kustomize. Ad-hoc bash scripts are prohibited for production. Docker Compose is strictly for local dev reference.
- Security First: Passwords and tokens must be stored in Kubernetes Secrets. The platform must be served over HTTPS.
- Database Discipline: Postgres only — no SQLite branch. Schema is managed manually or via external tools. Queries live in pkg/db/queries/ and regenerate pkg/db/gen/ via just db-generate.
- neoromantics Branding: Ensure all module paths (github.com/neoromantics/chess) and metadata reflect the neoromantics identity.
