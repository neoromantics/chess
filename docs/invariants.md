# Invariants

Rules every change must respect. Violating any of these causes silent corruption, security holes, or breakage at scale. A critical subset is mirrored inline in `CLAUDE.md` so Claude has it always-loaded; this file is the full normative list.

## Wire & data

- **Streams vs HTTP rule.** Redis Streams are used for **(1) CPU-asymmetric workloads** (engine search dispatch + result delivery on `engine:requests` / `engine:results`) and **(2) cross-service intent** (matchmaker pairing on `game:commands`). Everything else — single-game mutations, invites, profile changes — uses synchronous HTTP through the gateway. **Do not put a user-initiated chess action behind a Stream**; the SPA expects each button to round-trip a new `StateJSON`. See `cmd/game/handlers.go` for the pattern. Deep dive: [`architecture/redis-patterns.md`](architecture/redis-patterns.md).

- **Per-game lock (Redis `game:lock:{id}`, SETNX + token + Lua release).** Every code path that reads-then-writes a game's row must hold this lock for the duration. With N replicas of game-service consuming the same Redis Stream, the round-robin delivery does NOT partition by game_id; two MakeMove commands for the same game could otherwise race. See `acquireGameLock` in `cmd/game/lock.go`.

- **Postgres is durable truth; Redis is the hot cache.** Game rows live in `game:state:{id}` as a Redis hash, write-through to PG. Reads go Redis-first with PG fallback. Don't store game state in process memory — that breaks multi-replica. See `cmd/game/cache.go`.

- **`pkg/core` is zero-dependency, pure Go.** The chess engine search is the core IP; do not introduce deps there.

## Auth & trust boundary

- **Gateway injects `?user_id=N` into proxied requests.** Downstream services trust this query param and do not re-validate JWTs. See `injectAuthedUser` in `cmd/gateway/main.go`. Letting the frontend supply `user_id` would let any caller read anyone's games.

- **Per-game authorization at every game-keyed endpoint.** `userOwnsGame(uid, rec)` is the predicate; non-participants get **404 (not 403)** so existence doesn't leak. The WS upgrade gate pre-flights `/api/state` with the user_id injected to enforce the same check.

- **Only gateway gets `JWT_SECRET`** (see `infra/deploy.yaml`). Gateway is the only place JWTs are signed/verified; game-service and engine-worker trust the gateway-injected `?user_id=N` query param instead.

- **Gateway pulls the matchmaker rating from the DB, never from the request body.** `handleJoinQueue` looks up `dbUser.Rating` so a 1200 player can't queue as 2400. The SPA's `api.joinQueue` no longer sends a rating field.

## Frontend & embedding

- **Frontend is embedded only in the gateway binary** (`cmd/gateway/dist/` via `go:embed`). Other services that need a built asset (e.g. game-service's replay JSON) compose with the gateway, which substitutes templates.

- **Confirm + prompt dialogs use the singleton Pinia modals, not `window.confirm` / `window.prompt`.** Both follow the same pattern: store mounted once in `App.vue` (`<ConfirmModal />` + `<PromptModal />`), callers do `useConfirmStore().ask({title, message, confirmLabel, danger}) → Promise<boolean>` or `usePromptStore().ask({title, message?, defaultValue?, confirmLabel}) → Promise<string|null>`. Esc cancels, Enter confirms. Browser-native dialogs don't match the theme and get blocked on some embedded contexts. See `frontend/src/components/{ConfirmModal,PromptModal}.vue` + `frontend/src/stores/{confirm,prompt}.ts`.

## HTTP & WebSocket plumbing

- **Any HTTP middleware that wraps `http.ResponseWriter` MUST forward `Hijack()` and `Flush()`.** `gorilla/websocket` needs `Hijack()` to take over the TCP connection during Upgrade; SSE/streaming responses need `Flush()`. Forgetting this is silent at compile time and breaks every WebSocket handler at runtime with `websocket: response does not implement http.Hijacker`. See `pkg/metrics/metrics.go:statusRecorder` for the canonical pattern.

- **Gateway hub uses per-channel SUBSCRIBE, not PSUBSCRIBE.** When the first local WS client for game G connects, the hub `SUBSCRIBE`s `game.evt.G`; on the last disconnect it `UNSUBSCRIBE`s. This keeps cross-pod fan-out cost proportional to "pods with a live subscriber" instead of "every pod gets every event". A regression to PSUBSCRIBE wildcards would silently work but blow up the bandwidth bill at scale. See `cmd/gateway/hub.go`.

- **Register HTTP routes with Go 1.22 `Method /path/{id}` patterns.** The metrics middleware (`pkg/metrics/metrics.go:HTTPMiddleware`) labels by `r.Pattern`; anything that arrives without a Pattern gets bucketed as `<unknown>` to keep cardinality bounded. A `<unknown>` spike on the Grafana panel = someone registered a handler without a Method+Pattern declaration. Prefix handlers (`mux.Handle("/api/invites/", …)`) also populate `r.Pattern` with the registered prefix, so they're fine — what's NOT fine is hand-routed dispatchers that wrap a handler without going through ServeMux's pattern matching.
