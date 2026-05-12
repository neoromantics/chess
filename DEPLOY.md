# Deployment Guide: neoromantics Chess Platform

This platform is designed for professional Kubernetes deployment via **GitHub Actions** to a **k3s** cluster.

## 🏗 Prerequisites
- **Kubernetes Cluster**: Access to a K8s cluster (e.g., k3s on a VM).
- **GitHub Actions**: Configured with SSH secrets to deploy to the remote server.
- **GHCR**: GitHub Container Registry for hosting images.

## 🚀 Deployment Workflow (k3s + Let's Encrypt)

Our primary production environment uses **k3s**, **Traefik** for ingress, and **cert-manager** for automated Let's Encrypt HTTPS certificates.

### 1. Configure Secret Data
Generate cryptographically secure secrets locally:
```bash
just secrets-init
```
This generates a strict `.env` file locally. It is `.gitignore`d and will never be committed.

### 2. Deploy Manifests (Kustomize)
Deploy the fleet using standard native Kubernetes Kustomize:
```bash
just deploy-prod
```
Kustomize will automatically read the `.env` file, generate a hashed `chess-secrets` object, and deploy the 6-pod microservices fleet.

### 3. Configure GitHub Actions
Your GitHub repository must have the following Secrets configured:
- `DEPLOY_SSH_HOST`
- `DEPLOY_SSH_USER`
- `DEPLOY_SSH_KEY`
- `PROD_ENV_FILE` (The contents of your locally generated `.env` file)

### 4. Distributed Stack Overview
- **Gateway (Ingress)**: Entry point for all connections. Authenticates JWTs and routes traffic.
- **User Service**: Owns the identities and profile persistence.
- **Game Service**: Authoritative arbiter. Processes commands sequentially per game.
- **Matchmaker**: Processes queues and pairing logic.
- **Rating Updater**: Asynchronously updates ranks after game completion.
- **Engine Worker**: Calculation nodes consuming search tasks from Redis Streams.

## 🐳 Local Development

We maintain `docker-compose.yml` strictly as a local development reference.

```bash
just up
```

## 🗄 Persistence
Postgres is the source of truth for all durable state. Schema management is now handled manually; use `pkg/db/schema.sql` as the authoritative definition.
