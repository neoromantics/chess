# Common dev tasks. `just` lists them; `just <name>` runs one.

default:
    @just --list

# Build the binary into ./chess
build:
    go build -o chess .

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
    rm -f chess
