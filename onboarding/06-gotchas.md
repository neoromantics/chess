# 6. Common gotchas

Read these once so you recognize the signature next time.

- **"Live updates stopped working after my middleware change."** Your middleware wrapped `http.ResponseWriter` and didn't forward `Hijack()`. See [§3.7](03-mental-jumps/07-websocket-hijack.md).
- **"My new endpoint shows up as `<unknown>` in the metrics dashboard."** You registered it without a `Method /path` pattern. Use `mux.HandleFunc("POST /api/foo", handler)`, not bare `mux.HandleFunc("/api/foo", handler)`. See [§3.9](03-mental-jumps/09-metric-cardinality.md).
- **"Postgres says `28P01` (auth failed) on every pod."** Credential drift between `chess-secrets` and the persistent volume. Postgres only honors `POSTGRES_USER`/`PASSWORD` on the *first* init of the data directory. Rotating credentials requires wiping `chess-db-pvc`.
- **"Worker is stuck `Completed` and doing no work."** Historically this was `runtime.NumCPU()` oversubscribing inside a cgroup, or the UCI CLI mode auto-detecting and reading EOF. Both fixed; if it returns, look in `cmd/engine-worker/main.go`.
- **"401 everywhere after I logged in."** Gateway is missing `JWT_SECRET` env, so `loadSecret()` falls back to an ephemeral random key per pod. Two pods → two keys → tokens from one are invalid on the other.
- **"Engine plays but its move never shows up in the SPA."** Means a result is being dropped between `engine-worker` and `game-service`. Check `cmd/game/engine_results.go` — it should be reading a *consumer group* on the `engine:results` Stream, not Pub/Sub.
- **"SPA loads the old version after a deploy."** Browser cached the bundle. Hard refresh first to confirm; fix is correct `Cache-Control` headers on the gateway's static-asset path.
- **Confirm dialogs.** Don't reach for `window.confirm` — it can't be styled, browsers block it in some embedded contexts, and it doesn't match the theme. Use the singleton Pinia confirm modal: `useConfirmStore().ask({title, message, confirmLabel, danger}) → Promise<boolean>`. Mounted once in `App.vue`.

Canonical failure-mode index: [`../docs/operations/debugging.md`](../docs/operations/debugging.md).

---

← [`05-doing-work.md`](05-doing-work.md) · Next: [`07-reading-list.md`](07-reading-list.md)
