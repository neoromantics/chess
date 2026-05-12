# Million-Dollar Chess Platform: Docker-First Workflow

default:
    @just --list
# --- Production Operations (Docker Compose) ---

# Build the production binary with embedded frontend
build:
    cd frontend && npm install && npm run build
    mkdir -p pkg/api/dist
    cp -r frontend/dist/* pkg/api/dist/
    go build -o chess ./cmd/chess

# Start the entire distributed stack (API, Worker, Redis, Postgres)
up:
    docker-compose up --build -d

# Scale up engine calculation nodes
# Usage: just scale 3
scale n:
    docker-compose up -d --scale worker={{n}}

# Stop all services
down:
    docker-compose down

# --- Development Workflow ---

# View real-time logs from all services
logs:
    docker-compose logs -f

# View real-time logs from the API server
logs-api:
    docker-compose logs -f api

# View real-time logs from the Engine Worker
logs-worker:
    docker-compose logs -f worker

# View status of running containers
status:
    docker-compose ps

# Fully reset the platform (warning: deletes all database data)
reset:
    docker-compose down -v
    docker-compose up --build -d

# --- Development Workflow ---

# Run in local development mode (no containers, for rapid Go/Vue coding)
# Note: Requires a local Redis instance on :6379
dev:
    @echo "Starting local dev environment (requires local Redis)..."
    @(trap 'kill 0' SIGINT; \
      go run ./cmd/chess -addr localhost:8080 -no-open -server & \
      cd frontend && VITE_API_URL=http://localhost:8080 npm run dev)

# --- Engineering Standards ---

# Run all backend tests
test:
    go test -v ./pkg/...

# Format Go code
fmt:
    gofmt -w .

# Regenerate sqlc Go code from pkg/db/queries + pkg/db/migrations
db-generate:
    sqlc generate

# Clean up all build artifacts and node modules
clean:
    rm -rf chess build pkg/api/dist frontend/dist frontend/node_modules

# Generate a local .env file with strong random credentials
secrets-init:
    @if [ -f .env ]; then echo ".env already exists"; exit 1; fi
    @echo "POSTGRES_USER=chess_$(openssl rand -hex 4)" > .env
    @echo "POSTGRES_PASSWORD=$(openssl rand -hex 24)" >> .env
    @echo "POSTGRES_DB=chess_$(openssl rand -hex 4)" >> .env
    @echo "JWT_SECRET=$(openssl rand -base64 48 | tr -d '/+=')" >> .env
    @chmod 600 .env
    @echo "Generated new .env with strong secrets."

# Deploy to K8s using standard Kustomize
deploy-prod:
    kubectl apply -k deploy/kustomize/overlays/prod

# Rotate all secrets and rolling-restart pods in production
secrets-rotate:
    @rm -f .env
    @just secrets-init
    @just deploy-prod
    kubectl rollout restart deployment chess-api chess-worker chess-db -n chess

