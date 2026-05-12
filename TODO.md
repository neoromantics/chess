# Platform Roadmap — TODO

Tracks the chess-as-a-platform rewrite. Phases ship as standalone commits;
each is independently mergeable so production keeps working between them.

Status legend: ✅ shipped · 🚧 in progress · ⬜ not started

---

## Phase 1 — Foundation ✅

Multi-replica safety, schema for everything to come, operational hardening.

- ✅ Schema migration 000003 (dual white_user_id/black_user_id, time_control, rated, result, Glicko-2 ratings, invites table)
- ✅ Hub rewrite for cross-pod WS fan-out (game.evt.* / user.evt.* PSUBSCRIBE on every API pod)
- ✅ Per-user WebSocket channel /ws/user with ownership-gated upgrade
- ✅ pkg/leader — Redis-singleton election (SET NX EX + Lua release + renew loop)
- ✅ Engine runtime.NumCPU() → runtime.GOMAXPROCS(0) (respects cgroup)
- ✅ Engine timeout safety net (clears thinking flag in 2*movetime + 3s)
- ✅ HPA + PDB + Redis AOF + worker Guaranteed QoS in kustomize
- ✅ Docs (README, DEPLOY, GEMINI) + memories refreshed

---

## Phase 2 — Direct invites ✅

User-to-user challenges, durable via Postgres, live via Redis pub/sub.

- ✅ Invite handlers: POST /api/invites/send, /{id}/accept|decline|cancel, GET /api/invites/pending, GET /api/users/search
- ✅ Leader-elected invite-sweeper (30s cadence, single-trip UPDATE … RETURNING for expiry)
- ✅ Invite + user-events Pinia stores wired through /ws/user
- ✅ Invites view + navbar badge + autocomplete
- ✅ Verified end-to-end (frontend build, two-tab smoke test)

---

## Phase 3 — Matchmaking + Glicko-2 ratings ✅

Open-queue PvP that doesn't require knowing your opponent's username.

- ✅ POST /api/matchmaking/join — adds caller to mm:queue:{time_control} sorted set, score = rating. Returns 202.
- ✅ POST /api/matchmaking/leave
- ✅ Leader-elected matchmaker pairing sweep:
  - Walks each mm:queue:* sorted set
  - Expanding rating window (start ±50)
  - On match: atomic ZREM both, create game, publish match_found
- ✅ Glicko-2 implementation in pkg/rating (pure functions, table-tested)
- ✅ Leader-elected rating-updater listening on game.finished:
  - For rated games only, computes new ratings for both sides
  - Writes via UpdateUserRating (atomic increments + new rating/rd/volatility)
  - Publishes rating_updated on each user's channel
- ✅ Result detection: when Game.Status() flips terminal, set result on the games row (1-0 / 0-1 / 1/2-1/2).
- ✅ Frontend: Play tab with time-control picker + "Find game" button; match-found toast + auto-redirect to /game/{id}.
- ✅ Profile view exposes current rating + W/L/D record.

---

## Phase 4 — PvP polish ✅

Final polish for competitive play.

- ✅ Server-authoritative clocks. Each game has a Redis hash gameclock:{id}. Server deducts elapsed from the moving side, broadcasts fresh clock on game.evt.{id}.
- ✅ Flag-fall detection. Leader-elected clock-manager scans active_games set.
- ✅ Resign: POST /api/games/{id}/resign
- ✅ Draw offers: POST /api/games/{id}/offer-draw, /accept-draw, /decline-draw.
- ✅ Takeback requests (casual games only): same mechanism as draw.
- ✅ Disconnect grace period. If a player's WS drops mid-game, pause their clock for up to 60s while they reconnect.
- ✅ Frontend: clock display, resign button, draw/takeback offer UI.

---

## Phase 5 — Hardening 🚧

Production resilience the platform will need at any non-trivial scale.

- ✅ Redis Streams for engine queue + consumer groups + XCLAIM. Replaces the legacy BLPOP pattern with at-least-once delivery and visibility timeout.
- ⬜ Idempotency keys enforced on every state-mutating POST (Idempotency-Key header → idem:{user}:{key} -> response in Redis with 24h TTL).
- ⬜ Prometheus metrics end-to-end: request count/latency, WS connections, queue depth, rating updater lag, invite sweeper rate, leader-election holder identity.
- ⬜ OpenTelemetry tracing spans across api → bus → worker → bus → api.
- ⬜ Redis Sentinel (3 replicas, automatic failover) in deploy/kustomize/base/resources.yaml.
- ⬜ KEDA ScaledObject on engine-worker queue length so we scale on demand directly rather than CPU-proxy.
- ⬜ Drop migration: 000004_drop_legacy_user_id — remove the legacy games.user_id column.
- ⬜ Backups. pg_dump to S3-compatible storage daily.

---

## Phase 6+ — Beyond v1 (parking lot)

Captured to avoid losing the thread.

- ⬜ Chat per game (one channel per game.evt.{id}, persisted to PG for replay)
- ⬜ Friend / follow graph + presence broadcast to friends
- ⬜ Tournament engine (Swiss + arena formats)
- ⬜ Spectator mode (read-only WS subscription to any public game)
- ⬜ Account avatars / S3 upload pipeline
- ⬜ Multi-region replication (regional Redis + NATS JetStream)
- ⬜ Anti-cheat heuristics (engine correlation scan over move history)
