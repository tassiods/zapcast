# ZapCast architecture notes

Short reference for contributors and agents. The full public overview lives in `README.md`; this file focuses on code contracts that are easy to miss.

## Package layout

```text
.
├── main.go                     # thin entry; calls server.Run()
├── cmd/seed/                   # fixture seeder through ContactStore
├── e2e/                        # Playwright browser smoke tests
└── internal/server/            # HTTP service, storage, web assets
    ├── server.go               # App struct, Run(), route registration, shutdown
    ├── auth.go + session.go    # WhatsApp status/QR/logout and browser sessions
    ├── errors.go               # APIError envelope + LocalizedError contract
    ├── middleware.go           # request ID, CORS, auth, security headers
    ├── access_log.go           # structured request log + per-route metrics
    ├── health.go               # /healthz, /readyz
    ├── metrics.go              # /debug/vars expvar counters
    ├── handlers_*.go           # per-area HTTP handlers
    ├── contacts.go             # ContactStore and SQLite-backed persistence APIs
    ├── migrations.go           # schema_migrations runner
    ├── migrations/             # embedded .sql migrations
    ├── broadcast.go            # /api/broadcast handler and send loop
    ├── broadcaster.go          # in-flight slot, cancel and cooldown lifecycle
    ├── outbox.go               # retry worker and dead-letter rows
    ├── sse.go + sse_events.go  # SSE hub and typed payloads
    ├── wa_events.go            # whatsmeow → app bridge
    ├── waclient.go             # WhatsApp adapter interface for tests
    ├── i18n*.go                # translation tables + ETag cache
    └── web/                    # SPA source; Vite writes dist/ for prod builds
```

## Build modes

- Default Go builds embed `internal/server/web/` directly. This is convenient for development and includes source/test files in the embedded filesystem.
- Production builds must run `npm run build` first, then `go build -tags prod`; `embed_prod.go` serves only `internal/server/web/dist/`.
- Release workflow binaries inject the tag through `-ldflags "-X zapcast/internal/server.versionLdflag=<tag>"`.

## Error model

Translatable backend errors implement:

```go
type LocalizedError interface {
    error
    LocalizedKey() string
    LocalizedArgs() []any
}
```

Use `errors.As`-compatible implementations so localisation survives error wrapping.

When adding a translatable error:

1. Add the key to `i18n_en.go`, `i18n_pt.go` and `i18n_es.go`.
2. Use `newErrKey("err_foo")` or a typed struct when callers need structured data.
3. Add a static interface assertion for custom structs.
4. Surface it through `handleError` or `jsonErrorLocalized`.

Wire shape:

```json
{ "error": { "code": "err_phone_too_short", "message": "phone too short (minimum 7 digits, got 4)" } }
```

## HTTP routes

Routes are registered centrally in `server.go`. Keep `README.md`, `SECURITY.md` and tests in sync when public routes or stable error codes change.

## Storage

- SQLite runs in WAL mode with foreign keys enabled.
- `ContactStore` methods take `context.Context` as the first argument and use `*Context` driver calls.
- `SetMaxOpenConns(1)` serialises writes.
- Schema changes ship as new files in `internal/server/migrations/`; do not edit existing migrations in place after release.
- Current logical tables include `contacts`, `contact_groups`, `group_members`, `template_vars`, `broadcasts`, `broadcast_messages`, `outbox` and `schema_migrations`.

## Broadcast lifecycle

`Broadcaster.Start(ctx, cooldown)` is the only entry into the in-flight broadcast slot. It takes the mutex, checks cooldown, wires cancellation and returns a `StartHandle`.

Exactly one of these must happen:

- `handle.Done()` — normal goroutine completion, usually in a `defer`.
- `handle.Abandon()` — validation failed after `Start` but before launching the goroutine.

Both paths are `sync.Once` guarded. Forgetting both leaks the broadcast mutex and makes later callers see `ErrBroadcastRunning`.

## SSE events

`Event{Type string, Data any}` serialises as `{type, data}`. Every data payload should be a typed struct in `sse_events.go`.

`SSEHub.Broadcast` is non-blocking; slow subscribers may drop events and `metricSSEDrops` counts those drops. Never assume SSE delivery.

## Shutdown ordering

`runShutdown` cancels `App.shutdownCtx`, closes the broadcaster to stop admission, cancels any active broadcast, waits for broadcast/outbox work, shuts down HTTP, disconnects WhatsApp, closes storage and stops rate limiters. New async work should derive from an appropriate context and fit this ordering.
