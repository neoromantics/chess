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
# Copy the built frontend assets from Stage 1 into the api package's dist folder
COPY --from=frontend-builder /app/frontend/dist/ pkg/api/dist/

# Build both the API server and the Engine Worker
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o chess-api ./cmd/chess
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o chess-worker ./cmd/engine-worker

# --- Stage 3: Minimal Runtime (API) ---
FROM alpine:latest AS api-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/chess-api .
ENV PORT=8080
EXPOSE 8080
CMD ["./chess-api", "-addr", "0.0.0.0:8080", "-no-open"]

# --- Stage 4: Minimal Runtime (Worker) ---
FROM alpine:latest AS worker-runtime
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=backend-builder /app/chess-worker .
CMD ["./chess-worker"]
