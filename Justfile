# Million-Dollar Chess Platform: Docker-First Workflow

default:
    @just --list
# --- Production Operations (Docker Compose) ---

# Build all production binaries
build:
    cd frontend && npm install && npm run build
    mkdir -p cmd/gateway/dist
    cp -r frontend/dist/* cmd/gateway/dist/
    go build -o build/gateway ./cmd/gateway
    go build -o build/user-service ./cmd/user
    go build -o build/game-service ./cmd/game
    go build -o build/matchmaker ./cmd/matchmaker
    go build -o build/rating-updater ./cmd/rating-updater
    go build -o build/engine-worker ./cmd/engine-worker

# Start the entire distributed stack (Gateway, User, Game, Matchmaker, Rating, Worker, Redis, Postgres)
up:
    @if [ ! -f .env ]; then echo "No .env found — bootstrapping with secrets-init"; just secrets-init; fi
    docker-compose -f infra/docker-compose.yml up --build -d

# Scale up engine calculation nodes
# Usage: just scale 3
scale n:
    docker-compose -f infra/docker-compose.yml up -d --scale engine-worker={{n}}

# Stop all services
down:
    docker-compose -f infra/docker-compose.yml down

# --- Development Workflow ---

# View real-time logs from all services
logs:
    docker-compose -f infra/docker-compose.yml logs -f

# View real-time logs from the Gateway
logs-gateway:
    docker-compose -f infra/docker-compose.yml logs -f gateway

# View real-time logs from the User Service
logs-user:
    docker-compose -f infra/docker-compose.yml logs -f user-service

# View real-time logs from the Game Service
logs-game:
    docker-compose -f infra/docker-compose.yml logs -f game-service

# View real-time logs from the Engine Worker
logs-worker:
    docker-compose -f infra/docker-compose.yml logs -f engine-worker

# View real-time logs from the Matchmaker
logs-matchmaker:
    docker-compose -f infra/docker-compose.yml logs -f matchmaker

# View real-time logs from the Rating Updater
logs-rating:
    docker-compose -f infra/docker-compose.yml logs -f rating-updater

# View status of running containers
status:
    docker-compose -f infra/docker-compose.yml ps

# Fully reset the platform (warning: deletes all database data, including the
# bind-mounted ./postgres-data and the local .env)
reset:
    docker-compose -f infra/docker-compose.yml down -v
    rm -rf postgres-data .env
    just up

# --- Engineering Standards ---

# Run all backend tests
test:
    go test -v ./pkg/...

# Format Go code
fmt:
    gofmt -w .

# Regenerate sqlc Go code from pkg/db/queries + pkg/db/schema.sql
db-generate:
    sqlc generate -f infra/sqlc.yaml

# Clean up all build artifacts and node modules
clean:
    rm -rf build cmd/gateway/dist frontend/dist frontend/node_modules

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
    kubectl apply -k infra/kustomize/overlays/prod

# Rotate all secrets and rolling-restart pods in production
secrets-rotate:
    @rm -f .env
    @just secrets-init
    @just deploy-prod
    kubectl rollout restart deployment chess-gateway chess-user-service chess-game-service chess-matchmaker chess-rating-updater chess-worker chess-db -n chess
