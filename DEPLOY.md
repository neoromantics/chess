# Deployment Guide: neoromantics Chess Platform

This platform is designed for professional Kubernetes deployment using **Werf** and **Helm**.

## 🏗 Prerequisites
- **Kubernetes Cluster**: Access to a K8s cluster (GKE, EKS, local k3s/Minikube).
- **Werf**: The primary build and deployment orchestrator ([werf.io](https://werf.io)).
- **Helm**: Template engine for K8s manifests.
- **Redis & Postgres**: Provided in the Helm chart, but can be swapped for managed services.

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

### 2. Build & Push Images
We use a local Docker registry running on the k3s host to manage images:
```bash
docker build --network=host -t localhost:5000/chess-api:latest --target api-runtime .
docker build --network=host -t localhost:5000/chess-worker:latest --target worker-runtime .
docker push localhost:5000/chess-api:latest
docker push localhost:5000/chess-worker:latest
```

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
- **API Pods**: Stateless, handles WS and Auth.
- **Worker Pods**: CPU-intensive, subscribes to Redis tasks.
- **Redis**: Acts as the message broker and transient state store.
- **Postgres**: Authoritative persistent storage.
