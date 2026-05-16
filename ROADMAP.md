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
- ✅ Anonymous **temp games** (2026-05-15). Redis-only game with 10-minute sliding TTL. `chess-anon` HttpOnly cookie binds the session. Engine-only, no PvP. See `pkg/wire/CONTRACT.md` Section 6.
- ✅ Landing-page mode chooser (2026-05-15). `/` shows two cards: "Play vs Engine" (anon → temp `/play/<id>`, signed-in → durable `/game/<id>`) and "Match a Player" (anon → signup with `?next=/match`, signed-in → `/match`). Board-editor card is a soon-disabled placeholder until the editor is restored.
- ✅ Dashboard dropped, replaced by clean `/match` page (2026-05-15). PvP matchmaking + active-game list in a single focused view. `/dashboard` redirects to `/match` for any stale bookmarks.
- ✅ Anonymous → signed-in upgrade (2026-05-15). When `POST /api/auth/signup` carries a `chess-anon` cookie, the gateway calls internal `POST /api/temp/upgrade` on game-service to copy the temp game into a durable row owned by the new user; the signup response carries `upgraded_game_id` and the SPA lands the user back in their game.
- ✅ Server-authoritative clocks for PvP (2026-05-15). `clock:{id}` Redis hash holds the bank state; `clock:fallschedule` sorted-set drives a 500ms-tick flag-fall sweeper. PvP games initialize from `time_control` ("M+S"). SPA's `ClockDisplay` extrapolates locally between snapshots for smoothness; the server's number is always authoritative.
- ✅ Draw offer / accept / decline (2026-05-15). PvP only. SETNX-protected ephemeral key `draw-offer:{game_id}` holds the offerer; only the opposite participant can accept (status=`draw_agreement`, result=`1/2-1/2`) or decline. WS events `DrawOffered` / `DrawAccepted` / `DrawDeclined` round-trip both sides.
- ✅ Takeback request / accept (2026-05-15). PvP casual only (rated games never take back). Same SETNX pattern as draws; accept pops 1 or 2 plies depending on whose turn it is, so the requester ends up on move. Unilateral `/api/undo` now rejects PvP — Takeback is the only path.

---

## In progress

Nothing right now — the platform is in a stable state. Pick the next item below.

Recent ops/cleanup (2026-05-15):
- ✅ Dropped the dead `games.session_id` column (pre-SPA holdover).
  Removed from `schema.sql` + added an idempotent `DROP COLUMN IF
  EXISTS` for clusters that still have it; `sqlc` regenerated.
- ✅ Narrowed the `Store` interface — removed the unused
  `AcceptInvite(...)` wrapper; the only path now is the atomic
  `AcceptInviteWithGame(...)`. Also dropped the dead
  `eventbus.ChannelUserEvents` constant (services build the channel
  name directly via `fmt.Sprintf("user.evt.%d", uid)`).
- ✅ CI: bumped `actions/checkout` and `actions/setup-node` to `@v5`
  (native Node 24) so the Sept-2026 Node-20 deprecation warning stops
  firing. The `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24` env var stays as a
  backstop for any future Node-20 action.

---

## Queued — Restore deleted features (2026-05-14 cleanup pass)

The cleanup pass deleted speculative / broken surfaces to lower
entropy while we land core multiplayer correctness. Re-introduce one
at a time with a fresh design pass each — don't `git revert`. The
full removal list is in `pkg/wire/CONTRACT.md` Section 5; the most
likely-to-return ones:

- ✅ **Touch-move rule** (2026-05-15) — session-level client toggle in
      SidePanel toolbar. When ON, a selected ("touched") piece is locked
      until it makes a legal move; can't switch to a different piece or
      deselect. localStorage-persisted, applies to PvP + engine games.
      Pure SPA — no backend churn.
- ✅ **Move assessment** (2026-05-15, phase 1) — `POST /api/analyze`
      replays the game ply-by-ply and fires a 200ms engine search per
      pre-move position; per-ply `EvtAssessment` streams over the
      per-game WS channel. SidePanel renders ✓ / ? / ★ (best / alt /
      only-legal) next to each SAN. Centipawn-loss classification +
      stored `rec.Assessments` persistence are follow-up phases.
- ✅ **Bullet/blitz/classical time controls** (2026-05-15) — restored
      `1+0, 2+1, 3+0, 3+2, 5+0, 5+3, 10+0, 10+5, 15+10, 30+0` via
      `validTimeControl` + `supportedTCs`. Match page exposes the
      pills grouped by category (bullet/blitz/rapid/classical).
- ✅ **Save / load PGN** (2026-05-15) — proper Seven-Tag-Roster PGN
      via `pkg/pgn`. `GET /api/pgn?game_id=X` downloads, `POST
      /api/load_pgn` replays a pasted PGN onto the row (engine games
      only). Tested round-trip + custom-FEN headers + comments/variations.
- ✅ **Board editor** (2026-05-15) — restored as `POST /api/set_position`
      (engine games only) + `EditPanel.vue` inside `GameView`. User opens
      the Setup tool, paints pieces, sets side-to-move + castling, hits
      Apply; backend validates the FEN, wipes history, stores it as
      `start_fen`, and kicks the engine if it's the engine's turn.
      PvP rejected server-side. FEN paste still queued.
- ✅ **Elo / Glicko-2 ratings** (2026-05-15) — `pkg/rating` numerically
      verified against the paper's worked example
      (`TestUpdateAgainstPaperExample`: r=1500/RD=200/σ=0.06 +
      W/L/L → r≈1464.06, RD≈151.52, σ≈0.05999). `runRatingUpdater`
      goroutine re-armed, consumes `game:events` (rating-updater-group),
      one-game-per-period update on every rated `GameFinished`.
      Pushes `rating_updated` user-events; SPA `authStore` updates the
      live rating chip on the profile. Matchmaker now uses an expanding
      rating window: starts at ±50, grows by +50 every 2s, capped at
      ±400. Gateway looks up the user's authoritative rating instead of
      trusting the client.

---

## Queued — Product / chess features

In priority order. Each is independent; ship one at a time.

### ⬜ Spectator mode
Read-only WS subscription for public games. Need to relax `userMayWatchGame` for games flagged public, and a new `is_public` schema column.

### ✅ Matchmaker expanding rating window (2026-05-15)
Shipped with the rating restoration. Each queue entry carries an
enqueue timestamp in `mm:joined:{tc}`; per-tick `tryPair` walks the
ZSet and pairs adjacent users only when the rating gap fits inside
*both* parties' currently-allowed window (initial ±50, +50 every 2s,
capped at ±400). Gateway pulls the authoritative rating from the DB
on JoinQueue so a 1200 player can't queue as 2400.

---

## Scale design notes (10k concurrent pairs)

Recorded 2026-05-15 from a deliberate "what fails at 20k users / 10k
games?" walk-through. Three immediate wins shipped that day; the
rest are deferred with reasons.

**Shipped:**
- ✅ **Per-channel SUBSCRIBE in the gateway hub.** Was
      `PSUBSCRIBE game.evt.*`, so every pod received every event.
      Now the hub `SUBSCRIBE`s `game.evt.{id}` on first local-client
      register and `UNSUBSCRIBE`s on last unregister. Cross-pod
      fan-out cost is now proportional to "pods with a live local
      subscriber" rather than "every pod".
- ✅ **Postgres indices.** `games.white_user_id`,
      `games.black_user_id`, `games.updated_at DESC`, `games.status`,
      and partial indices on `invites` for pending / per-user lookups.
      Idempotent in `pkg/db/schema.sql`.
- ✅ **Env-tunable connection pool + bumped defaults.**
      `PG_MAX_OPEN_CONNS` / `PG_MAX_IDLE_CONNS` so ops can move the
      per-pod ceiling without a code change. Defaults bumped
      2026-05-15: open 25→30, idle 5→10. Engine-worker has no PG pool,
      so the live ceiling is 8 (gateway HPA max) + 6 (game-service HPA
      max) = 14 pods × 30 = 420 client conns vs. `max_connections=500`
      — leaving ~80 for autovacuum and superuser reservations.

**Deliberately deferred (real bottlenecks at 10k pair scale, but not
worth pre-building):**
- 🟰 **Redis Sentinel + multi-node.** Today's single Redis is the
      hot path for streams, locks, cache, pub/sub. It's a SPOF and a
      vertical scale ceiling, but failover semantics need a real
      second node — meaningful only after the cluster grows past one
      VM.
- 🟰 **KEDA on engine:requests stream depth** (already on the
      hardening list).
- 🟰 **PG read replicas** for `ListGames` / search / replay.
- 🟰 **Matchmaker sharding per TC.** Current single-leader pairing
      handles low-thousands queue depth comfortably; sharding only
      pays back past that.
- 🟰 **Pgbouncer.** Tried during the 6→3 work; Docker Hub tag flakes
      cost four deploy cycles. Bumping `max_connections=500` + the
      env-tunable per-pod pool covers us to mid-low-thousands of
      backends.

**Real anti-patterns I checked for and didn't find:**
- No in-process game state (would break multi-replica).
- No `runtime.NumCPU()` (would oversubscribe under cgroups —
  uses `GOMAXPROCS` instead).
- No JWT-style trust of client-supplied identity for game ownership.
- No per-game leader election needed — every mutation path holds the
  per-game Redis SETNX lock.

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
