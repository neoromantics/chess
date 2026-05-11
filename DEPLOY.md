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
      - "8080:8080"
    volumes:
      - ./chess-data:/app/data
    environment:
      - PORT=8080
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

## Production Architecture (Nginx + HTTPS)

In production, you should run the application behind a reverse proxy like **Nginx** or **Caddy** to handle SSL/TLS (HTTPS).

### Nginx Example
```nginx
server {
    server_name chess.example.com;

    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    listen 443 ssl; # managed by Certbot
    # ... ssl certificates ...
}
```

### Caddy Example
```caddy
chess.example.com {
    reverse_proxy localhost:8080
}
```

## Health Checks & Monitoring

The backend provides a health check endpoint at `/health`.

- **Liveness/Readiness**: Use `GET /health` for Kubernetes or Docker health checks.
- **Logging**: The application uses structured JSON logging (`slog`). Logs can be collected using standard tools like Fluentbit or ELK stack.

## Data Persistence
The `chess.db` file in the data volume stores all user accounts and past games. Ensure this volume is backed up regularly.
