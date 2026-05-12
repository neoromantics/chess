# Million-Dollar Chess Platform: Docker-First Workflow

default:
    @just --list

# --- Production Operations (Docker Compose) ---

# Start the entire distributed stack (API, Worker, Postgres)
up:
    docker-compose up --build -d

# Stop all services
down:
    docker-compose down

# View real-time logs from all services
logs:
    docker-compose logs -f

# View status of running containers
status:
    docker-compose ps

# Fully reset the platform (warning: deletes all database data)
reset:
    docker-compose down -v
    docker-compose up --build -d

# --- Development Workflow ---

# Run in local development mode (no containers, for rapid Go/Vue coding)
# Go API on :8080, Vite on :5173 with HMR
dev:
    @echo "Starting local dev environment..."
    @(trap 'kill 0' SIGINT; \
      go run ./cmd/chess -addr localhost:8080 -no-open & \
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
