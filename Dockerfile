# --- Stage 1: Build the Vue Frontend ---
FROM node:20-alpine AS frontend-builder
WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# --- Stage 2: Build the Go Backend ---
FROM golang:1.24-alpine AS backend-builder
WORKDIR /app
# Install build dependencies (needed for webview if we were building for desktop, 
# but for Docker we only build the server/web version)
RUN apk add --no-cache git gcc musl-dev

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Copy the built frontend assets from Stage 1 into the api package's dist folder
COPY --from=frontend-builder /app/frontend/dist/ pkg/api/dist/

# Build the binary. We use tags to exclude webview from the docker build
# as it's intended for server-side execution.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o chess ./cmd/chess

# --- Stage 3: Minimal Runtime ---
FROM alpine:latest
WORKDIR /app
RUN apk add --no-cache ca-certificates

COPY --from=backend-builder /app/chess .

# Default environment variables
ENV PORT=8080
ENV DB_PATH=/app/data/chess.db
ENV JWT_SECRET=change-me-in-production

# Create data directory for SQLite
RUN mkdir -p /app/data

EXPOSE 8080
CMD ["./chess", "-addr", "0.0.0.0:8080", "-no-open"]
