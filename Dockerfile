# --- Stage 1: Build the Vue Frontend ---
FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# --- Stage 2: Build the Go Binaries ---
FROM golang:1.25-alpine AS backend-builder
WORKDIR /app
RUN apk add --no-cache git gcc musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Copy the built frontend assets from Stage 1 into the Gateway for embedding
COPY --from=frontend-builder /app/frontend/dist/ cmd/gateway/dist/

# Build all microservices into a /build directory
RUN mkdir -p /build && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /build/worker ./cmd/engine-worker && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /build/gateway ./cmd/gateway && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /build/user-service ./cmd/user && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /build/game-service ./cmd/game && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /build/matchmaker ./cmd/matchmaker && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /build/rating-updater ./cmd/rating-updater

# --- Stage 3: Unified Platform Runtime ---
FROM alpine:latest
WORKDIR /app
RUN apk add --no-cache ca-certificates

# Copy all binaries from the builder
COPY --from=backend-builder /build/* ./

# Default metadata
EXPOSE 8080 8081
CMD ["./gateway"]
