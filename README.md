# neoromantics Chess Platform

**Live Deployment:** [https://vcm-50800.vm.duke.edu](https://vcm-50800.vm.duke.edu)
(Running on a Kubernetes/k3s cluster with Traefik and Let's Encrypt TLS)

A professional, distributed chess platform architected for commercial scale. Built with Go, Vue 3 (TypeScript), and Redis.

## 🏗 Distributed Architecture
Six independent microservices coordinate via **Event Sourcing** and **Redis Streams**, ensuring zero-lock consistency and horizontal scalability.

- **gateway** (Go) - Stateless HTTP/WS entrance. Handles JWT validation, command translation, and fan-out.
- **user-service** (Go) - Owns identities, profiles, and authentication.
- **game-service** (Go) - Authoritative domain arbiter. Consumes Commands, emits authoritative Events.
- **matchmaker** (Go) - Manages queues and pairing logic asynchronously.
- **rating-updater** (Go) - Asynchronously updates Glicko-2 ratings after game completion.
- **engine-worker** (Go) - Pure CPU calculation nodes consuming search tasks from Redis Streams.

## 🚀 Key Invariants
- **Sequential Command Processing**: All state mutations (moves, resigns) are processed via Redis Streams; no distributed locks required.
- **Authoritative Backend**: The `game-service` is the sole source of truth for move validation and clocks.
- **Durable Tasking**: Calculation nodes use acknowledged streams with consumer groups for crash recovery.
- **Optimistic Concurrency**: Database consistency is managed via Postgres MVCC.

## 🛠 Operations
Direct commands (no task runner — there is no Justfile):

| Command | Description |
|---------|-------------|
| `go test ./pkg/...` | Run the Go test suite (CI gates on this) |
| `go build ./...` | Build every service binary into the default Go cache |
| `gofmt -l .` | Format check (CI fails if non-empty) |
| `kubectl apply -k infra/` | Apply the cluster manifests (run on the VM) |
| `kubectl -n chess logs -l app=chess-gateway --tail=80` | Watch the primary entrance logs |
| `kubectl -n chess logs -l app=chess-game-service --tail=80` | Watch the authoritative game logic logs |
| `cd frontend && npm run build` | Build the Vue SPA (auto-embedded into the gateway image by Dockerfile) |
| `sqlc generate -f infra/sqlc.yaml` | Regenerate `pkg/db/gen/*` after editing SQL |

### Production Deployment
The platform uses **GitHub Actions** with a **Self-Hosted Runner** to deploy directly to the k3s cluster. Manifests in `infra/` are rendered and applied locally on the VM without requiring exposed inbound ports or SSH keys.

**Secrets are owned by the cluster, not by CI.** On a fresh VM, bootstrap once:
```bash
./infra/bootstrap-secrets.sh
```
This creates the `chess-secrets` Secret (Postgres creds + JWT signing key) in the `chess` namespace using `openssl`-generated random values. CI's deploy job only runs `kubectl apply -k infra/` and `kubectl rollout restart` — it never sees the secret values, and rotating a secret means editing the live Secret with `kubectl` rather than pushing a code commit.

Rotating Postgres credentials additionally requires wiping the `chess-db` PVC, since Postgres only honors `POSTGRES_USER`/`POSTGRES_PASSWORD` on the first init of the data directory.

## 🗺 Roadmap
- ✅ **6-Pod Microservices Fleet**: Successfully transitioned from legacy monolith.
- ✅ **Event-Sourced Core**: Eliminated locks in favor of sequential streams.
- ⬜ **OpenTelemetry**: Trace commands across the distributed fleet.
- ⬜ **Spectator Mode**: Read-only WebSocket subscriptions for public games.
- ⬜ **KEDA Autoscaling**: Scale workers based on real-time stream depth.
- ⬜ **Anti-Cheat**: Asynchronous engine correlation scans over move history.

## 📂 Repository Structure
```
.
├── cmd/                # Microservice Entry Points
├── pkg/                # Shared Domain Logic & Packages
├── infra/              # K8s Manifests, Docker Compose, SQLC Config
├── frontend/           # Vue 3 + TypeScript SPA
└── pkg/db/schema.sql   # Authoritative Postgres Schema
```

## 📜 License
MIT
