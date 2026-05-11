# Common dev tasks. `just` lists them; `just <name>` runs one.

default:
    @just --list

# Build the frontend assets
frontend:
    cd frontend && npm install && npm run build
    mkdir -p pkg/api/dist
    cp -r frontend/dist/* pkg/api/dist/

# Build the binary into ./chess
build: frontend
    go build -o chess ./cmd/chess

# Run in development mode with HMR for frontend
dev:
    @echo "Starting backend on :8080 and frontend on :5173..."
    @(trap 'kill 0' SIGINT; \
      go run ./cmd/chess -gui -no-open -addr localhost:8080 & \
      cd frontend && VITE_API_URL=http://localhost:8080 npm run dev)

# Run all tests
test:
    go test ./...

# Run only the perft (move-gen validation) tests with output
perft:
    go test -run Perft -v ./...

# Run UCI on stdio
run: build
    ./chess

# Run the GUI at http://localhost:8080
gui: build
    ./chess -gui

# Format every Go file in the repo
fmt:
    gofmt -w .

# Lint: gofmt clean + vet + test. Fails on any formatter diff.
check:
    test -z "$(gofmt -l .)" || (echo 'gofmt diff:'; gofmt -d .; exit 1)
    go vet ./...
    go test ./...

# Pure search benchmark from startpos at fixed depth (smoke check for perf regressions)
bench depth='6':
    @printf 'uci\nposition startpos\ngo depth {{depth}}\nquit\n' | ./chess | grep '^info depth'

# Remove build artifacts
clean:
    rm -rf chess build pkg/api/dist frontend/dist frontend/node_modules
