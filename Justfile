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

# Clean up all build artifacts and node modules
clean:
    rm -rf chess build pkg/api/dist frontend/dist frontend/node_modules
