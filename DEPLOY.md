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
The Engine Worker pool can be scaled independently to handle increased load:
```bash
kubectl scale deployment chess-worker -n chess --replicas=10
```

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
- **Redis (Event Bus & Queues)**: The operational backbone. Acts as the WebSocket broadcast bus, task queue for engine searches, and distributed lock manager.
- **Postgres (Authoritative State)**: The single source of truth for long-term persistence and active game states.
