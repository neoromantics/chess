# Deployment & secrets

## Cluster & manifests

- All manifests live in `infra/deploy.yaml` (a single file with all three services + ingress + PVCs). Kustomize at `infra/kustomization.yaml` sets `namespace: chess`.
- KEDA `ScaledObject` definitions for `engine-worker` (off Redis stream depth) and `gateway` (off live WS connection count) live in `infra/keda.yaml`. KEDA v2 must be installed on the cluster as a one-time prerequisite.
- Observability (Prometheus + Grafana) lives in `infra/observability.yaml`. Grafana is exposed at `/grafana/`.

## Deploy flow

GitHub Actions builds the unified image and pushes to `ghcr.io/neoromantics/chess`. A self-hosted runner on the VM does `kubectl apply -k infra/` + `kubectl rollout restart` for the three Deployments.

## Secrets

- **Secrets live in k3s**, owned by the cluster. Bootstrap a fresh cluster with `./infra/bootstrap-secrets.sh` (random openssl-generated values). Rotate via `kubectl edit secret chess-secrets -n chess` then `kubectl rollout restart …`.
- **CI never sees prod secrets.** The deploy job is `kubectl apply -k infra/` + `kubectl rollout restart`. The self-hosted runner runs on the VM.
- **Rotating Postgres credentials requires also wiping `chess-db-pvc`**, since Postgres only honors `POSTGRES_USER`/`PASSWORD` on the first init of the data dir.

## Postgres connection sizing

**`max_connections=500`** is set via `args: ["-c", "max_connections=500"]` on the `chess-db` Deployment. Engine-worker doesn't open a PG pool, so the live ceiling is `(gateway HPA max 8) + (game-service HPA max 6) = 14 pods × MaxOpenConns=30 = 420 client conns`, leaving ~80 for autovacuum + superuser reservations.

Per-pod sizing is env-tunable via `PG_MAX_OPEN_CONNS` / `PG_MAX_IDLE_CONNS`.

We tried PGBouncer but burned four deploy cycles on broken Docker Hub tags (`edoburu/pgbouncer:1.23.1`, `:1.23.0`, `bitnami/pgbouncer:1.23.1`, all returned `manifest unknown`). Tuning PG itself is the simpler win at this scale. Revisit when we cross ~500 concurrent backends.
