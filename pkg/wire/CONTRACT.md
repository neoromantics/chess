# Wire Contract

The single source of truth for **every** interface the SPA and backend
agree on. Every event type, every HTTP route, every payload shape lives
here. When you add or rename one of these, edit this file in the same
commit — drift between this doc and the code is the bug pattern that
ate most of session-N.

CI does a grep-check (see `scripts/check-wire-contract.sh`) that fails
the build if a constant declared here isn't referenced from both
backend and frontend.

> **2026-05-14 cleanup pass:** touch-move, move-assess, board editor,
> save / load PGN file, FEN paste, draw / takeback offers, and every
> PvP time control except `15+10` were removed from this surface to
> reduce broken-feature entropy. They will be re-introduced one at a
> time later — see the `chess-paused-features` memory and ROADMAP.md.
>
> **2026-05-15:** Elo / Glicko-2 ratings paused. Anonymous **temp
> games** added — Redis-only, 10-minute sliding TTL, cookie-bound
> identity. See Sections 1 (`/api/temp/*` and `/play/:id`) and 6.

---

## Section 1 — HTTP endpoints

Every route the SPA hits. Auth column: **🔓** = no JWT required,
**🔐** = JWT cookie required, **🔐+game** = JWT + caller must own the
game row.

### Auth & user
| Method | Path | Auth | Owner | Frontend caller |
|---|---|---|---|---|
| POST | `/api/auth/signup` | 🔓 | gateway | `api.signup` |
| POST | `/api/auth/login` | 🔓 | gateway | `api.login` |
| POST | `/api/auth/logout` | 🔓 | gateway | `api.logout` |
| GET | `/api/user/me` | 🔐 | gateway | `api.getMe` |
| GET | `/api/user/profile` | 🔐 | gateway | `api.getUserProfile` |
| POST | `/api/user/password` | 🔐 | gateway | `api.changePassword` |
| GET | `/api/user/stats` | 🔐 | gateway | `api.getUserStats` |
| GET | `/api/users/search?q=…` | 🔐 | gateway | `api.searchUsers` |

### Games (lifecycle)
| Method | Path | Auth | Owner | Frontend caller |
|---|---|---|---|---|
| POST | `/api/games/new` | 🔐 | gateway | `api.createGame` |
| GET | `/api/games` | 🔐 | gateway → game-svc | `api.listGames` |
| DELETE | `/api/games/delete?game_id=X` | 🔐+game | gateway → game-svc | `api.deleteGame` |

### Games (state)
| Method | Path | Auth | Owner | Frontend caller |
|---|---|---|---|---|
| GET | `/api/state?game_id=X` | 🔐+game | gateway → game-svc | `api.getState` |
| POST | `/api/move?game_id=X` | 🔐+game | gateway → game-svc | `api.move` |
| POST | `/api/resign?game_id=X` | 🔐+game | gateway → game-svc | `api.resign` |
| POST | `/api/new?game_id=X` | 🔐+game | gateway → game-svc | `api.newGame` |
| POST | `/api/undo?game_id=X` | 🔐+game | gateway → game-svc | `api.undo` |
| POST | `/api/set_players?game_id=X` | 🔐+game | gateway → game-svc | `api.setPlayers` |
| POST | `/api/set_position?game_id=X` | 🔐+game | gateway → game-svc | `api.setPosition` (engine games only) |
| POST | `/api/hint?game_id=X` | 🔐+game | gateway → game-svc | `api.getHint` |
| GET | `/api/replay?game_id=X` | 🔐+game | gateway → game-svc | (data fetched by gateway) |
| GET | `/api/replay.html?game_id=X` | 🔐+game | gateway (template) | `Replay` button |

### Matchmaking & invites
| Method | Path | Auth | Owner | Frontend caller |
|---|---|---|---|---|
| POST | `/api/matchmaking/join` | 🔐 | gateway → game-svc (Cmd via `game:commands`) | `api.joinQueue` |
| POST | `/api/matchmaking/leave` | 🔐 | gateway → game-svc (Cmd via `game:commands`) | `api.leaveQueue` |
| GET | `/api/invites/pending` | 🔐 | gateway → game-svc | `api.listPendingInvites` |
| POST | `/api/invites/send` | 🔐 | gateway → game-svc | `api.sendInvite` |
| POST | `/api/invites/{id}/accept` | 🔐 | gateway → game-svc | `api.acceptInvite` |
| POST | `/api/invites/{id}/decline` | 🔐 | gateway → game-svc | `api.declineInvite` |
| POST | `/api/invites/{id}/cancel` | 🔐 | gateway → game-svc | `api.cancelInvite` |

### Anonymous temp games (no JWT)
Identity is the `chess-anon` HttpOnly cookie. Gateway mints it on first
hit and injects `?anon_id=<uuid>` on every proxied call. **Auth column
🍪** = cookie required. The `/ws?game_id=temp-…` upgrade path branches
inside the gateway: temp game IDs use the cookie, durable IDs use the
JWT.

| Method | Path | Auth | Owner | Frontend caller |
|---|---|---|---|---|
| POST | `/api/temp/session` | 🍪 | gateway → game-svc | `api.tempSession` |
| GET | `/api/temp/state?game_id=X` | 🍪+game | gateway → game-svc | `api.getState` (auto-routed) |
| POST | `/api/temp/move?game_id=X` | 🍪+game | gateway → game-svc | `api.move` (auto-routed) |
| POST | `/api/temp/new?game_id=X` | 🍪+game | gateway → game-svc | `api.newGame` (auto-routed) |
| POST | `/api/temp/undo?game_id=X` | 🍪+game | gateway → game-svc | `api.undo` (auto-routed) |
| POST | `/api/temp/resign?game_id=X` | 🍪+game | gateway → game-svc | `api.resign` (auto-routed) |
| POST | `/api/temp/hint?game_id=X` | 🍪+game | gateway → game-svc | `api.getHint` (auto-routed) |

`api.ts` routes by ID prefix: any game whose ID starts with `temp-`
hits the `/api/temp/*` surface; everything else hits the durable
`/api/*` surface. SPA components don't branch on this themselves.

### Infra
| Method | Path | Notes |
|---|---|---|
| GET | `/health` | every service |
| GET | `/metrics` | Prometheus scrape, every service |
| GET | `/ws?game_id=X` | per-game WebSocket. JWT + game-ownership pre-flight for durable; cookie + temp-ownership check for temp-prefixed IDs |
| GET | `/ws/user` | per-user WebSocket. JWT cookie |
| GET | `/`, `/assets/*` | gateway-embedded SPA |

The single supported PvP time control is **`15+10`** (rapid). The
matchmaking queue keys (`mm:queue:{tc}`), the invite-acceptance
validator (`validTimeControl`), and the SPA pickers all hard-code
this for now.

---

## Section 2 — Redis pub/sub & streams

Three keyspaces with different durability semantics. Don't mix them.

| Channel | Type | Durability | Direction |
|---|---|---|---|
| `game:commands` | Stream + consumer group | Durable, at-least-once | gateway/matchmaker → game-svc |
| `game:events` | Stream + consumer group | Durable, replayable | game-svc → rating-updater + audit |
| `engine:requests` | Stream + consumer group | Durable | game-svc → engine-worker pool |
| `engine:results` | Stream + consumer group | Durable | engine-worker → game-svc |
| `game.evt.{id}` | Pub/Sub | Ephemeral | game-svc → gateway hub → per-game WS clients |
| `user.evt.{id}` | Pub/Sub | Ephemeral | any service → gateway hub → per-user WS clients |

### Redis state keys

| Key | Type | TTL | Purpose |
|---|---|---|---|
| `game:lock:{id}` | string (SET NX) | 10s | Per-game mutation serialization (durable AND temp) |
| `game:state:{id}` | hash | 1h | Hot cache for durable game rows |
| `game:thinking:{id}` | string | 2×movetime + 2s | UI spinner sentinel |
| `mm:queue:{tc}` | sorted set (rating) | none | Matchmaking queue per time control |
| `mm:leader` | string (SET NX) | 15s | Pairing-loop leader election |
| `tempgame:state:{id}` | string (JSON blob) | 10m sliding | Anonymous game record; expires after 10m of inactivity |
| `tempgame:session:{anon_id}` | string (game_id) | 10m sliding | Maps a `chess-anon` cookie to its current temp game (one per browser) |
| `clock:{id}` | hash | none (deleted on game end) | Server-authoritative bank: `white_ms`, `black_ms`, `inc_ms`, `initial_ms`, `mover`, `turn_started_ms` |
| `clock:fallschedule` | sorted set | none | game_id → unix-ms deadline of current mover; clock-fall sweeper polls this every 500ms |
| `draw-offer:{game_id}` | string (user_id) | 60s (capped to remaining clock) | Pending draw offer; SETNX-protected so only one offer can be open at a time |
| `takeback-offer:{game_id}` | string (user_id) | 60s (capped to remaining clock) | Pending takeback request; SETNX-protected |

---

## Section 3 — WebSocket envelope events

Every WS frame is `{type: string, payload: any}`. The SPA listeners
demultiplex on `type` exact-match. **Renaming any of these is a wire-
protocol break** — bump the SPA simultaneously.

⚠️ **Middleware that wraps `http.ResponseWriter` must forward
`http.Hijacker` (and ideally `http.Flusher`).** `gorilla/websocket`
needs `Hijack()` to take over the TCP connection during `Upgrade`.
A wrapper that embeds `http.ResponseWriter` only exposes interface
methods, NOT the concrete underlying implementation's `Hijack()`.
Forgetting this breaks every WS upgrade silently with
`websocket: response does not implement http.Hijacker`. See
`pkg/metrics/metrics.go:statusRecorder` for the canonical pattern.

### `/ws?game_id=X` (game.evt.{id})

| `type` | When | Payload shape | Backend constant | Frontend handler |
|---|---|---|---|---|
| `StateUpdated` | every HTTP mutation in cmd/game/handlers.go | full `stateJSON` | `eventbus.EvtStateUpdated` | `GameView.connectWS` → `updateState` |
| `state` | (legacy alias for StateUpdated) | full `stateJSON` | — | `GameView.connectWS` → `updateState` |
| `MovePlayed` | engine-result Command consumer | `MovePlayedEvt` (partial — SPA refetches) | `eventbus.EvtMovePlayed` | `GameView.connectWS` → `api.getState` |
| `GameStarted` | new row written | empty | `eventbus.EvtGameStarted` | `GameView.connectWS` → `api.getState` |
| `GameFinished` | game terminal | `stateJSON` | `eventbus.EvtGameFinished` | (rating-updater consumes; SPA reads via StateUpdated companion) |
| `hint` | engine response, hint context | `{move, from, to, score, depth}` | (literal) | `GameView.onHintReceived` |
| `DrawOffered` | one PvP participant offered a draw | `{from_user_id, game_id}` | `eventbus.EvtDrawOffered` | `GameView.connectWS` → set incoming-draw banner |
| `DrawDeclined` | opponent declined a draw offer | `{by_user_id, game_id}` | `eventbus.EvtDrawDeclined` | `GameView.connectWS` → toast + clear banner |
| `DrawAccepted` | opponent accepted; game ends 1/2-1/2 | `stateJSON` (status=draw_agreement) | `eventbus.EvtDrawAccepted` | (companion to StateUpdated; SPA clears banner) |
| `TakebackRequested` | PvP casual game; one side asked for a takeback | `{from_user_id, game_id, plies}` | (literal — defined in cmd/game/takebacks.go) | `GameView.connectWS` → set incoming-takeback banner |
| `TakebackDeclined` | opponent declined a takeback | `{by_user_id, game_id}` | (literal) | toast + clear banner |
| `TakebackAccepted` | opponent accepted; history rolled back N plies | `stateJSON` | (literal) | (companion to StateUpdated; SPA clears banner) |

### `/ws/user` (user.evt.{user_id})

All snake_case. SPA's `userEventsStore.on(type, fn)` listeners are the
contract — backend emissions must match these strings exactly.

| `type` | When | Payload shape | Backend constant | Frontend handler |
|---|---|---|---|---|
| `match_found` | matchmaker pairs two users | `MatchFoundEvt {game_id, white_user_id, black_user_id, color}` | `eventbus.EvtMatchFound` | `Match.onMounted` |
| `invite_created` | recipient receives a new invite | `inviteWire` | `eventbus.EvtInviteCreated` | `useInviteStore` |
| `invite_sent` | sender's confirmation echo | `inviteWire` | `eventbus.EvtInviteSent` | `useInviteStore` |
| `invite_accepted` | invite matured into a game | `inviteWire` (includes game_id) | `eventbus.EvtInviteAccepted` | `useInviteStore` + redirect |
| `invite_declined` | recipient said no | `inviteWire` | `eventbus.EvtInviteDeclined` | `useInviteStore` |
| `invite_cancelled` | sender withdrew | `inviteWire` | `eventbus.EvtInviteCancelled` | `useInviteStore` |
| `invite_expired` | TTL ran out (sweeper) | `inviteWire` | `eventbus.EvtInviteExpired` | `useInviteStore` |

---

## Section 4 — JSON payload shapes

### `stateJSON` — returned by every game-mutation HTTP endpoint and broadcast on `StateUpdated`

```ts
interface StateJSON {
  id?: string;                             // set on temp-game snapshots, omitted for durable
  fen: string;
  turn: 'w' | 'b';
  engine_white: boolean;
  engine_black: boolean;
  engine_to_move: boolean;
  status: string;                          // ongoing | checkmate | stalemate | draw_* | resign | timeout
  result: string;                          // '*' | '1-0' | '0-1' | '1/2-1/2'
  in_check: boolean;
  legal_moves: string[];
  history: string[];                       // UCI
  history_san: string[];
  last_move: { from, to } | null;
  thinking: boolean;
  white_think_time: number;                // engine search budget for white, in ms
  black_think_time: number;                // engine search budget for black, in ms
  white_user_id: number | null;            // null = engine
  black_user_id: number | null;
  time_control: string;                    // "engine" | "M+S" e.g. "15+10"
  rated: boolean;

  // Server-authoritative clock projection (PvP only; engine games leave
  // clock_initial_ms === 0 and the SPA hides the clock UI).
  // - white_clock_ms / black_clock_ms: bank values at clock_server_ms.
  //   The mover's bank already has elapsed wall time deducted.
  // - clock_mover: "w" | "b" while a clock is running, "" otherwise.
  // - SPA extrapolates locally: for the mover side, subtract
  //   (Date.now() - snapshot_received_at) from the bank each frame.
  white_clock_ms: number;
  black_clock_ms: number;
  clock_initial_ms: number;
  clock_inc_ms: number;
  clock_mover: '' | 'w' | 'b';
  clock_server_ms: number;                 // unix-ms at snapshot time
}
```

### `MatchFoundEvt`
```go
type MatchFoundEvt struct {
    GameID      string `json:"game_id"`
    WhiteUserID int64  `json:"white_user_id"`
    BlackUserID int64  `json:"black_user_id"`
    Color       string `json:"color"`      // recipient's color: "white" | "black"
}
```

### `inviteWire` (cmd/game/invites.go)
```go
type inviteWire struct {
    ID           string  `json:"id"`
    FromUserID   int64   `json:"from_user_id"`
    FromUsername string  `json:"from_username,omitempty"`
    ToUserID     int64   `json:"to_user_id"`
    ToUsername   string  `json:"to_username,omitempty"`
    TimeControl  string  `json:"time_control"`
    Rated        bool    `json:"rated"`
    Status       string  `json:"status"`
    GameID       *string `json:"game_id,omitempty"`
    CreatedAt    string  `json:"created_at"`   // RFC3339
    ExpiresAt    string  `json:"expires_at"`   // RFC3339
}
```

---

## Section 5 — Deferred features (will return one at a time)

The following surfaces existed in earlier sessions and were deleted
during the 2026-05-14 entropy-cleanup pass. When you re-introduce one,
land it as its own commit with a fresh design pass — don't `git revert`
the cleanup. The `chess-paused-features` memory tracks the full list.

| Feature | What was removed |
|---|---|
| Server clocks | Shipped 2026-05-15. PvP games initialize a `clock:{id}` Redis hash from `time_control` (e.g. "15+10"); `clocks.go` deducts elapsed wall time + adds increment per move; flag-fall sweeper finalizes timeouts. Engine games still use `white_think_time`/`black_think_time` — those are engine search budgets, not a game clock. |
| Touch-move | `pkg/game/Touch`, `TouchMove`, `TouchedSq`, `TouchLost`, `StatusTouchLost`, `/api/touch`, `/api/touch_move`, SPA toggle |
| Move assessment | `/api/assess`, `Assessments` field in `stateJSON`, `ClassifyAssessment`, SPA UI, `ASSESS_COLORS` / `ASSESS_SYMBOL` |
| Board editor | Restored 2026-05-15. `EditPanel.vue` is back; `POST /api/set_position` (engine games only) replaces the position with a user-supplied FEN, persisted as `start_fen` on the `games` row. PvP games are rejected server-side. |
| Save / load PGN file + FEN paste | `/api/save`, `/api/load`, SPA buttons + handlers |
| Draw offer / accept / decline | SPA emit sites in `SidePanel`, `GameView` handlers |
| Takeback request | (same as draw) |
| Bullet / blitz / correspondence time controls | `1+0`, `2+1`, `3+2`, `5+0`, `10+5`, `corr-1d` dropped from `validTimeControl`, `supportedTCs`, SPA pickers |
| Elo / Glicko-2 ratings (paused 2026-05-15) | `cmd/game/rating.go`, `runRatingUpdater` goroutine, every UI rating chip. DB columns + `pkg/rating` math kept |

---

## Section 6 — Anonymous temp games

The "land on the URL, get a game; come back within 10 minutes, your
game is still there; otherwise it's gone forever" flow.

**Identity.** The gateway mints an `chess-anon` HttpOnly cookie on
first hit (UUID). It injects `?anon_id=<uuid>` into every proxied
`/api/temp/*` request — symmetric with `injectAuthedUser`. Game-service
trusts the injection and authorizes by matching against the stored
`OwnerAnonID` field on the temp record.

**Storage.** Two Redis keys per session, both with sliding 10-minute
TTL — every read AND every write calls `EXPIRE` to push the window
back. No Postgres row, no sweeper goroutine, no migration.

```
tempgame:state:{game_id}    JSON blob (the full tempGameRec)
tempgame:session:{anon_id}  string game_id (one per browser)
```

**Engine pipeline.** `EngineRequest.Metadata{"temp":"1","anon_id":…}`
flags a search as temp. Engine-worker echoes Metadata verbatim;
`processEngineResult` branches on `Metadata["temp"] == "1"` and calls
`applyTempEngineMove` (writes to the temp store) instead of dispatching
the usual `CmdMakeMove` Command (which would write to PG).

**Realtime.** Temp games publish state on `game.evt.{game_id}` —
exactly the same channel durable games use. The hub's existing
`PSUBSCRIBE game.evt.*` picks both up. The `/ws?game_id=X` upgrade
inspects the prefix: `temp-…` IDs go through the cookie-authenticated
path that reads `tempgame:state:{id}` to verify ownership.

**Routes.** Mirror the durable surface, prefixed `/api/temp/`:
`session, state, move, new, undo, resign, hint, set_players, set_position`. The
SPA's `api.ts` auto-routes by ID prefix so callers stay flavor-agnostic.
`set_players` updates engine assignment + per-side think time on the
existing temp record (split fields `WhiteThinkTimeMS`/`BlackThinkTimeMS`)
and immediately kicks off an engine search if the new settings make it
the engine's turn — so a mid-game think-time change takes effect on
the engine's next move without a New Game.

**Limits.** Engine-only (no PvP, no invites, no rating). One game per
browser session. Resigning ends the game; the cookie still points at
it until expiry, so a refresh re-renders the resigned position.

**Sign-up upgrade.** Implemented 2026-05-15. When `POST /api/auth/signup`
sees a `chess-anon` cookie, the gateway calls game-service's internal
`POST /api/temp/upgrade?anon_id=X&user_id=Y` after the user is created.
That handler copies the `tempGameRec` into a durable `games` row owned
by the new user (carrying FEN, history, engine assignment, per-side
think times), then deletes both `tempgame:state:{old_id}` and
`tempgame:session:{anon_id}`. The signup response gains an
`upgraded_game_id` field; the SPA navigates to `/game/<new-id>` if
present so the carry-over is invisible to the user. Failures are
non-fatal (signup still succeeds, the user just lands on the next
route as usual).

## Section 6 — How to add a new wire surface

1. Edit this doc first. Pick names, document the payload shape.
2. Add a constant in `pkg/eventbus/eventbus.go` (or the HTTP route in
   the appropriate gateway/service mux). Don't use string literals.
3. Add the matching TypeScript type / store handler / api function.
4. Reference the same constant from BOTH sides — backend emit site
   AND frontend listener — so renames are single-grep operations.
5. If you're adding a JSON payload, put the Go struct in `pkg/eventbus`
   and the TS interface in `frontend/src/types.ts`. Keep field names
   identical (snake_case) so they line up under JSON serialization.

The pattern that has burned us: defining the wire type in one place
and the listener in another. Don't do that.
