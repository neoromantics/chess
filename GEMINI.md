# Gemini Mandates: neoromantics Chess Platform

## Distributed Microservices (5-pod architecture)
- **Service Boundaries**: The platform is composed of five independent services: `gateway`, `user`, `game`, `matchmaker`, and `worker`.
- **Event-Driven Core**: Strictly use Event Sourcing via Redis Streams for game state mutations. Synchronous state modification and distributed Redis locks are strictly prohibited.
- **Gateway as Entrance**: All external traffic (HTTP/HTTPS, WebSockets) must pass through the `gateway`. It handles auth validation, reverse-proxying to `user`, and WebSocket-to-Command translation.
- **Stateless Gateway**: The `gateway` holds zero game state. It PSUBSCRIBEs to event patterns and delivers to local clients.
- **Authoritative Game Service**: The `game` service is the sole arbiter of game state. It consumes Commands from Redis Streams, validates them, and emits authoritative Events.

## Notification delivery contract
- **Events are facts**: Services emit events to `game:events` (durable stream) and publish to `game.evt.*` (realtime Pub/Sub).
- **Commands are intents**: Gateways append client actions to `game:commands`.
- **Gateway fan-out**: The `gateway` demultiplexes Redis Pub/Sub messages to WebSocket clients.

## Scaling & Resilience
- **Independent Scaling**: Scale services based on their specific profile (e.g., `worker` on CPU, `gateway` on memory/connections).
- **Durable Streams**: Use Redis Streams with Consumer Groups for all inter-service commands and events.
- **Optimistic Concurrency**: Use Postgres versioning/MVCC instead of Redis locks for database consistency.


## Engineering Standards
- Pure Go Search: Keep pkg/core zero-dependency and high-performance. It is the core IP of the platform.
- Service Purity: Remove all GUI terminology from the backend. It is a headless data manager.
- No Emojis: All documentation and log messages must use professional, high-signal technical tone without emojis.
- Kubernetes-Native GitOps: The canonical deployment uses Kubernetes (k3s) managed via Kustomize. Ad-hoc bash scripts are prohibited for production. Docker Compose is strictly for local dev reference.
- Security First: Passwords and tokens must be stored in Kubernetes Secrets. The platform must be served over HTTPS.
- Database Discipline: Postgres only — no SQLite branch. Schema is managed manually or via external tools. Queries live in pkg/db/queries/ and regenerate pkg/db/gen/ via just db-generate.
- neoromantics Branding: Ensure all module paths (github.com/neoromantics/chess) and metadata reflect the neoromantics identity.
