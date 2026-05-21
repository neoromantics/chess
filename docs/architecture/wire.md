# Wire surface (summary)

**See [`pkg/wire/CONTRACT.md`](../../pkg/wire/CONTRACT.md) for the canonical source of truth** — every endpoint, every event type, every payload shape, with both the backend constant and the frontend listener. The doc is normative; this file is the high-level summary.

## Redis channels

Six "channels" with different durability semantics. Don't conflate them.

| Channel | Type | Durability | Purpose |
|---|---|---|---|
| `game:commands` | Stream + consumer group | Durable, at-least-once via XCLAIM | Intent dispatch from gateway/matchmaker → game-service |
| `game:events` | Stream + consumer group | Durable, replayable | Facts emitted by game-service → rating-updater, audit |
| `engine:requests` | Stream + consumer group | Durable | Search work → engine-worker pool |
| `engine:results` | Stream + consumer group | Durable, at-least-once | Worker → game-service result fan-in (was Pub/Sub; promoted in `fa76c2f` after a production loss-of-result incident) |
| `game.evt.{id}` | Pub/Sub channel | Ephemeral | game-service → gateway hub → per-game WS clients |
| `user.evt.{id}` | Pub/Sub channel | Ephemeral | any service → gateway hub → per-user WS clients |

## Two delivery tiers, never mix them

- **Ephemeral events** (moves, hints, clock ticks): Pub/Sub only. Reconnecting clients re-sync via `GET /api/state`.
- **Durable events** (invites, match-found, game-end): Postgres row first, **then** publish to `user.evt.{id}`. Reconnecting clients fetch outstanding via REST (e.g. `GET /api/invites/pending`).

The rationale for which primitive solves which problem is in [`redis-patterns.md`](redis-patterns.md).

## Adding new wire surface

- All Command/Event types live in `pkg/eventbus/eventbus.go`. Adding a new type is additive; renaming an existing one breaks the wire.
- Update [`pkg/wire/CONTRACT.md`](../../pkg/wire/CONTRACT.md) in the same commit. CI (`infra/check-wire-contract.sh`) verifies every WS event listed in CONTRACT Section 3 appears as a literal in both backend Go and frontend TS/Vue.
- When adding an HTTP route, register with Go 1.22 `Method /path/{id}` patterns so `r.Pattern` populates and metrics labels stay bounded.
