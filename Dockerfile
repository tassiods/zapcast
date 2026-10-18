# Build the same production embed as CI; never ship the source web tree.
FROM node:22.23.3-alpine@sha256:0a7108bf6c7bf5de370ffb1a3ed6be93d405b43ff159f681a8d18c0e2bc2e402 AS web
WORKDIR /src
RUN npm install --global npm@12.2.0
COPY package.json package-lock.json ./
RUN npm ci
COPY vite.config.js ./
COPY internal/server/web ./internal/server/web
RUN npm run build

FROM golang:1.26.9-alpine@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade AS builder
RUN apk add --no-cache build-base git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY main.go ./
COPY internal ./internal
COPY --from=web /src/internal/server/web/dist ./internal/server/web/dist
ARG VERSION
ENV CGO_ENABLED=1 GOTOOLCHAIN=local GOMAXPROCS=1 GOMEMLIMIT=600MiB GOFLAGS=-p=1
RUN test -n "$VERSION" && mkdir -p /out && go build -trimpath -tags prod \
    -ldflags="-s -w -X zapcast/internal/server.versionLdflag=${VERSION}" -o /out/zapcast .

FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 app && adduser -S -u 10001 -G app app && \
    mkdir -p /app/store && chown app:app /app/store
WORKDIR /app
COPY --from=builder /out/zapcast /app/zapcast
ENV HOST=0.0.0.0 PORT=3000 STORE_DIR=/app/store LOG_FORMAT=json LOG_LEVEL=info AUTH_MODE=native
VOLUME ["/app/store"]
EXPOSE 3000
# Liveness does not depend on WhatsApp pairing. Operators probe /readyz separately.
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD ["/app/zapcast", "healthcheck"]
USER 10001:10001
ENTRYPOINT ["/app/zapcast"]
