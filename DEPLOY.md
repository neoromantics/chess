# Deployment Guide: neoromantics Chess Platform

This platform is designed for professional Kubernetes deployment via **GitHub Actions** to a **k3s** cluster.

## 🏗 Prerequisites
- **Kubernetes Cluster**: Access to a K8s cluster (e.g., k3s on a VM).
- **GitHub Actions**: Configured with SSH secrets to deploy to the remote server.
- **GHCR**: GitHub Container Registry for hosting images.

## 🚀 Deployment Workflow (k3s + Let's Encrypt)

Our primary production environment uses **k3s**, **Traefik** for ingress, and **cert-manager** for automated Let's Encrypt HTTPS certificates.

### 1. Configure Secret Data
Generate cryptographically secure secrets and store them directly in the cluster:
```bash
kubectl create namespace chess

JWT_SECRET=$(openssl rand -hex 32)
PG_PASSWORD=$(openssl rand -base64 24 | tr -d '=/+' | head -c 32)

kubectl -n chess create secret generic chess-secrets \
  --from-literal=jwt-secret="$JWT_SECRET" \
  --from-literal=postgres-password="$PG_PASSWORD" \
  --from-literal=database-url="postgres://chess:${PG_PASSWORD}@chess-db:5432/chess?sslmode=disable"
```

### 2. Configure GitHub Actions
Your GitHub repository must have the following Secrets configured to allow the CI/CD pipeline to deploy:
- `DEPLOY_SSH_HOST` (e.g. `vcm-50800.vm.duke.edu`)
- `DEPLOY_SSH_USER` (e.g. `tl370`)
- `DEPLOY_SSH_KEY` (Private SSH key with server access)

Once configured, pushing to the `main` branch will automatically build Docker images, push them to `ghcr.io`, and deploy them via SSH.

### 3. Deploy Manifests
Deploy the core infrastructure from the tracked manifests:
```bash
kubectl apply -n chess -f deploy/k8s.yaml
```
This provisions Postgres, Redis, API replicas, Worker replicas, and an Ingress with TLS.

### 4. Scaling Workers
The Engine Worker pool can be scaled independently to handle increased load:
```bash
kubectl scale deployment chess-worker -n chess --replicas=10
```

## 📦 Local Binary (Single File)
For quick internal testing or low-traffic instances, you can build a single Go binary that includes the embedded frontend:

```bash
just build
./chess -server
```

## 🔗 Distributed Stack Overview
- **API Pods (Strictly Stateless)**: Handles WS and Auth. Infinitely scalable behind standard load balancers without requiring sticky sessions, thanks to Redis.
- **Worker Pods**: CPU-intensive, subscribes to Redis tasks for move calculation.
- **Redis**: The operational backbone. Acts as the message broker, distributed lock manager (preventing race conditions), and inter-pod cache invalidator.
- **Postgres**: Authoritative persistent storage.
