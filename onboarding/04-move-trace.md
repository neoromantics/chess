# 4. A move, from button to board

Here is what happens when the user clicks a square and a piece moves. This trace touches almost everything from [`03-mental-jumps/`](03-mental-jumps/).

1. **SPA** sends `POST /api/games/{id}/move` with body `{"from":"e2","to":"e4"}`. Cookie `token=<JWT>` rides along.
2. **Gateway** middleware reads the cookie, validates the JWT signature against `JWT_SECRET`, extracts user ID, sets `r.Header["X-User-ID"] = "42"`, strips any incoming `X-User-ID` the client may have tried to spoof.
3. **Gateway** reverse-proxies via its shared bounded `*http.Transport` to `game-service`. Prometheus middleware records start time.
4. **game-service** receives the request, reads `X-User-ID=42`, looks up the game via `acquireGameLock("game:lock:{id}")` → cache read on `game:state:{id}` → fall through to Postgres if cache miss.
5. Validates: does user 42 own this game? Is it their turn? Is the move legal? (`userOwnsGame` returns 404 to non-participants — *not* 403, because that would leak existence of the game.)
6. Applies the move via `pkg/core`, updates clock fields, persists the new game state: **Postgres write first, then Redis cache update**. Publishes a `game.evt.{id}` Pub/Sub message with the new state.
7. **Gateway hub** has a `SUBSCRIBE game.evt.42` running (the first WebSocket client for game 42 caused it to subscribe; the last disconnect will cause `UNSUBSCRIBE`). The hub receives the message and fans it out to every local WebSocket connected to game 42.
8. **SPA** receives the WS frame, replaces the board state, re-renders, plays the move sound.
9. If the opponent is an engine: `game-service` *also* writes an `engine:requests` Stream entry. `engine-worker` (one of N pods) reads it via its consumer group, runs the search, writes the result to `engine:results`. `game-service` reads `engine:results` and loops back to step 5 with the engine's move.

Every single step has a failure mode. The lock prevents two replicas from both applying a move ([§3.2](03-mental-jumps/02-distributed-locks.md)). The "Postgres first, then cache" order prevents stale reads after a crash. The Pub/Sub fan-out means a reconnecting client can miss a frame but recover via `GET /api/state` ([§3.4](03-mental-jumps/04-streams-vs-pubsub.md)). The Stream for engine results means a `game-service` restart doesn't lose the engine's reply.

[`../docs/invariants.md`](../docs/invariants.md) calls out each of these as an invariant.

---

← [`README.md`](README.md) · Next: [`05-doing-work.md`](05-doing-work.md)
