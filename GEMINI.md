# Gemini Mandates: neoromantics Chess Platform

## Distributed Microservices (Million-Dollar Architecture)
- Decoupled Engine Hub: All CPU-intensive search and analysis must reside in cmd/engine-worker. The API server must remain a lightweight orchestrator.
- Event-Driven Communication: Use pkg/bus (Redis Pub/Sub) for all cross-service communication. API dispatches engine.request and reacts to engine.response.
- Reactive WebSocket Events: WebSockets must follow the structured event pattern (type, payload). Avoid streaming raw board state; push transient insights (hints, assessments) via specific event types.
- Authoritative Backend: The backend is the sole source of truth for the game lifecycle. The frontend is a reactive terminal that displays state but never dictates engine scheduling.

## Scaling & Resilience
- Horizontally Scalable: Design all logic to support multiple API replicas and dozens of Engine Worker nodes without requiring sticky sessions.
- Stateless Orchestration: Use PostgreSQL for long-term persistence. Use Redis as a unified distributed lock manager and inter-pod cache invalidator to prevent split-brain conditions across replicas.
- Fail-Safe Processing: Use defer and atomic flags to ensure the system never gets stuck in a Thinking state if a network hop or calculation is aborted.
- Asynchronous Analysis: Analysis features (Hints, Assess) are asynchronous by design. API returns 202 Accepted and streams results via WebSockets.

## Engineering Standards
- Pure Go Search: Keep pkg/core zero-dependency and high-performance. It is the core IP of the platform.
- Service Purity: Remove all GUI terminology from the backend. It is a headless data manager.
- Kubernetes-Native GitOps: The canonical deployment uses Kubernetes (k3s) with raw manifests applied via GitHub Actions. Helm and Werf are explicitly rejected to maintain pure, untemplated GitOps simplicity in `deploy/k8s.yaml`.
- Security First: Passwords and tokens must be stored in Kubernetes Secrets. The platform must be served over HTTPS.
- neoromantics Branding: Ensure all module paths (github.com/neoromantics/chess) and metadata reflect the neoromantics identity.
