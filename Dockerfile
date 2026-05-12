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

# Build all microservices
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o chess-worker ./cmd/engine-worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o gateway ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o user-service ./cmd/user
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o game-service ./cmd/game
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o matchmaker ./cmd/matchmaker

# --- Stage 3: Gateway Runtime ---
FROM alpine:latest AS gateway-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/gateway .
ENV PORT=8080
EXPOSE 8080
CMD ["./gateway"]

# --- Stage 4: User Service Runtime ---
FROM alpine:latest AS user-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/user-service .
ENV PORT=8081
EXPOSE 8081
CMD ["./user-service"]

# --- Stage 5: Game Service Runtime ---
FROM alpine:latest AS game-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/game-service .
CMD ["./game-service"]

# --- Stage 6: Matchmaker Runtime ---
FROM alpine:latest AS matchmaker-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/matchmaker .
CMD ["./matchmaker"]

# --- Stage 7: Engine Worker Runtime ---
FROM alpine:latest AS worker-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/chess-worker .
CMD ["./chess-worker"]


