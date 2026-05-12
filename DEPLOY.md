# Deployment Guide: neoromantics Chess Platform

This platform is designed for professional Kubernetes deployment using **Werf** and **Helm**.

## 🏗 Prerequisites
- **Kubernetes Cluster**: Access to a K8s cluster (GKE, EKS, local k3s/Minikube).
- **Werf**: The primary build and deployment orchestrator ([werf.io](https://werf.io)).
- **Helm**: Template engine for K8s manifests.
- **Redis & Postgres**: Provided in the Helm chart, but can be swapped for managed services.

## 🚀 Deployment Workflow

### 1. Configure Secret Data
Update `.helm/values.yaml` or create a custom values file for your environment (e.g., `values-prod.yaml`). Ensure `jwtSecret` and `databaseUrl` are secure.

### 2. Build & Deploy with Werf
Werf handles the entire lifecycle: building images, tagging them, and deploying the Helm chart.

```bash
# Deploy to the 'dev' namespace
just converge env=dev

# Deploy to 'prod' with custom settings
just converge env=prod
```

### 3. Scaling Workers
The Engine Worker pool can be scaled independently via Helm values or the command line.

```bash
# Scale to 10 workers in production
werf converge --env prod --set "worker.replicaCount=10"
```

## 🛠 CI/CD Integration
Werf is designed for GitOps. In a professional pipeline (GitHub Actions, GitLab CI):
1. Werf builds the images based on the Git commit SHA.
2. It caches stages in your container registry.
3. It performs a zero-downtime rolling update on your K8s cluster.

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
