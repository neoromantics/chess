# Platform Roadmap — TODO

Tracks the chess-as-a-platform microservices rewrite.

Status legend: ✅ shipped · 🚧 in progress · ⬜ not started

---

## Phase 1-5: Ground-Up Rewrite ✅

Transitioned from legacy monolith to authoritative Event-Driven microservices.

- ✅ Established 6-pod topology (Gateway, User, Game, Matchmaker, Rating, Worker).
- ✅ Event Sourcing Core: Centralized domain logic in `chess-game` with Redis Streams.
- ✅ Eliminated all distributed Redis locks; achieved zero-lock consistency via sequential command processing.
- ✅ Decentralized WebSocket Hub: Multi-pod safe fan-out via Redis Pub/Sub demultiplexing.
- ✅ Autonomous User Management: Auth and Profiles moved to `chess-user`.
- ✅ Durable Engine Tasking: Workers use acknowledged streams with consumer groups.
- ✅ Asynchronous Rating Updates: Dedicated service for Glicko-2 rank persistence.
- ✅ Tooling alignment: Multi-service CI/CD and granular log recipes.

---

## Phase 6+ — Expanding the Platform 🚧

New features built on the high-performance event-driven foundation.

- ⬜ **OpenTelemetry Tracing**: Trace commands/events across Gateway → Game → Worker → Gateway.
- ⬜ **Spectator Mode**: Read-only WebSocket subscription to any public game event stream.
- ⬜ **Persistent In-Game Chat**: New channel per game.evt.{id}, persisted to Postgres.
- ⬜ **Matchmaking Expansion**: Support Swiss and Arena tournament formats.
- ⬜ **KEDA Autoscaling**: Scale workers based on Redis Stream depth rather than CPU proxy.
- ⬜ **Redis Sentinel**: High-availability operational backbone.
- ⬜ **Anti-cheat heuristics**: Asynchronous engine correlation scans over move history.
