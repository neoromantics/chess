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

Recent ops/cleanup (2026-05-16):
- ✅ **Holistic design-audit pass** (`6c464a5` … `2f6ebee`).
  - Dropped dead `users.elo` + `games.assessments` columns and the
    `CmdResign` / `SendInviteCmd` / `AcceptInviteCmd` /
    `DeclineInviteCmd` / `CancelInviteCmd` structs that had no
    consumer. Single-game mutations and per-invite verbs all run
    over sync HTTP now per the Streams-vs-HTTP rule.
  - Per-session PG `statement_timeout = 5s` via DSN
    (`PG_STATEMENT_TIMEOUT_MS` overrides). Stops a missing-index
    regression from pinning a pool connection forever.
  - Game cache stores one JSON blob per row instead of the
    per-field hash — one serialize per write, one deserialize per
    read, and schema changes ride on the existing json tags.
  - `bus.Consume(stream, group, consumer, handler)` collapses the
    for-select + ReadX + range + Ack boilerplate that was duplicated
    at three sites.
  - Graceful shutdown across gateway, game-service, engine-worker
    (`http.Server.Shutdown` + WaitGroup; engine-worker also signals
    every active search via the existing per-game stop atomic so the
    consumer-group's pending list doesn't grow on rolling restarts).
  - Identity moved to `X-User-ID` / `X-Anon-ID` request headers
    (query params still accepted as fallback for rolling-deploy
    safety; drop in a follow-up).
  - New `/api/can_watch?game_id=X` — bare ownership check for the
    WS upgrade preflight; replaces the old full-snapshot call.
  - Hub `register`/`unregister` channels 32 → 1024 for reconnect
    storms; Traefik `service.sticky.cookie` on `chess-gateway` so a
    returning client lands on the pod that already has its
    `game.evt.{id}` / `user.evt.{id}` SUBSCRIBEs live.
- ✅ **Hint privacy fix** (`ed90b36`). PvP hint now publishes to the
  requester's `user.evt.{id}` instead of the shared `game.evt.{id}`,
  so the opponent no longer sees the suggested move on their own
  board. Temp games stay on `game.evt` (single-player; no leak).
- ✅ **Time-control menu trimmed to 3+0 (Blitz) and 10+0 (Rapid)**
  (`ed90b36`). 10 buckets diluted the matchmaking pool at our
  current playerbase; both `validTimeControl` and `supportedTCs`
  moved in lock-step with the SPA picker.
- ✅ CI: bumped `actions/setup-go` from v5 to v6 (native Node 24).
  Combined with the earlier `checkout` / `setup-node` bumps and the
  `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24` backstop, the Sept-2026
  Node-20 deprecation warning is fully cleared.
- ✅ CI: bumped the three docker actions to their Node-24-native
  majors — `docker/login-action` v3→v4, `docker/setup-buildx-action`
  v3→v4, `docker/build-push-action` v5→v7. Each major's release
  notes call out "Node 24 as default runtime", so the deprecation
  warning is now silent on the `docker` job too.
- ✅ **Engine-fallback matchmaker** (`c21b863` → `1a69a1b` → `dc409f6`
  → this commit). Temporary engagement hack while organic pairing
  volume is low: a matchmaking entry that waits longer than
  `mmEngineFallbackAfter` (10s) gets silently routed into a game
  against a seeded "bot" user from `cmd/game/bots.go`. All bot infra
  is tagged `TODO(matchmaker-engine-fallback)` for trivial removal.
  - **Bot pool**: 12 chess-themed usernames seeded into the `users`
    table at boot (idempotent `UpsertBot` with `is_bot=TRUE`); each
    gets a random bcrypt hash so nobody can log in as them.
    `SearchUsersByPrefix` excludes them so they don't surface in
    invite autocomplete.
  - **Disguise**: snapshotFromRecord masks `engine_{white,black}` /
    `engine_to_move` to false for bot matches, so the SPA renders
    the game as PvP — no "Engine settings", no "(engine thinking…)".
    Server-side trigger uses the truthful flags.
  - **Humanization**: random color per match (50/50), 3s or 10s
    reaction delay added to every engine move (the search itself is
    600ms — the delay is what reads as human). Thinking-sentinel
    TTL bumped to 15s for bot games so the spinner survives the
    full reaction window.
  - **Silent rematch**: `handleHTTPNew` detects `isBotMatch(rec)`
    and preserves the original engine_white/black flags even when
    the SPA (which sees masked flags) sends both false.
  - **Stale-row guard**: the delayed MakeMove dispatcher captures
    `rec.UpdatedAt` as a generation token; if the row changed
    during the wait (resign / rematch / something else), the move
    is dropped and the thinking sentinel cleared.
  - **Negotiation auto-response**: when a human in a bot match
    offers a draw or takeback, a goroutine waits 0.5–10s then
    randomly accepts (~40%) or declines, emitting the same
    `DrawAccepted` / `DrawDeclined` / `TakebackAccepted` /
    `TakebackDeclined` events the HTTP handler would. Without this
    the bot would silently ignore the offer until TTL expiry, which
    is the loudest possible "this isn't a human" tell.
  - All non-rated so `rec.Rated` guards keep Glicko-2 clean.

Recent ops/cleanup (2026-05-15):
- ✅ Dropped the dead `games.session_id` column (pre-SPA holdover).
  Removed from `schema.sql` + added an idempotent `DROP COLUMN IF
  EXISTS` for clusters that still have it; `sqlc` regenerated.
- ✅ Narrowed the `Store` interface — removed the unused
  `AcceptInvite(...)` wrapper; the only path now is the atomic
  `AcceptInviteWithGame(...)`. Also dropped the dead
  `eventbus.ChannelUserEvents` constant (services build the channel
  name directly via `fmt.Sprintf("user.evt.%d", uid)`).

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
- ✅ **Move assessment** (2026-05-15 phase 1; 2026-05-16 phases 2+3) —
      `POST /api/analyze` replays the game ply-by-ply and dispatches a
      200ms engine search per pre-move position PLUS one terminal
      anchor at the post-last-move position. cp_loss is computed from
      consecutive search scores (`pre.Score + post.Score`, additive
      under negamax) and bucketed into `best / only / great / good /
      inaccuracy / mistake / blunder` (lichess-calibrated thresholds).
      Per-ply `EvtAssessment` streams over the per-game WS channel;
      multi-replica dedupe via `analyze:emitted:{game_id}:{ply}`
      SETNX. SidePanel decorates the move list with ✓ / ★ / ! / !? /
      ? / ?? glyphs + color spectrum + cp-loss tooltips. **Phase 3**:
      classified payloads accumulate in `analyze:emits:{game_id}` and,
      once HLEN == ply count, a single SETNX-deduped bulk write
      persists the array to `games.assessments`. Snapshot drops it
      again if the count doesn't match the live history length (so a
      subsequent move/undo doesn't leave stale verdicts on the board).
      The SPA hydrates `assessments.value` from the snapshot, so
      reopening a finished game shows its analysis without re-running
      the engine.
- ✅ **Time controls** (2026-05-15, 2026-05-16) — restored as
      `1+0, 2+1, 3+0, 3+2, 5+0, 5+3, 10+0, 10+5, 15+10, 30+0`, then
      trimmed back to `3+0` + `10+0` on 2026-05-16 because the
      10-bucket fan-out diluted the matchmaking pool. Expand again
      via `validTimeControl` + `supportedTCs` when the playerbase
      justifies a third queue.
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

### ✅ Spectator mode (2026-05-16)
Read-only WS subscription for public games. Shipped:
- `games.is_public BOOLEAN NOT NULL DEFAULT FALSE` column with
  idempotent `ADD COLUMN IF NOT EXISTS` for clusters mid-rollout.
- Backend split: `userOwnsGame` stays strict (mutations require
  participation); new `userMayRead` allows anyone on public rows.
- `POST /api/visibility?game_id=X` (owner-only) flips the flag.
- `/api/state` and `/api/can_watch` made auth-optional via the new
  `injectAuthedUserOptional` middleware; anonymous viewers pass the
  preflight only on public games.
- New SPA route `/watch/:id` loads `GameView` with `spectator: true`.
  GameView also auto-detects read-only mode when a signed-in user
  lands on someone else's public game.
- SidePanel: visibility toggle for owners (with a "copy spectator
  link" button); spectator banner + button suppression for viewers.
- Click handlers short-circuit for spectators (the backend rejects
  too via `userOwnsGame`, but suppressing the phantom selection is
  the friendlier UX).

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
- _(KEDA on engine:requests stream depth shipped 2026-05-16 — see Production hardening below.)_
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

### ✅ Prometheus + Grafana install (2026-05-16)
Standalone Prom + Grafana shipped in `infra/observability.yaml`, plugged into the existing Kustomization. Prometheus discovers chess Services via `kubernetes_sd_config` (namespace-scoped Role/RoleBinding) and the existing `prometheus.io/scrape` annotations; Grafana auto-provisions Prometheus as the default datasource and ships one `chess-overview` dashboard (HTTP RPS / p95 latency / WS connection gauge / Redis stream pending / 5xx error rate / pod count). Grafana lives behind the existing Traefik ingress at `/grafana/` with the LE cert; admin password is `GRAFANA_ADMIN_PASSWORD` in `chess-secrets` (bootstrap mints it). Prometheus query UI is intentionally not exposed — `kubectl port-forward svc/prometheus 9090` for power users. Skipped `kube-prometheus-stack` because its 30+ CRDs and operator are overkill for three Services.

### ✅ CI grep-check for wire-contract drift (2026-05-16)
Shipped in `infra/check-wire-contract.sh` + a `wire-contract` CI job that blocks `docker` from running on drift. Extracts every WS event from Section 3 of `CONTRACT.md`, verifies each is referenced as a literal in both backend Go and frontend TS/Vue. Rows tagged with `<!-- spa-ignored -->` opt out of the frontend check (currently only `GameFinished`, which the SPA reads via its `StateUpdated` companion).

### ✅ KEDA on `engine:requests` stream depth (2026-05-16)
Shipped in `infra/keda.yaml` (added to the kustomization). Replaces the CPU-only chess-worker HPA with a KEDA `ScaledObject` that pages off `XPENDING` for the `engine-worker-group` consumer group on `engine:requests`, with CPU at 70% as a secondary backstop (so a long-running search keeps a pod scaled-up even when the queue itself has drained). `pendingEntriesCount=3` per pod targets ~24 in-flight engine jobs at max scale (`maxReplicaCount=8`), matching the worker's GOMAXPROCS=1-per-pod design. Same min/max envelope and scale-up/scale-down behaviour as the legacy HPA. Prerequisite: KEDA v2 must be installed on the cluster (one-time `kubectl apply -f .../keda-2.16.0.yaml`, documented in `infra/keda.yaml`). Operator action after this lands: `kubectl -n chess delete hpa chess-worker-hpa` to remove the old scaler.

### ✅ Gateway scaled on live WebSocket connection count (2026-05-16)
Shipped in `infra/keda.yaml` alongside the worker ScaledObject. CPU + memory were poor proxies for gateway load — a gateway pod spends most of its time idle on `select` waiting for WS register/unregister events, while the actual hot path is per-subscriber pub/sub fan-out from `game.evt.{id}` / `user.evt.{id}`. KEDA queries Prometheus directly (`sum(chess_ws_connections_active{service="gateway"})`) and divides by replica count to compare against `threshold=200`, so each pod targets ~200 live WS clients. CPU 70% + memory 75% stay as backstops in case a runaway hub goroutine pegs a core or Prometheus has an outage. The `chess_ws_connections_active` gauge was already wired in `pkg/metrics` + `cmd/gateway/hub.go` for this purpose; this commit only ships the autoscaler. KEDA's `prometheus` trigger means no Prometheus Adapter install is needed (KEDA already speaks PromQL). Operator action: `kubectl -n chess delete hpa chess-gateway-hpa`.

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
