# syntax=docker/dockerfile:1.7

# ---- Build stage ----
FROM golang:1.26-alpine AS builder

# go-sqlite3 requires cgo + a C toolchain
RUN apk add --no-cache build-base git

WORKDIR /src

# Cache dependencies first
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Build with cgo enabled (go-sqlite3), stripped binary
ENV CGO_ENABLED=1 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/zapcast .

# ---- Runtime stage ----
FROM alpine:3.20

# Needed by cgo binary + TLS roots
RUN apk add --no-cache ca-certificates tzdata libc6-compat && \
    addgroup -S app && adduser -S -G app app

WORKDIR /app
COPY --from=builder /out/zapcast /app/zapcast

# Create store dir and assign ownership so the volume mount works on first run.
RUN mkdir -p /app/store && chown -R app:app /app/store

# Runtime defaults. HOST is 0.0.0.0 inside the container so docker-compose can
# expose the port; this MUST be paired with a reverse-proxy + auth in front of
# the container. The app also warns when bound beyond loopback without AUTH_TOKEN.
ENV HOST=0.0.0.0 \
    PORT=3000 \
    STORE_DIR=/app/store \
    LOG_FORMAT=json \
    LOG_LEVEL=info

# Persistent data volume
VOLUME ["/app/store"]
EXPOSE 3000

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q --spider http://localhost:3000/healthz || exit 1

USER app
ENTRYPOINT ["/app/zapcast"]
