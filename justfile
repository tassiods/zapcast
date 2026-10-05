# ZapCast — development task runner
# Install just: https://github.com/casey/just

set dotenv-load := false

# Port for the HTTP server (override with PORT=8080 just run)
port := env("PORT", "3000")

# Default: list available recipes
default:
    @just --list

# ─── Development ──────────────────────────────────────────────

# Run the server (cgo required for go-sqlite3)
run *args='':
    CGO_ENABLED=1 PORT={{port}} go run . {{args}}

# Run with debug logging
dev:
    LOG_LEVEL=debug just run

# Build a release binary
build:
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o zapcast .

# ─── Quality ──────────────────────────────────────────────────

# Run tests
test *args='':
    CGO_ENABLED=1 go test ./... {{args}}

# Run tests with verbose output
test-v:
    just test -v

# Run tests with the race detector enabled. Slower but catches data races
# on shared state (App.wa swaps, SSE subscriber map, broadcastCancel).
test-race *args='':
    CGO_ENABLED=1 go test -race ./... {{args}}

# Run tests and write a coverage profile; print the per-function totals.
cover:
    CGO_ENABLED=1 go test -coverprofile=coverage.out ./...
    go tool cover -func=coverage.out | tail -1

# Run go vet
vet:
    go vet ./...

# Format all Go files
fmt:
    gofmt -s -w .

# Run all checks (fmt + vet + race-test). Mirrors the CI gate.
check:
    @echo "--- fmt ---"
    gofmt -s -l . | tee /dev/stderr | (! read) && echo "ok"
    @echo "--- vet ---"
    just vet
    @echo "--- test (race) ---"
    just test-race

# ─── Docker ───────────────────────────────────────────────────

# Build the Docker image
docker-build:
    docker compose build

# Start container (detached)
docker-up:
    PORT={{port}} docker compose up -d

# Stop container
docker-down:
    docker compose down

# Show container logs (follow)
docker-logs:
    docker compose logs -f

# Full cycle: build image, restart, show logs
docker-run: docker-build docker-down docker-up docker-logs

# ─── Data ─────────────────────────────────────────────────────

# Seed the database with fake contacts for UX testing
seed *args='':
    CGO_ENABLED=1 go run ./cmd/seed {{args}}

# Remove local store (databases). Asks for confirmation.
clean:
    @echo "This will delete store/ (device.db + zapcast.db)"
    @read -p "Continue? [y/N] " yn && [ "$$yn" = "y" ] && rm -rf store/ && echo "Removed." || echo "Cancelled."
