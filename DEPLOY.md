# Deployment Guide

This guide covers how to deploy the Chess Platform for web production.

## Docker (Recommended)

The easiest way to deploy is using the provided `Dockerfile`.

### Build the image
```bash
docker build -t neoromantics/chess .
```

### Run the container
```bash
docker run -d \
  -p 8080:8080 \
  -e JWT_SECRET=your-secret-key \
  -v $(pwd)/data:/app/data \
  --name chess \
  neoromantics/chess
```

## Docker Compose

For a production-ready setup with persistent storage:

```yaml
services:
  chess:
    image: neoromantics/chess
    build: .
    ports:
      - "80:8080"
    volumes:
      - ./chess-data:/app/data
    environment:
      - DB_PATH=/app/data/chess.db
      - JWT_SECRET=generate-a-long-random-string
    restart: always
```

Run with: `docker-compose up -d`

## Configuration (Environment Variables)

| Variable | Description | Default |
|----------|-------------|---------|
| `PORT` | The port the Go server listens on | `8080` |
| `DB_PATH` | Path to the SQLite database file | `/app/data/chess.db` |
| `JWT_SECRET` | Secret key for signing session tokens | `change-me` |

## Manual Deployment (No Docker)

1. **Build Frontend**:
   ```bash
   cd frontend && npm install && npm run build
   ```
2. **Compile Go Binary**:
   ```bash
   mkdir -p pkg/api/dist
   cp -r frontend/dist/* pkg/api/dist/
   go build -o chess ./cmd/chess
   ```
3. **Run**:
   ```bash
   ./chess -addr 0.0.0.0:80 -no-open
   ```

## Production Considerations

1. **HTTPS**: The Go server is HTTP-only. In production, run it behind a reverse proxy like **Nginx**, **Caddy**, or **Traefik** to handle SSL/TLS.
2. **Backup**: Regularly back up the `chess.db` file.
3. **Engine Load**: The chess engine is CPU intensive. Ensure your server has sufficient compute resources for multiple concurrent searches.
