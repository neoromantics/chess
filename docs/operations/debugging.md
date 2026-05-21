# Debugging

## kubectl cheatsheet

```bash
# Switch namespace once per shell so we stop passing -n
kubectl config set-context --current --namespace=chess

# Pod state across all services
kubectl get pods

# Why a service is crash-looping (gets the LAST exit's logs)
kubectl logs -l app=chess-<service> --tail=80 --previous

# Live logs of a specific service
kubectl logs -l app=chess-gateway --tail=80 -f

# Verify env var injection on a Deployment
kubectl get deploy chess-gateway -o jsonpath='{.spec.template.spec.containers[0].env[*].name}'
```

## Common failure modes

- **Every WebSocket call fails silently** ("doesn't update live", "refresh required", browser DevTools shows WS connections in red) — almost always an HTTP middleware wrapping `http.ResponseWriter` without forwarding `Hijack()`. Gateway logs show `websocket: response does not implement http.Hijacker`. Fix: every wrapper in the middleware chain must implement `http.Hijacker` (and `http.Flusher`). The metrics middleware bit us this way once; see `pkg/metrics/metrics.go:statusRecorder`.

- **`502 Bad Gateway` on `/api/auth/*`** — gateway pod crash-looping (it owns auth now, not a separate user-service).

- **All Postgres-touching services crash-looping with `28P01`** — credential drift; usually means the `chess-db` PVC was initialized with different credentials than `chess-secrets` now holds. Fix: wipe PVC + redeploy.

- **Worker stuck in `Completed`** — pre-fix this was `runtime.NumCPU()` oversubscribing or tty-auto-detect entering UCI mode. Both are fixed; if it returns, something in `cmd/engine-worker/main.go` is exiting cleanly without an error.

- **401 "unauthorized" everywhere after signup works** — gateway is missing `JWT_SECRET` env, so its `loadSecret()` falls back to an ephemeral random key.

- **Engine plays but its move never reaches the SPA** — historically because `engine:results` was Pub/Sub (lossy under restart); now a durable Stream. If it regresses, check the consumer-group reader in `cmd/game/engine_results.go:listenToEngineResults`.

- **Image pull failures pinning a public `pgbouncer` / `bitnami` / … tag** — Docker Hub's tag publishing is unreliable. Either pin a verified digest, switch images, or in our case (low scale) skip the dependency entirely. See the in-line comment under "PGBOUNCER" in `infra/deploy.yaml`.
