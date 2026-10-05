# AGENTS.md

Guidance for AI coding agents and maintainers working in this repository.

## Purpose

ZapCast is a self-hosted WhatsApp bulk-messaging web app. It has a Go backend, SQLite persistence, an embedded SPA and a `whatsmeow` WhatsApp Multi-Device client.

## Stack

- Go 1.26 (`go.mod`)
- `go.mau.fi/whatsmeow` for WhatsApp connectivity
- `github.com/mattn/go-sqlite3` for SQLite (cgo required)
- `log/slog` for structured logging
- Plain HTML/CSS/JS under `internal/server/web/`
- Vite/Vitest/TypeScript/Playwright for frontend build and tests

## Run

```bash
go mod download
go run .
```

Defaults: `http://127.0.0.1:3000`, runtime state in `./store/`.

Docker:

```bash
docker compose up -d
docker compose logs -f
```

## Build modes

- Dev/default: `go run .` or `go build .` embeds `internal/server/web/`.
- Prod: run `npm run build`, then `go build -tags prod`; this embeds `internal/server/web/dist/`.
- Release binaries inject `versionLdflag` via ldflags.

## Environment

- `HOST` default `127.0.0.1`
- `PORT` default `3000`
- `STORE_DIR` default `store`
- `LOG_LEVEL` default `info`
- `LOG_FORMAT` default `text`
- `AUTH_TOKEN` enables native auth for `/api/*` and `/debug/vars`
- `FRONTEND_ORIGIN` pins allowed browser origin
- `BROADCAST_DELAY_MS`, `BROADCAST_DELAY_JITTER`, `BROADCAST_HOURLY_CAP` tune pacing

## Test

```bash
gofmt -s -w .
go vet ./...
go test ./...
go test -race -coverprofile=coverage.out ./...

npm ci
npm test
npm run test:types
npm run build
npm run test:e2e
```

`e2e/` contains Playwright browser smoke tests.

## Architecture notes

- `main.go` only calls `server.Run()`.
- Server code lives under `internal/server/`.
- `server.go` owns `App`, startup, routes and shutdown.
- `contacts.go` owns persistence APIs; avoid raw SQL in handlers and WhatsApp callbacks.
- Migrations live under `internal/server/migrations/`; add new files, do not edit released migrations in place.
- `broadcast.go`, `broadcaster.go` and `outbox.go` own delivery lifecycle/retries.
- `sse.go` + `sse_events.go` own non-blocking SSE fan-out and typed payloads.
- `auth.go` + `session.go` own QR/status/logout and browser session exchange.
- `internal/server/web/` is the SPA source.

## Conventions

- Keep API docs in `README.md` and error-code docs in `SECURITY.md` in sync with routes/errors.
- Translatable errors implement `LocalizedError` and are surfaced through `handleError` or `jsonErrorLocalized`.
- Logs use injected `*slog.Logger` values and structured key/value pairs.
- `SSEHub.Broadcast` is intentionally non-blocking; drops are counted.
- `Broadcaster.Start` returns a `StartHandle`; exactly one of `Done()` or `Abandon()` must run.
- Phone validation is strict E.164-style digits: 7–15 digits, no leading `0`.
- Auth accepts an HttpOnly browser session cookie first, then `Authorization: Bearer ...`; query-string token support exists only as a deprecated fallback.

## Gotchas

- `store/` contains WhatsApp session secrets and app data. It is gitignored and must not be committed.
- cgo is required for SQLite.
- Production web bytes are generated; if `-tags prod` fails, run `npm run build` first.
- Shutdown ordering matters: close broadcast admission, cancel active broadcast, wait for broadcast/outbox, shut down HTTP, disconnect WhatsApp, close DB.
