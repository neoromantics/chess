# Roadmap

The single source of truth for what's shipped, what's queued, and what's deferred. Updates land in the same commit as the work itself; `CLAUDE.md` and `README.md` point here.

Legend: ✅ shipped · 🚧 in progress · ⬜ queued · 🟰 deliberately deferred

---

## Foundations (shipped)

The session that produced this roadmap shipped ~40 commits restructuring the platform. Everything below is in production on `main`.

### Wire + correctness
- ✅ Per-game lock (Redis SETNX + token + Lua compare-and-delete) gates every read-modify-write on the `games` table. No more silent ply-drops under multi-replica.
- ✅ Per-game authorization at every game-keyed endpoint (`userOwnsGame`); WS upgrade pre-flights `/api/state` so signed-in users can't subscribe to strangers' games.
- ✅ Side-to-move authz on `/api/move` — backend rejects moves from the wrong side; frontend silently no-ops opponent clicks.
- ✅ `engine:results` promoted from Pub/Sub to durable Stream (lost-move incidents eliminated).
- ✅ Schema applied idempotently on every service boot under a Postgres advisory lock; `CREATE TABLE IF NOT EXISTS` throughout.
- ✅ Hot cache for game state via Redis hash `game:state:{id}`, write-through to PG.
- ✅ `pkg/wire/CONTRACT.md` — the canonical wire-protocol doc; every event name and payload shape lives there.

### SPA contract
- ✅ Sync HTTP for every single-game mutation (`/api/move`, `/api/resign`, `/api/new`, `/api/undo`, `/api/touch`, `/api/touch_move`, `/api/set_players`, `/api/load`, `/api/save`, `/api/games/delete`, `/api/hint`, `/api/assess`).
- ✅ Live WebSocket pushes work end-to-end (was broken for hours by a missing `Hijack()` forward in the metrics middleware; lesson lives in CLAUDE.md).
- ✅ Board auto-flips for the black player on PvP load.
- ✅ Last-move highlight (`from`/`to` squares) renders from `state.last_move`.
- ✅ Match-found auto-redirect via `user.evt.{id}` with per-recipient color in the payload.
- ✅ Invite flow end-to-end: send-by-username with autocomplete, accept/decline/cancel, durable PG row + live push, 60s TTL sweeper.
- ✅ Replay viewer for finished games (template substitution in gateway).

### Architecture / ops
- ✅ 6 → 3 pod consolidation: `chess-gateway`, `chess-game-service`, `chess-engine-worker`. `user-service` folded into gateway; `matchmaker` + `rating-updater` folded into game-service as Redis-leader-elected goroutines.
- ✅ HPAs on all three Deployments + PodDisruptionBudgets `minAvailable=1`.
- ✅ Prometheus metrics scaffold (`/metrics` on every service; service-discovery annotations).
- ✅ k3s secrets owned by the cluster (`infra/bootstrap-secrets.sh`); CI never sees prod secrets.
- ✅ Postgres `max_connections=500` (instead of an external pooler — pgbouncer image-tag situation on Docker Hub was unreliable).
- ✅ Anonymous **temp games** (2026-05-15). Visitor lands on `/`, gets a Redis-only game with 10-minute sliding TTL. `chess-anon` HttpOnly cookie binds the session. Engine-only, no PvP. See `pkg/wire/CONTRACT.md` Section 6.
- ✅ Server-authoritative clocks for PvP (2026-05-15). `clock:{id}` Redis hash holds the bank state; `clock:fallschedule` sorted-set drives a 500ms-tick flag-fall sweeper. PvP games initialize from `time_control` ("M+S"). SPA's `ClockDisplay` extrapolates locally between snapshots for smoothness; the server's number is always authoritative.
- ✅ Draw offer / accept / decline (2026-05-15). PvP only. SETNX-protected ephemeral key `draw-offer:{game_id}` holds the offerer; only the opposite participant can accept (status=`draw_agreement`, result=`1/2-1/2`) or decline. WS events `DrawOffered` / `DrawAccepted` / `DrawDeclined` round-trip both sides.
- ✅ Takeback request / accept (2026-05-15). PvP casual only (rated games never take back). Same SETNX pattern as draws; accept pops 1 or 2 plies depending on whose turn it is, so the requester ends up on move. Unilateral `/api/undo` now rejects PvP — Takeback is the only path.

---

## In progress

Nothing right now — the platform is in a stable state. Pick the next item below.

---

## Queued — Restore deleted features (2026-05-14 cleanup pass)

The cleanup pass deleted speculative / broken surfaces to lower
entropy while we land core multiplayer correctness. Re-introduce one
at a time with a fresh design pass each — don't `git revert`. The
full removal list is in `pkg/wire/CONTRACT.md` Section 5; the most
likely-to-return ones:

- ⬜ **Touch-move rule** — chess.com/lichess parity. Re-add as a
      session-level toggle, not a per-game mutation.
- ⬜ **Move assessment** — engine-graded annotations on the move list,
      pushed via WS instead of the old request/response dance.
- ⬜ **Bullet/blitz time controls** (1+0, 3+2, 5+0, 10+5…) — server
      clocks already shipped; just need to allow more strings through
      `validTimeControl` and add the SPA pickers.
- ⬜ **Save / load PGN** — proper PGN this time, not the JSON dump.
- ⬜ **Board editor + FEN paste** — a small standalone view, not
      bolted into GameView.
- ⬜ **Elo / Glicko-2 ratings** — paused 2026-05-15. The Glicko-2 math
      lives in `pkg/rating` and the DB columns are still on `users`;
      what was deleted was the consumer goroutine + every UI surface.
      When it returns, verify against the paper's worked example
      (was never numerically tested) and rebuild the matchmaker queue
      to use rating windows.

---

## Queued — Product / chess features

In priority order. Each is independent; ship one at a time.

### ⬜ Spectator mode
Read-only WS subscription for public games. Need to relax `userMayWatchGame` for games flagged public, and a new `is_public` schema column.

### ⬜ Anonymous → signed-in upgrade
A visitor playing a temp game who signs up mid-session loses their game today (the temp record is dropped on the floor; they get a fresh durable game). To preserve it, copy the `tempGameRec` into a `games` row owned by the new user inside the signup handler and redirect to `/game/<new-id>`. Single-session UX win; not blocking anyone yet.

### ⬜ Matchmaker expanding rating window
Today `ZRange 0 1` takes the two lowest-rated queue entries. Real platforms grow the rating window over time (start at ±50, expand by +50 every 2s up to ±400) so similar-rated players prefer each other on first pass.

---

## Queued — Production hardening

### ⬜ Prometheus + Grafana install
Metrics emit, nothing scrapes. Either deploy `kube-prometheus-stack` or wire up a small standalone Prom + Grafana with the existing `prometheus.io/scrape` annotations.

### ⬜ CI grep-check for wire-contract drift
A small script that fails the build when an event-name constant declared in `pkg/wire/CONTRACT.md` isn't referenced from BOTH backend and frontend. Catches the class of bug that ate hours of this session.

### ⬜ KEDA on `engine:requests` stream length
Better autoscale signal for engine-worker than CPU%. Once installed, switch the worker HPA to a `ScaledObject` with `trigger.type=redis-streams`.

### ⬜ PG read replicas
For `ListGames`, user search, replay queries. Wait until actual traffic justifies it.

### ⬜ Frontend not embedded in gateway binary
Currently every SPA tweak rebuilds the gateway image. CDN-host `dist/` instead, with the gateway only serving `index.html` (or even just the manifest). Mostly a deploy-pipeline change.

---

## Deferred (deliberate)

### 🟰 Redis Sentinel + replica HA
Single-node k3s today. Sentinel pods would all share the node's fate on a node reboot, so the failover semantics buy nothing. AOF gives durability; Sentinel revisits when a second node lands.

### 🟰 Postgres HA via Patroni
Same reasoning. Single VM, single failure domain.

### 🟰 Self-built pgbouncer image
We tried `edoburu/pgbouncer:1.23.1`, `:1.23.0`, `bitnami/pgbouncer:1.23.1`, `bitnami/pgbouncer:latest` — all returned `manifest unknown` from Docker Hub. Bumping `max_connections=500` on chess-db was simpler, lower-dependency. Build our own image only when we cross ~500 real concurrent backends.

---

## Parking lot

Things we've discussed and noted but aren't queued:

- Chat per game (Redis pub/sub channel per `game.evt.{id}`, persisted to PG for replay)
- Friend / follow graph + presence broadcast to friends
- Tournaments (Swiss + arena)
- Avatar uploads to S3-compatible storage
- Mobile app via the same WS protocol
- Public REST API + rate-limited API keys
- Anti-cheat heuristics (engine-correlation scan over move history)
- Multi-region: regional Redis + NATS JetStream cross-region replication
