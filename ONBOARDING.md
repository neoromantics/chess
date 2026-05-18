# Onboarding — neoromantics Chess

Welcome. This document is the bridge between what you learned in your CS degree and what you'll actually be doing in this codebase. It assumes you're comfortable with algorithms, data structures, basic OS concepts, SQL, Git, and at least one mainstream language. It does **not** assume you've shipped to a Kubernetes cluster, debugged a distributed lock, or argued with a Prometheus dashboard at 11 pm. Those gaps are what this doc fills.

Read this once end-to-end before touching code. Then keep `CLAUDE.md` open as your day-to-day reference — that's the operator's manual; this is the *map*.

---

## 1. What you're working on

`neoromantics-chess` is a multiplayer chess platform — sign up, get a Glicko-2 rating, play other humans through matchmaking, play the engine, analyze your games, watch other people's games live. It runs at `https://vcm-50800.vm.duke.edu` on a Kubernetes cluster (k3s, single VM at Duke).

Three programs in Go, one Postgres, one Redis, one Vue 3 single-page app. Everything talks over the network — no shared memory, no shared disk, no in-process state that matters across restarts. You will find this annoying at first and liberating later.

The frontend is *embedded inside the gateway binary* via Go's `//go:embed`. That means one Docker image carries the SPA, the auth surface, the game logic, and the routing layer. The engine search is the only thing that lives in its own image, because CPU search has a fundamentally different scaling profile from web traffic.

---

## 2. The shape of the system

```
Browser (Vue 3 SPA, embedded in gateway binary)
        │ HTTPS / WSS
        ▼
   gateway  ──── auth, profiles, HTTP routing, WebSocket fan-out ────┐
        │                                                            │
        │ sync HTTP for game endpoints                               │
        │ Redis Streams for intent dispatch (matchmaker, engine)     │
        ▼                                                            │
  game-service  ──── moves, invites, matchmaking, ratings ──────┐    │
        │                                                       │    │
        ▼                                                       ▼    ▼
  engine-worker  ──── CPU search, HPA on queue depth ──────  Postgres  Redis
                                                             (truth)   (cache+bus)
```

Three Go services. One Postgres. One Redis. That's the whole system. The reason it isn't *six* services (gateway + user + game + matchmaker + rating-updater + engine) is that we tried that and most of the bugs were wire-protocol drift across service boundaries. **Splitting a system into more services doesn't make it simpler; it makes the boundaries more important and more brittle.** Default to consolidation.

`engine-worker` stays separate for a real reason: each pod does *one search at a time* (CPU-bound, parallelism = `runtime.GOMAXPROCS(0)`), and we autoscale on queue depth. If you want more search throughput, you scale *out* (more pods), not threads inside a pod. The other services can be replicated freely; they're stateless web servers.

### The service responsibilities, briefly

| Service | What it owns |
|---|---|
| **gateway** (`cmd/gateway`) | JWT auth, signup/login/profile, anonymous-cookie session for unsigned-in players, WebSocket fan-out, reverse-proxy to game-service, serves the SPA. The only service with `JWT_SECRET`. |
| **game-service** (`cmd/game`) | All authoritative game state. Move validation, invites, matchmaking, Glicko-2 rating updates, draw/takeback/rematch protocols, replay generation. |
| **engine-worker** (`cmd/engine-worker`) | Reads search requests from a Redis Stream, runs the engine search, writes results back. Also has a CLI mode (`-uci`) so the same binary can be plugged into chess GUIs like Arena. |

### Why Postgres *and* Redis

You probably learned them as alternatives. In a real system they do different jobs:

- **Postgres = durable truth.** If the cluster vanishes and you bring it back, Postgres is what survives. Every `games` row, every `users` row, every result of every move lives here. Use it for anything you can't reconstruct.
- **Redis = hot cache + message bus + lock store.** Fast (in-memory), but a single instance — if it dies, the *cache* is cold but the *truth* is fine. Used for: write-through caching of hot game rows, distributed locks (`SETNX`), Pub/Sub for ephemeral browser updates, and Streams for durable cross-service messaging.

A move flows like this: the handler locks the game in Redis → reads the game row from Redis cache (or falls back to Postgres) → applies the move in memory → writes back to **Postgres first**, then updates the Redis cache → publishes the event to Pub/Sub for live browsers. Postgres is the source of truth; Redis is the speed layer.

---

## 3. The mental jumps from CS to production

This is the part you can't get from a textbook. Each subsection is something that bit us in production at least once.

### 3.1 "Works locally" is not enough

Your dev machine runs one copy of each service. Production runs *N* copies behind a load balancer, where N changes during the day as the autoscaler reacts to load. Two requests for the same game arrive at two different pods. Both pods think they own the truth. Without coordination, they trample each other.

The implication: every change has to survive **(a) multi-replica deployment**, **(b) rolling restarts** (during deploys we kill pods one at a time and bring up new ones — for a few seconds, traffic hits a mix of old and new code), and **(c) an HTTPS reverse proxy in front** (Traefik terminates TLS and forwards plain HTTP, which means anything that depends on the client's TCP socket or IP needs `X-Forwarded-For` handling).

When you write a new endpoint, ask yourself: *what happens if two replicas process this simultaneously? what happens if the pod dies mid-request? what happens if the request is retried?*

### 3.2 Distributed locks (Redis `SETNX`)

You learned about mutexes that protect a critical section inside one process. Across pods, you need a *distributed* lock — something all replicas can see and respect.

We use the standard Redis idiom: `SET key value NX PX <ms>` to acquire (atomic "set if not exists" with a TTL so a crashed holder eventually releases), and a tiny Lua script to release only if the token still matches yours (so you don't accidentally release someone else's lock if your TTL expired and another holder took over).

```go
// every code path that reads-then-writes a game's row holds this lock
unlock, err := acquireGameLock(ctx, redis, gameID, 5*time.Second)
if err != nil { return err }
defer unlock()
// ... read game, mutate, write back ...
```

See `cmd/game/lock.go`. The "token + Lua release" pattern is industry-standard; the paper to read if you want depth is Martin Kleppmann's critique of Redlock and the responses to it. For our scale (one Redis, low contention), `SETNX` is fine.

### 3.3 Leader election

Some jobs should run *exactly once* in the cluster, not once per pod. Examples here: the matchmaker pairing loop (otherwise two pods pair the same player twice), the invite expiry sweeper, the clock flag-fall sweeper.

We do leader election by having every pod try to acquire `SETNX mm:leader <pod-id>` with a short TTL, refreshing it as long as it holds. The pod that wins runs the loop; the others sleep and retry. If the leader dies, the TTL expires and another pod takes over within seconds.

This is a primitive form of leader election. Real systems use Raft (Postgres replicas, etcd, Consul). For our scale, Redis-with-TTL is fine — the cost of a brief leaderless gap is "matchmaking pairs run a few seconds late," not "data corruption."

### 3.4 Redis Streams vs Pub/Sub vs plain key/value

Three different Redis features, three different jobs.

- **Plain GET/SET (and HSET hashes):** key-value cache. Fast lookup. Used for game state cache (`game:state:{id}`), clocks (`clock:{id}`), locks, sessions, rate-limiter buckets.
- **Pub/Sub:** *ephemeral* fan-out. Publisher sends, all current subscribers receive, nothing is stored. If you reconnect 1 second later, you missed it. We use this for the "live move arrived, push it to the browser" path — if a browser reconnects mid-game, it just re-fetches state via `GET /api/state`.
- **Streams + consumer groups:** *durable* queue with at-least-once delivery, replay, and acknowledgment. Producers append; consumers read; unacknowledged messages can be re-claimed by another consumer if the original one dies. This is what Kafka does. We use it for engine search dispatch (`engine:requests`, `engine:results`) and for cross-service command intent (`game:commands`).

**The rule:** ephemeral live updates → Pub/Sub. Anything where losing the message is unacceptable → Stream. Don't mix them. The wire contract spells out which channel each event flows over (`pkg/wire/CONTRACT.md`).

We learned this the hard way: `engine:results` started life as Pub/Sub. A game-service restart during a search dropped the engine's reply on the floor, and the SPA hung forever waiting for a move that had been computed and discarded. Promoted to a Stream in commit `fa76c2f`.

### 3.5 The trust boundary: JWT and `X-User-ID`

In school you probably wrote auth as "check the password, set a session cookie." Production auth has more moving parts because (a) you don't want every service to know the password-hashing secret, and (b) you want stateless verification.

The pattern here:

1. User logs in → gateway checks bcrypt hash against the DB → gateway signs a JWT with `JWT_SECRET` → JWT goes into an HttpOnly cookie called `token`.
2. On every subsequent request, the browser sends the cookie. The gateway validates the JWT signature (only the gateway has `JWT_SECRET`), extracts the user ID.
3. When the gateway proxies the request to `game-service`, it strips any incoming `X-User-ID` header and *injects* the validated user ID: `r.Header.Set("X-User-ID", strconv.FormatInt(user.UserID, 10))`.
4. `game-service` trusts the `X-User-ID` header and does not validate the JWT.

This is the **trust boundary pattern**. Only one service holds the cryptographic secret. Downstream services trust headers set by the trust boundary, because the only way a request reaches them is through the gateway. If you skipped this pattern and let the frontend supply `user_id`, *any caller could read anyone's games*.

Two corollaries you'll meet:

- Anonymous play uses the same pattern with a different header: `X-Anon-ID`, sourced from a `chess-anon` HttpOnly cookie minted on first hit. Same trust boundary, different identity.
- **Authoritative data never comes from the request body when the server already knows it.** Matchmaker rating? Server reads it from the DB. Game owner? Server checks the game row. The frontend can lie about everything; the gateway can lie about nothing because it just verified the JWT.

### 3.6 Why we hash passwords with bcrypt, not SHA-256

You probably learned SHA-family hashes as "cryptographically secure." For password storage they are catastrophically wrong because they're *fast*. An attacker who steals the password DB can compute billions of SHA-256 candidates per second on a GPU and brute-force most user passwords in hours.

bcrypt (and scrypt, argon2) are designed to be *slow* and *memory-hard*. They include a per-password salt (so identical passwords hash differently, and rainbow tables don't work) and a tunable work factor (so you can make hashing slower as hardware gets faster). We use bcrypt because it's well-supported in Go's standard library ecosystem and good enough for our scale. If we were a bigger target, argon2id would be the modern default.

See `pkg/auth/auth.go`.

### 3.7 WebSockets and the middleware-composition gotcha

WebSockets are HTTP upgrades: the request comes in as HTTP, the server returns `101 Switching Protocols`, and then the *underlying TCP socket* is handed to the WebSocket library, which speaks its own framing on top.

The "hand the socket over" step happens via the `http.Hijacker` interface. The library calls `Hijack()` on the `ResponseWriter`, gets the raw TCP connection back, and goes from there.

Here's the gotcha. Suppose you write a middleware that wraps `http.ResponseWriter` so you can record the status code for metrics:

```go
type statusRecorder struct {
    http.ResponseWriter
    status int
}
func (r *statusRecorder) WriteHeader(code int) {
    r.status = code
    r.ResponseWriter.WriteHeader(code)
}
```

That embedding only exposes the methods on `http.ResponseWriter`. It does **not** automatically expose `Hijack()` even if the underlying writer implements it. So when `gorilla/websocket` does the upgrade, it sees your wrapper, type-asserts to `http.Hijacker`, fails, and every WebSocket connection dies with `websocket: response does not implement http.Hijacker`.

The fix is explicit forwarding:

```go
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
    h, ok := r.ResponseWriter.(http.Hijacker)
    if !ok { return nil, nil, errors.New("...") }
    return h.Hijack()
}
```

Same for `Flush()` (needed for SSE). This is in `pkg/metrics/metrics.go`. Read the comment block above `statusRecorder` — every middleware in this codebase must do this forwarding, or every live update silently breaks.

The general lesson: when you wrap an interface in Go, embedded methods are exposed but **non-interface methods of the concrete type are hidden**. If anyone downstream does a type assertion, your wrapper has to forward.

### 3.8 Same-origin and the CSWSH attack

`Same-origin` means: scheme + host + port all match. The browser enforces this for normal Fetch requests via CORS. WebSocket upgrades, by default, **do not** enforce same-origin — the `Origin` header is sent, but it's the *server's* job to check it.

If you skip the check, an attacker's site can open a WebSocket to your server using the victim's logged-in cookies (since cookies are sent by origin, not by the destination), then read/write the victim's data. This is "Cross-Site WebSocket Hijacking" (CSWSH).

We defend by setting `Upgrader.CheckOrigin` to a function that checks the `Origin` header against same-origin plus an explicit `ALLOWED_WS_ORIGINS` env-var allow-list. See `cmd/gateway/ws.go:checkWSOrigin`. The same logic applies to any cookie-authenticated WebSocket anywhere.

### 3.9 Metric cardinality

Prometheus counters look like one metric, but in reality they're a *family* — one time-series per unique combination of label values. A counter labeled by `route` looks innocent until you let `route` be the *raw URL* including IDs: `/api/games/12345/move`, `/api/games/12346/move`, ... and now you have a million series, your Prometheus dies, your Grafana dashboards take 60 seconds to render, and your retention drops to two days.

The rule: **labels must have bounded cardinality**. For HTTP routes, that means templated paths (`/api/games/{id}/move`) not resolved ones. Go 1.22's enhanced `ServeMux` does this for us: when you register `mux.HandleFunc("POST /api/games/{id}/move", ...)`, the matched request has `r.Pattern == "POST /api/games/{id}/move"`. We label by `r.Pattern`, and any request that arrives without a Pattern goes into a fixed `<unknown>` bucket so cardinality stays bounded. A spike on the `<unknown>` panel = somebody registered a handler without a `Method /path` declaration.

See `pkg/metrics/metrics.go:HTTPMiddleware`. The same discipline applies to every label everywhere: `time_control` is fine (10 values), `user_id` would be a disaster.

### 3.10 Schema migrations as code, not commands

In some shops, migrations are sequential SQL files run by a tool. Here, the schema is a single idempotent file (`pkg/db/schema.sql`) that *every service applies on boot*, guarded by a Postgres advisory lock so multiple replicas racing to boot serialize their apply.

`CREATE TABLE IF NOT EXISTS …` is idempotent. `ALTER TABLE … ADD COLUMN IF NOT EXISTS …` is idempotent. Dropping a column is also idempotent if you write `DROP COLUMN IF EXISTS` and grep first to make sure nothing reads or writes it anywhere.

The advantage: no separate migration tool, no migration version table to drift, no "did the migration run on staging?" questions. The constraint: you have to think in terms of *additive, idempotent* schema changes. Renames become "add new column, dual-write, backfill, drop old column" sequences, not in-place renames.

### 3.11 Why we read kubectl logs, not Sentry or Datadog

Production observability here is `kubectl logs` (raw text) plus Prometheus/Grafana (numbers). No APM, no log-aggregation SaaS. That means:

- **Log a sentence, not a fragment.** `slog.Info("matchmaker paired", "white", w, "black", b, "tc", tc)` becomes a JSON line a human can grep. Decisions should be logged at INFO; routine successes should not be.
- **Log on direction changes.** When the matchmaker switches a player from "queued for human" to "engine fallback," log it. When the engine returns a result, log the move and the eval. When auth rejects something, log why.
- **Errors get the cause.** `slog.Error("xadd failed", "err", err, "stream", "engine:requests")` — never bare `log.Println(err)`.

The user (your collaborator) reads logs on the VM. They will not see your `fmt.Println` debugging in production. Use `slog`.

---

## 4. A move, from button to board

Here is what happens when the user clicks a square and a piece moves. This trace touches almost everything above.

1. **SPA** sends `POST /api/games/{id}/move` with body `{"from":"e2","to":"e4"}`. Cookie `token=<JWT>` rides along.
2. **Gateway** middleware reads the cookie, validates the JWT signature against `JWT_SECRET`, extracts user ID, sets `r.Header["X-User-ID"] = "42"`, strips any incoming `X-User-ID` the client may have tried to spoof.
3. **Gateway** reverse-proxies via its shared bounded `*http.Transport` to `game-service`. Prometheus middleware records start time.
4. **game-service** receives the request, reads `X-User-ID=42`, looks up the game via `acquireGameLock("game:lock:{id}")` → cache read on `game:state:{id}` → fall through to Postgres if cache miss.
5. Validates: does user 42 own this game? Is it their turn? Is the move legal? (`userOwnsGame` returns 404 to non-participants — *not* 403, because that would leak existence of the game.)
6. Applies the move via `pkg/core`, updates clock fields, persists the new game state: **Postgres write first, then Redis cache update**. Publishes a `game.evt.{id}` Pub/Sub message with the new state.
7. **Gateway hub** has a `SUBSCRIBE game.evt.42` running (the first WebSocket client for game 42 caused it to subscribe; the last disconnect will cause `UNSUBSCRIBE`). The hub receives the message and fans it out to every local WebSocket connected to game 42.
8. **SPA** receives the WS frame, replaces the board state, re-renders, plays the move sound.
9. If the opponent is an engine: `game-service` *also* writes an `engine:requests` Stream entry. `engine-worker` (one of N pods) reads it via its consumer group, runs the search, writes the result to `engine:results`. `game-service` reads `engine:results` and loops back to step 5 with the engine's move.

Every single step has a failure mode. The lock prevents two replicas from both applying a move. The "Postgres first, then cache" order prevents stale reads after a crash. The Pub/Sub fan-out means a reconnecting client can miss a frame but recover via `GET /api/state`. The Stream for engine results means a `game-service` restart doesn't lose the engine's reply. Read `CLAUDE.md`'s invariants section and you'll see each of these called out.

---

## 5. How to actually do work

### 5.1 The dev loop

```bash
# Make sure cmd/gateway/dist/ exists so go:embed is happy on backend-only changes
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html

# Quick build of everything
go build ./...

# Run a single service binary
go build -o /tmp/gateway ./cmd/gateway && /tmp/gateway

# Tests
go test -v ./pkg/... ./cmd/...

# A specific test
go test -run TestSingleLeader ./pkg/leader/...

# Format gate — CI fails if this prints anything
gofmt -l .

# Lint
golangci-lint run --config infra/.golangci.yml

# Frontend
cd frontend && npm run build

# UCI smoke — quick way to make sure the engine works at all
printf 'uci\nposition startpos\ngo depth 4\nquit\n' | ./chess-worker -uci
```

There is **no Justfile** despite docs that mention one — direct commands only.

### 5.2 Where to find what

- `cmd/gateway/` — HTTP/WS edge, auth surface
- `cmd/game/` — game state machine, matchmaking, rating updates
- `cmd/engine-worker/` — CPU search + UCI CLI mode
- `pkg/core/` — **pure chess engine, zero dependencies.** This is the core IP. Do not introduce third-party packages here.
- `pkg/db/queries/queries.sql` — sqlc input. After editing, run `sqlc generate -f infra/sqlc.yaml`. Never hand-edit `pkg/db/gen/*`.
- `pkg/db/schema.sql` — single source of truth for the schema.
- `pkg/wire/CONTRACT.md` — *the* wire contract. Every endpoint, event, payload. Edit in the same commit as any new wire surface.
- `frontend/src/` — Vue 3 SPA. Components, stores (Pinia), API client.
- `infra/deploy.yaml` — single Kubernetes manifest with all three Deployments + ingress + PVCs.
- `CLAUDE.md` — operator's reference, invariants, debugging cheatsheet.
- `ROADMAP.md` — shipped / queued / deferred status board.

### 5.3 Tests

`pkg/...` has the unit tests you'd expect — chess rules, PGN encode/decode, Glicko-2 math (numerically verified against the original paper).

`cmd/game/` is a more recent test suite. The pattern is composable in-memory stores: `panicStore` panics on any call (the default — failing loudly tells you what the test should be stubbing), then you compose a `gameStore` that overrides only the methods your test actually uses. `miniredis` stands in for Redis. See `cmd/game/testhelpers_test.go`, `cmd/game/lock_test.go`, `cmd/game/handle_move_test.go`. This is a useful pattern in general: test doubles that loudly fail on unexpected use catch more bugs than test doubles that silently return zero values.

### 5.4 Committing

- Create new commits, don't amend (failed pre-commit hooks mean the commit didn't happen — amending would modify the *previous* commit).
- Never `--no-verify` to skip hooks; never `--force` to main.
- Conventional commits: `feat(scope): summary`, `fix(scope): summary`, `refactor(scope): summary`, `docs: summary`. Bodies in HEREDOC so formatting survives.

### 5.5 Deploying

CI is GitHub Actions. On push to main: build the unified Docker image, push to `ghcr.io/neoromantics/chess`, then a self-hosted runner on the VM does `kubectl apply -k infra/` + `kubectl rollout restart` on each Deployment.

**You do not run `kubectl` yourself.** That's your collaborator's role (operating the cluster). You commit code; CI ships it. If a deploy fails, the next message on your screen will be them showing you `kubectl logs` output.

---

## 6. Common gotchas (read these once so you recognize them later)

- **"Live updates stopped working after my middleware change."** Your middleware wrapped `http.ResponseWriter` and didn't forward `Hijack()`. See section 3.7.
- **"My new endpoint shows up as `<unknown>` in the metrics dashboard."** You registered it without a `Method /path` pattern. Use `mux.HandleFunc("POST /api/foo", handler)`, not bare `mux.HandleFunc("/api/foo", handler)`.
- **"Postgres says `28P01` (auth failed) on every pod."** Credential drift between `chess-secrets` and the persistent volume. Postgres only honors `POSTGRES_USER`/`PASSWORD` on the *first* init of the data directory. Rotating credentials requires wiping `chess-db-pvc`.
- **"Worker is stuck `Completed` and doing no work."** Historically this was `runtime.NumCPU()` oversubscribing inside a cgroup, or the UCI CLI mode auto-detecting and reading EOF. Both fixed; if it returns, look in `cmd/engine-worker/main.go`.
- **"401 everywhere after I logged in."** Gateway is missing `JWT_SECRET` env, so `loadSecret()` falls back to an ephemeral random key per pod. Two pods → two keys → tokens from one are invalid on the other.
- **"Engine plays but its move never shows up in the SPA."** Means a result is being dropped between `engine-worker` and `game-service`. Check `cmd/game/engine_results.go` — it should be reading a *consumer group* on the `engine:results` Stream, not Pub/Sub.
- **"SPA loads the old version after a deploy."** Browser cached the bundle. Hard refresh first to confirm; fix is correct `Cache-Control` headers on the gateway's static-asset path.
- **Confirm dialogs.** Don't reach for `window.confirm` — it can't be styled, browsers block it in some embedded contexts, and it doesn't match the theme. Use the singleton Pinia confirm modal: `useConfirmStore().ask({title, message, confirmLabel, danger}) → Promise<boolean>`. Mounted once in `App.vue`.

---

## 7. The reading list

Read in this order:

1. **`CLAUDE.md`** at the repo root. Operator's reference. Treat it as authoritative for invariants — if this doc and CLAUDE.md disagree, CLAUDE.md wins.
2. **`pkg/wire/CONTRACT.md`**. The wire protocol. Every endpoint, every event, every payload shape. The frontend and backend both reference it.
3. **`ROADMAP.md`**. Shipped, queued, deferred. Look here before proposing a feature — it might already be on the list with a reason it hasn't shipped yet.
4. **`README.md`**. The public-facing summary. Shorter than CLAUDE.md, more polished.
5. **`infra/deploy.yaml`**. The whole Kubernetes deployment in one file. Reading this teaches you what env vars each service needs, how the HPAs are configured, where secrets come from.

External material that will pay off here:

- *Designing Data-Intensive Applications* (Kleppmann). The book to read on distributed systems if you only read one. Especially chapters 5 (replication), 7 (transactions), 9 (consistency and consensus).
- The Redis docs on Streams + consumer groups (the official docs are unusually good).
- Go's `net/http` source — specifically the `ResponseWriter`, `Hijacker`, `Flusher` interfaces. Read `httptest.ResponseRecorder` for a reference wrapper.
- Glicko-2 paper (Glickman). Short, readable, and the comments in `pkg/rating/glicko2.go` reference its equation numbers.

---

## 8. The way we work

A few cultural notes that will save you cycles:

- **Investigate first, then ship.** When asked "where can we improve this?", produce a prioritized list with no code. Wait for explicit greenlight before shipping. Ship items one at a time with build + test + format gates between, not as one giant PR.
- **Don't add abstractions you don't need.** Three similar lines is better than a premature framework. A bug fix doesn't need surrounding cleanup. A one-shot operation doesn't need a helper.
- **Don't write defensive code for impossible cases.** Trust internal callers. Validate at system boundaries (user input, external APIs), not at every layer.
- **Default to no comments.** The code says *what*. Comments say *why*, when the why is non-obvious — a hidden constraint, a workaround for a specific bug, surprising behavior. If removing the comment wouldn't confuse a future reader, don't write it.
- **Ask before doing irreversible things.** Reading and searching are free. Deleting, force-pushing, sending messages, modifying shared infrastructure all need confirmation.
- **Update docs in the same commit as the work.** If you ship a new endpoint, update `pkg/wire/CONTRACT.md`. If you ship a new invariant, update `CLAUDE.md`. Stale docs send the next person down a wrong path.

---

## 9. Deeper dives (Redis + WebSocket internals)

These extend sections 3.4 and 3.7. They came up in real onboarding conversations — if the earlier sections felt like enough, skim and skip. If you wanted more, this is the more.

### 9.1 What actually lives in Redis here

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

### 9.2 Redis data structures: the full menu

"In-memory hashmap" undersells it. Redis is really a *library of hand-picked data structures*, each with atomic operations implemented in C — that's most of why people reach for it over a SQL database.

| Type | Shape | Where it lands |
|---|---|---|
| **String** | `key → bytes` | Counters (`INCR`), locks (`SET NX PX`), JSON blob caches |
| **Hash** | `key → {field: value, …}` | A record where fields update independently. We use it for `clock:{id}` so a clock tick doesn't rewrite the whole blob |
| **List** | doubly-linked, push/pop both ends, blocking variants (`BLPOP`) | Queues/stacks. We use Streams instead |
| **Set** | unordered unique members + set algebra (`SUNION`, `SINTER`, `SDIFF`) | Membership tests across populations |
| **Sorted Set (ZSet)** | unique members + `float64` scores, sorted by score, range queries are O(log N + M) | Priority queues, leaderboards, time-window indexes. We use it for matchmaking (by rating) and flag-fall (by deadline) |
| **Stream** | append-only log + offsets + consumer groups; unacked messages can be `XCLAIM`ed by another consumer | Kafka-lite. Engine work, game-commands |
| **Pub/Sub** | ephemeral fan-out channels (not stored) | Live WebSocket updates |
| **HyperLogLog** | probabilistic cardinality, ~12 KB regardless of input size, ~0.81% error | "Unique daily active users" without storing user IDs |
| **Bitmap / Bitfield** | bit-addressable string | "Has user N done X today?" at one bit per user |
| **Geo** | lat/lng members + radius queries (built on ZSets via geohashes) | "Players within 50 km" |

### 9.3 The structure IS the index

This is the framing shift that distinguishes Redis from "SQL but faster," and it's the one most worth internalizing.

In Postgres, the table and an index are separate physical structures. Data lives in the heap; an index is one access path. The query planner picks an index at query time. You can add new indexes after the fact without changing any write code. A query the planner can't index just falls back to sequential scan — slower, but it still answers.

In Redis, **the structure IS the data IS the index.** A ZSET keyed by rating answers a whole family of related queries off the same sort key — top-N, range, rank, score lookup — all O(log N) or O(1). What it cannot answer is anything about a *different dimension*: querying the same members by registration date is *impossible from this ZSET*, because that information isn't in it.

So in Redis you pre-build a structure *per access dimension*, and to support a new access pattern you add a new structure and a new write path that keeps it in sync. The matchmaker is the canonical example: `mm:queue:{tc}` is a ZSET indexed by rating (so `ZRANGEBYSCORE` does rating-window pairing in O(log N + M)), and `mm:joined:{tc}` is a Hash indexed by user (so we can compute per-user wait time and trigger the 10s engine fallback). Two structures, two access patterns, both updated together on every join/leave:

```go
// cmd/game/matchmaker.go — every join writes both
s.bus.Rdb().ZAdd(ctx, queueKey(tc), redis.Z{Score: float64(rating), Member: uidStr})
s.bus.Rdb().HSet(ctx, joinedKey(tc), uid, time.Now().Unix())
```

The costs: memory duplication, write-path complexity, no foreign keys, no `ON DELETE CASCADE` — application code owns consistency. The rewards: predictable latency (no query-planner surprises), atomic operations against the structure (no row locks, no MVCC), and microsecond reads. You take this trade for **known, hot, repeated** access patterns. Ad-hoc queries you didn't anticipate go to Postgres.

A related framing trap: Redis isn't only "SQL questions made faster." A large slice of *why we have Redis at all* is things SQL is bad at — distributed locks, leader election, Pub/Sub fan-out, durable inter-service queues, TTL on everything. Those aren't SQL-shaped problems made fast; they're a different category of problem that happens to fit in a key-value server.

### 9.4 Pub/Sub: the gateway-as-relay topology

Section 3.4 covered "ephemeral vs durable." Two further details are load-bearing.

**Browsers are not Redis clients.** "Client" in Redis-speak means anything that holds a TCP connection to `redis-server` and speaks the wire protocol — a Go service, `redis-cli` from a debug shell, your laptop. In production, the only Redis clients are pods inside the cluster. Browsers connect to *gateway pods* via WebSocket; each gateway pod opens **one** long-lived Pub/Sub connection to Redis (the hub goroutine) and acts as a *relay* between Redis Pub/Sub and per-browser WebSockets.

```
                                       Pub/Sub conn         WebSocket
game-service ──PUBLISH game.evt.42──▶ Redis ──────▶  gateway pod  ──────▶  browser
                                                  (Redis client)         (WS client)
```

This indirection earns its keep: Redis isn't exposed to the public internet; connection counts stay sane (one Redis conn per gateway pod fans out to thousands of browser sockets, instead of one Redis conn per browser); per-channel auth (only deliver `game.evt.42` to a browser whose JWT proves it may watch game 42) lives in the gateway because Redis has no concept of "users"; and the per-channel ref-counted subscription pattern below only works because the gateway is the unit that subscribes.

**Per-channel SUBSCRIBE, never PSUBSCRIBE wildcards.** The obvious shortcut — each pod does `PSUBSCRIBE game.evt.*` at boot, filters locally — works perfectly at small scale and silently destroys you at large scale. Pub/Sub fan-out cost is `O(messages × subscribers)`. With wildcards, *every gateway pod is a subscriber to every channel*, even ones nobody local cares about. At 5 pods × 1000 active games × 2 moves/sec, that's 10K Pub/Sub messages/sec — most of them delivered to pods that immediately throw them away.

The harder, correct thing: when the first local WebSocket for game 42 connects to pod A, the hub bumps a ref count and (if it hit 1) issues `SUBSCRIBE game.evt.42`. When the last local browser for game 42 on pod A disconnects, the count drops to 0 and the hub issues `UNSUBSCRIBE`. Fan-out becomes `O(messages × pods-with-a-live-subscriber)` instead of `O(messages × all-pods)`. The wiring is in `cmd/gateway/hub.go` — the comment at the top of the file calls this out as a scale-design note.

**Multi-pod consequence.** Each gateway pod independently maintains its own in-memory map of "which local WebSockets care about which game/user" and its own Redis subscriptions. There is no shared "who's watching what" state across pods. A browser is sticky to whichever pod accepted its WebSocket upgrade — that pod is the only one that knows about it. If the pod dies, the WebSocket dies, the browser auto-reconnects, the load balancer routes it to a different pod, and that pod runs the SUBSCRIBE-and-attach dance from scratch. The browser misses 1–3 frames during the gap and recovers because the SPA also calls `GET /api/state` on reconnect. **Pub/Sub is the speed layer; REST is the correctness layer.** If you ever publish without a preceding durable write, the next deploy will lose a message and the user will see a bug.

```
                            Traefik (Ingress / LB)
                            /         |          \
            ┌──────────────┘          │           └──────────────┐
            ▼                         ▼                          ▼
       gateway pod A              gateway pod B              gateway pod C
       Alice (WS, game 42)        Bob (WS, game 42)          Carol (WS, game 99)
       hub: SUBSCRIBE             hub: SUBSCRIBE             hub: SUBSCRIBE
         game.evt.42                game.evt.42                game.evt.99
            │                         │                          │
            └─────────────────────────┴──────────────────────────┘
                                      │
                                      ▼
                                    Redis
                                      ▲
                                      │  PUBLISH game.evt.42
                                      │
                              ┌───────┴───────┐
                              │ game-service  │
                              └───────────────┘
```

Game-service publishes once. Redis fans out only to pods A and B (the two that have a subscriber). Pod C never sees the message because it never subscribed. Pod A's hub looks up its local map, finds Alice's WebSocket, writes the frame; pod B does the same for Bob. Carol is undisturbed.

A useful debugging consequence of "client = anything on a TCP socket": `kubectl exec` into any pod and run `redis-cli -h chess-redis SUBSCRIBE 'game.evt.42'` to watch live traffic with your own eyes. You're now a Pub/Sub client too, sitting alongside the gateway. If `redis-cli` sees the frame and the browser doesn't, the bug is in the gateway-side relay, not in game-service.

---

## 10. What "the cloud" buys you (and what it costs)

This system runs on a single VM at Duke. That's deliberate — it's free, it's enough for a school project, and one operator reads `kubectl logs` directly. But "deploy to AWS" comes up in every interview, so it's worth understanding what that phrase actually means for *this* stack.

### 10.1 What AWS actually is

A buffet of ~200 managed services. You don't "deploy to AWS" wholesale — you pick which pieces of your infrastructure to swap for AWS-operated versions. The pitch on each one: instead of running Postgres yourself (backups, replication, failover, patching), pay AWS to run it and treat it as a hostname-and-credentials. Same for Redis, k8s control plane, DNS, certificates, secrets, load balancers, CDN, registry.

GCP and Azure offer near-identical menus with different names. Picking between them is mostly a function of existing org relationships, not capability.

### 10.2 Mapping our current stack to AWS

| What we run today | What AWS gives us | What the handoff buys |
|---|---|---|
| One Duke VM | EC2 Auto Scaling Group, or EKS-managed nodes | Cluster spans multiple AZs (independent datacenters in one region) — one DC failing doesn't take us down |
| k3s, we admin the control plane | EKS (managed k8s) | AWS runs etcd, the API server, the scheduler |
| Postgres as a k8s Deployment + PVC | RDS PostgreSQL with Multi-AZ | Synchronous standby in another AZ, automatic failover (~60s), daily snapshots + 5-min point-in-time recovery, optional read replicas for `ListGames`/search |
| Redis as a k8s Deployment + PVC | ElastiCache for Redis | Replication + automatic failover (replaces the "Redis Sentinel" roadmap item), automated snapshots |
| Traefik + Let's Encrypt | ALB + ACM | Free auto-renewing TLS certs, native WebSocket support, integrates with WAF |
| `go:embed` SPA into the gateway binary | S3 + CloudFront (CDN) | SPA served from edge POPs globally; gateway image stops growing when frontend changes; SPA-only deploys don't touch k8s |
| `chess-secrets` k8s Secret | Secrets Manager / Parameter Store | Auto-rotation, audit log, KMS encryption at rest |
| ghcr.io for images | ECR | Faster pulls from EKS, vulnerability scanning, IAM-integrated |
| `kubectl logs` | CloudWatch Logs (or keep Prometheus) | Searchable retention, log-based alerts — at the cost of CloudWatch's mediocre UX |
| No DR plan | RDS snapshots + cross-region replication | An actual disaster-recovery story |
| No DNS we own | Route 53 | We own the domain. Today we're on `vcm-50800.vm.duke.edu` — when Duke takes the VM back, the domain goes with it |

### 10.3 What it specifically fixes for our system

Walk through the failure modes called out in CLAUDE.md and the roadmap:

1. **Redis is a SPOF.** Today, if Redis dies, every cache read misses, every live update drops, matchmaking queues vanish. AOF gives durability on disk but not failover. **ElastiCache with a replica** gives automatic failover in ~30s. Biggest reliability win — it's been on the roadmap as "Redis Sentinel" forever, deferred because we don't have a second node.
2. **Postgres is a SPOF.** Same problem, worse consequences (source of truth). **RDS Multi-AZ** gives a synchronous standby in another AZ and ~60s failover.
3. **The whole VM is a SPOF.** Duke pulls the VM offline for maintenance → cluster gone. **EKS across 3 AZs** means an entire AZ can fail and the cluster keeps serving.
4. **No backup story.** It's not actually clear what would happen if `chess-db-pvc` corrupted right now. **RDS** does daily snapshots + 5-min PITR built in, zero code.
5. **Engine search bursts are painful to scale out.** Today an analysis storm fills our 8-pod HPA ceiling on the VM. **EKS + Spot instances + Cluster Autoscaler** lets engine-worker scale to 50+ pods for a 30-min burst, then scale back to 2. Spot pricing is ~30% of on-demand, and engine-worker is the textbook Spot workload (interruptible, parallel, stateless).
6. **Frontend bundle ships with every gateway change.** Every backend tweak rebuilds the SPA and bloats the Docker image. **S3 + CloudFront** decouples the SPA — SPA-only changes deploy in seconds without touching k8s.
7. **Pod-level secrets are coarse.** Everything in `chess-secrets` is visible to every pod that mounts it. **IRSA (IAM Roles for Service Accounts)** gives per-pod credentials — game-service has a role that can read game tables; gateway has a separate role for user tables. Least privilege without a secret-per-service.
8. **TLS cert renewal could break.** Let's Encrypt is great until it isn't (rate limits, DNS challenge weirdness). **ACM** is "click yes" and AWS handles it forever.

### 10.4 What you give up

In priority order — be honest about these in any interview:

1. **Cost.** Duke VM = $0/month. A barebones equivalent AWS footprint for our scale: EKS control plane (~$73/mo) + 3× t3.medium nodes (~$90/mo) + RDS db.t4g.small Multi-AZ (~$60/mo) + ElastiCache cache.t3.micro with a replica (~$25/mo) + ALB (~$20/mo + traffic) + Route 53 + S3 + data transfer. Realistic floor for *barebones* prod is **$300–500/month**. For a hobby project, that doesn't change.
2. **Complexity surface.** EKS is harder than k3s. The IAM + VPC + Security Group + Subnet design is a multi-day project to get right. AWS-flavored networking (private vs public subnets, NAT gateways, VPC peering, endpoint policies) is its own discipline. For 3 services this is overkill; for 30 it's table stakes.
3. **Vendor lock-in by tier.** Some AWS services are "standard X with a billing relationship" — RDS Postgres is plain Postgres, `pg_dump` and walk away. Some are AWS-specific — DynamoDB, SQS, CloudWatch alarms, IAM policies. The further down the stack you go (managed DBs → app services → IAM model), the harder it is to leave. Stay close to open standards if you care about portability. `EKS + RDS Postgres + ElastiCache Redis` is portable; `EKS + DynamoDB + SQS + EventBridge + Lambda` is not.
4. **Different debugging surface.** `kubectl logs` still works, but incident response moves to CloudWatch dashboards, X-Ray traces, IAM audit logs. The mental model shifts from "SSH into a box and grep" to "click through five web consoles."
5. **Simple things become forms with thirty fields.** "Add a Postgres user" goes from `CREATE USER` to IAM role + RDS auth config + database role + grant matrix + secret rotation policy. For our scale this is friction; at fintech scale it's compliance.

### 10.5 The mental shift

AWS doesn't make our application better. It makes our **failure modes** better — and only for the failure modes we're willing to pay for. The single-VM Duke setup is *correct* for a school project with one operator reading `kubectl logs`. The moment a real userbase would notice if the platform went down for an afternoon, AWS (or GCP, or Azure — substitutes for this purpose) starts paying off.

The framing to carry: **AWS is a list of "things I no longer have to operate."** Each service is a tradeoff — money + a bit of opacity, in exchange for reliability + scale + features you'd otherwise build yourself. Picking *all of them* is how startups burn through a seed round on a 100-user app.

For *this* system, the high-leverage moves, in order, would be: RDS for Postgres (fixes backups + Multi-AZ) → ElastiCache for Redis (fixes the SPOF) → ALB + ACM (removes Let's Encrypt operational risk) → S3 + CloudFront for the SPA (decouples frontend deploys). Everything else — EKS vs self-hosted k3s on EC2, IRSA, CloudWatch vs Prometheus — is secondary and largely a matter of taste.

---

Welcome aboard.
