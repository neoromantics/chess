# neoromantics Chess Platform

**Live:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(k3s + Traefik + Let's Encrypt on a Duke VCM)

A multiplayer chess platform built in Go + Vue 3 + Postgres + Redis, deployed to Kubernetes. Three backend services, one durable database, one in-memory bus. Frontend builds into the same binary as the gateway so a single image carries everything except the engine.

## Architecture in one picture

```
           Browser (Vue 3 SPA, embedded in gateway binary)
                                │ HTTPS / WSS
                                ▼
                        ┌──── gateway ────┐
                        │ auth, profiles, │
                        │ HTTP routing,   │
                        │ WS fan-out      │
                        └─┬───────────────┘
                          │ sync HTTP for game endpoints
                          │ Redis Streams for intent dispatch
                          ▼
                  ┌── game-service ──┐    ┌─ engine-worker ──┐
                  │ moves, invites,  │◀──▶│ CPU search       │
                  │ matchmaking,     │    │ HPA on queue     │
                  │ Glicko ratings   │    │ depth            │
                  └───┬──────────────┘    └──────────────────┘
                      │ shared state
                      ▼
                ┌── Postgres ──┐ ┌── Redis ─────┐
                │ durable      │ │ hot cache,   │
                │ truth        │ │ streams,     │
                └──────────────┘ │ pub/sub,     │
                                 │ locks        │
                                 └──────────────┘
```

Three pods scale horizontally; engine-worker has its own autoscaling profile because chess search is CPU-asymmetric. Everything else (profiles, invites, matchmaking, ratings) shares the same data and lives in the gateway or game-service binary.

## Wire-protocol contract

Every HTTP endpoint, WebSocket event type, and JSON payload shape is enumerated in **`pkg/wire/CONTRACT.md`**. The convention is to edit that doc in the same commit as any new wire surface; backend constants and frontend listeners reference it.

## Key invariants

- **Per-game lock.** Every read-modify-write on the `games` table goes through `acquireGameLock` in `cmd/game/lock.go` (Redis SETNX + token + Lua release). Concurrent moves on the same game serialize across replicas.
- **Postgres is durable truth; Redis is the hot cache.** `cmd/game/cache.go` wraps `game:state:{id}` as a write-through hash. Reads hit Redis, fall through to PG on miss.
- **Gateway injects `?user_id=N` into proxied requests.** Downstream services trust the query param; nothing else re-validates the JWT.
- **Engine results are durable.** `engine:results` is a Stream with a consumer group, not Pub/Sub. A game-service restart no longer loses an in-flight move.
- **No in-memory game state.** Every request hydrates from PG (via the cache). Multi-replica safety has no shared mutable state.
- **`pkg/core` is zero-dependency Go.** The chess search is the core IP; don't add third-party deps.

See `CLAUDE.md` for the full set of invariants and the Streams-vs-HTTP rule.

## Common operations

There is no Justfile. Direct commands:

| What | Command |
|---|---|
| Format check (CI gates on this) | `gofmt -l .` |
| Vet | `go vet ./...` |
| Tests | `go test -v ./pkg/...` |
| Build everything | `go build ./...` |
| Build one service | `go build -o /tmp/gateway ./cmd/gateway` |
| Frontend build | `cd frontend && npm run build` |
| Regenerate sqlc | `sqlc generate -f infra/sqlc.yaml` |
| UCI smoke | `printf 'uci\ngo depth 4\nquit\n' \| ./chess-worker -uci` |

Backend-only builds need a dummy frontend dir so `go:embed` finds something:
```bash
mkdir -p cmd/gateway/dist && touch cmd/gateway/dist/index.html
```

## Deployment

GitHub Actions builds the unified image and pushes to `ghcr.io/neoromantics/chess`. A self-hosted runner on the VM does `kubectl apply -k infra/` + `kubectl rollout restart` for the three Deployments.

### Bootstrap secrets

**Secrets live in k3s, not in CI.** On a fresh cluster, run once on the VM:
```bash
./infra/bootstrap-secrets.sh
```
That creates `chess-secrets` with random Postgres credentials + a JWT signing key. CI never sees these values; rotation is `kubectl edit secret chess-secrets -n chess` + `kubectl rollout restart …`.

Rotating Postgres credentials additionally requires wiping `chess-db-pvc` (Postgres only honors `POSTGRES_USER` / `POSTGRES_PASSWORD` on first init of the data dir).

## Production debugging

```bash
# Set namespace once per shell
kubectl config set-context --current --namespace=chess

# State of the fleet
kubectl get pods

# Why a service is crash-looping (LAST exit's logs)
kubectl logs -l app=chess-<service> --tail=80 --previous

# Live logs
kubectl logs -l app=chess-gateway --tail=80 -f
```

Common failure modes and where to look are in `CLAUDE.md`.

## Repository layout

```
├── cmd/
│   ├── gateway/       # HTTP/WS edge + auth + profiles (absorbed user-service)
│   ├── game/          # Game state + invites + matchmaking + ratings
│   └── engine-worker/ # CPU search, queue consumer
├── pkg/
│   ├── core/          # Pure chess engine; zero deps
│   ├── auth/          # JWT + bcrypt
│   ├── db/            # sqlc-generated types + Postgres impl + schema.sql
│   ├── eventbus/      # Redis Streams + Pub/Sub primitives
│   ├── game/          # Game state machine
│   ├── metrics/       # Prometheus instrumentation
│   ├── rating/        # Glicko-2
│   ├── uci/           # UCI protocol (CLI mode)
│   └── wire/          # CONTRACT.md — the wire-protocol source of truth
├── frontend/          # Vue 3 + TS SPA, embedded into gateway via //go:embed
├── infra/             # k8s manifests, sqlc.yaml, .golangci.yml
└── Dockerfile         # Single multi-stage build, three binaries out
```

## License
MIT
