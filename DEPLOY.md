# Deployment Guide: neoromantics Chess Platform

This platform is designed for professional Kubernetes deployment via **GitHub Actions** to a **k3s** cluster.

## 🏗 Prerequisites
- **Kubernetes Cluster**: Access to a K8s cluster (e.g., k3s on a VM).
- **GitHub Actions**: Configured with SSH secrets to deploy to the remote server.
- **GHCR**: GitHub Container Registry for hosting images.

## 🚀 Deployment Workflow (k3s + Let's Encrypt)

Our primary production environment uses **k3s**, **Traefik** for ingress, and **cert-manager** for automated Let's Encrypt HTTPS certificates.

### 1. Configure Secret Data (Interim / Bootstrapping)
For bootstrapping the cluster, we use `Kustomize secretGenerator`. Generate cryptographically secure secrets locally:
```bash
just secrets-init
```
This generates a strict `.env` file locally. It is `.gitignore`d and will never be committed.

> [!TIP]
> **Idiomatic Secret Management (Recommended):**
> For long-term production, do not rely on raw `.env` files injected by CI/CD. Instead, use an idiomatic Kubernetes Secret Management Service such as **SealedSecrets**, **SOPS**, or the **External Secrets Operator** (syncing from AWS Secrets Manager / HashiCorp Vault). Kustomize should reference these encrypted manifests rather than plain text `.env` files.

### 2. Deploy Manifests (Kustomize)
Deploy the core infrastructure using standard native Kubernetes Kustomize:
```bash
kubectl apply -k deploy/kustomize/overlays/prod
```
Kustomize will automatically read the `.env` file, generate a hashed `chess-secrets` object, and deploy Postgres, Redis, API replicas, Worker replicas, and an Ingress with TLS.

### 3. Configure GitHub Actions
Your GitHub repository must have the following Secrets configured to allow the CI/CD pipeline to deploy:
- `DEPLOY_SSH_HOST` (e.g. `vcm-50800.vm.duke.edu`)
- `DEPLOY_SSH_USER` (e.g. `tl370`)
- `DEPLOY_SSH_KEY` (Private SSH key with server access)
- `PROD_ENV_FILE` (The contents of your securely generated `.env` file)

Once configured, pushing to the `main` branch will automatically build Docker images, push them to `ghcr.io`, and deploy them via Kustomize over SSH.

### 4. Scaling Workers
The Engine Worker pool autoscales via HPA on CPU utilization (target 70%). Default bounds: `minReplicas=2`, `maxReplicas=8`. To override transiently:
```bash
kubectl scale deployment chess-worker -n chess --replicas=10
```
The HPA will reclaim the override on its next reconcile (~30s) if the CPU signal doesn't justify the headcount.

**Why CPU and not queue depth?** Queue depth is the *cleaner* signal because it directly measures unsatisfied demand, but it requires KEDA (Phase 5). CPU utilization is a fair proxy today: an actively-searching worker pegs its core, so sustained high CPU across the fleet means the queue is being chased. Switch to KEDA once the cluster grows beyond a single VM.

**Each worker pod runs exactly one search at a time.** Go's `GOMAXPROCS(0)` reads the cgroup CPU limit, so on a 1-core pod (the default Guaranteed QoS shape), `WORKER_CONCURRENCY` defaults to 1. To handle more concurrent searches, **scale out** (more pods), don't pack threads into one pod and starve them — the original code used `runtime.NumCPU()` which ignored cgroup limits and would silently oversubscribe 16:1 on a fat node.

### 5. Scaling API
The API HPA scales on CPU + memory (target 70% / 75%). WebSocket connections accumulate goroutine stacks, so memory is often the binding constraint before CPU. Custom WS-connection-count metrics are a Phase-5 deliverable.

### 6. PodDisruptionBudgets
Both deployments have `minAvailable=1` PDBs so node maintenance can't drain the entire fleet at once. During HPA scale-down or rolling updates, k8s waits for replacement pods to be Ready before terminating the next.

### 7. Redis durability
The Redis deployment is single-primary with AOF persistence (`appendfsync everysec`) — a hard crash loses at most ~1s of pub/sub fan-out events. The engine queue (Redis List) and durable state (game records, invites) survive because Postgres is the source of truth; pub/sub is acceleration only.

Sentinel/HA is intentionally deferred to Phase 5 hardening — the cost of running Sentinel + 2 replicas + a failover script doesn't yet justify the resilience gain on a single-node k3s cluster. When you add a second physical node, the upgrade path is documented in `deploy/kustomize/base/resources.yaml` (commented stub).

## 🐳 Local Development vs Production

> [!WARNING]
> **Docker Compose is for Local Reference Only**
> We maintain `docker-compose.yml` strictly as a local development reference to ensure parity and testing ease. It is **not** intended for VM or production deployment. All future VM deployments must use the idiomatic K8s/Kustomize workflow.

For quick internal testing or low-traffic instances, you can use the compose file or build a single Go binary that includes the embedded frontend:

```bash
just build
./chess -server
```

## 🔗 Distributed Stack Overview
- **API Pods (100% Stateless)**: Handles WS and HTTP. Zero in-memory game state. Reads/writes to Postgres on demand. Infinitely scalable behind standard load balancers.
- **Worker Pods (Stateless Engine)**: CPU-intensive. Subscribes to Redis `BLPOP` queues for move calculation.
- **Redis (Event Bus & Queues)**: The operational backbone. Acts as the WebSocket broadcast bus, task queue for engine searches, and distributed lock manager (token + Lua compare-and-delete release).
- **Postgres (Authoritative State)**: The single source of truth for long-term persistence and active game states.

## 🗄 Persistence & Schema Migrations

The API container starts up by:
1. Reading `DATABASE_URL` from the environment (required — pod will crash-loop with a clear log line if missing).
2. Opening a pooled Postgres connection (`MaxOpenConns=25`, `MaxIdleConns=5`, `ConnMaxLifetime=30m`).
3. Applying any pending migrations from `pkg/db/migrations/`, embedded into the binary via `//go:embed`. `golang-migrate` holds a Postgres advisory lock during migration, so every replica running this on startup is safe — only one will apply, the rest fast-path to `ErrNoChange`.

Schema and queries live in two places:
- `pkg/db/migrations/000001_initial.{up,down}.sql` — versioned, append-only. Add `000002_*.sql` for new changes; never edit an applied migration.
- `pkg/db/queries/queries.sql` — sqlc input. After editing, run `just db-generate` to regenerate `pkg/db/gen/*` and commit both the SQL and the generated code.
