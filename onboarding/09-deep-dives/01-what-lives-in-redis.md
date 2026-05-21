# 9.1 What actually lives in Redis here

A grounded answer to "Redis is a cache — but a cache of what?". The "If Redis dies" column is the whole point of the design: almost everything is reconstructible from Postgres or explicitly ephemeral, which is what lets us run one Redis instance without a replica.

| Key | Redis type | Holds | If Redis dies |
|---|---|---|---|
| `game:state:{id}` | String (JSON, ~1–3 KB) | Write-through cache of the `games` row | Next read falls through to Postgres; cache repopulates |
| `game:lock:{id}` | String + TTL (`SETNX`) | Per-game distributed mutex; value is a random token, released via Lua | Two pods could double-process the same move |
| `clock:{id}` | Hash | `whiteMs`, `blackMs`, `lastMoveAt`, `runningSide` — fields updated independently | Live clocks lose subsecond accuracy until next snapshot |
| `clock:fallschedule` | Sorted Set (score = unix deadline ms, member = game ID) | Priority queue of "which game flags next" — one `ZRangeByScore` per sweeper tick instead of scanning every game | Flag detection falls back to per-game polling |
| `mm:queue:{tc}` | Sorted Set (score = rating, member = user ID) | Matchmaking queue per time control — range queries find opponents within ±50 → ±400 rating | Queued users get dropped; they re-queue |
| `mm:joined:{tc}` | Hash (user → unix timestamp) | Per-user join time, backing the wait-time histogram and the 10s engine-fallback trigger | Wait metric goes blind for ~10s |
| `mm:leader` | String + TTL | Leader-election token for the matchmaker pairing loop | Leaderless gap of a few seconds, then another pod takes over |
| `engine:requests` / `engine:results` | Streams + consumer groups | Engine search dispatch and results, durable + at-least-once | Pending searches survive restarts; in-flight stays delivered |
| `game:commands` | Stream + consumer group | Cross-service intent dispatch (new game, join queue) | Pending intents replay on consumer restart |
| `game.evt.{id}` / `user.evt.{id}` | Pub/Sub channels | Ephemeral live-update fan-out to WebSocket clients | Browser misses frames; SPA re-syncs via `GET /api/state` on reconnect |
| `game:thinking:{id}` | String + short TTL | "Engine is thinking on this game" flag, so a mid-search browser refresh still shows the spinner | UI briefly drops the spinner until the result arrives |
| `temp:state:{id}` / `temp:session:{anon_id}` | String | Anonymous-play game state — **no Postgres counterpart**, 10-min sliding TTL | Anonymous games in flight are lost (accepted tradeoff — that's why we don't write them to PG up front) |

---

← [`README.md`](README.md) · Next: [`02-redis-data-structures.md`](02-redis-data-structures.md)
