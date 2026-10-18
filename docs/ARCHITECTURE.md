# ZapCast — Architecture Notes

Short reference for contributors. Covers the contracts that are easy to
miss from reading code in isolation.

## Package layout

```
.
├── main.go                     # thin entry — calls server.Run()
├── cmd/seed/                   # CLI for fixture data, goes through ContactStore
└── internal/server/            # the entire HTTP service
    ├── server.go               # App struct + Run() boot
    ├── errors.go               # APIError envelope + LocalizedError contract
    ├── middleware.go           # requestID, CORS, auth, ETag, security headers
    ├── access_log.go           # structured request log + per-route metric
    ├── sse.go + sse_events.go  # hub + typed event payloads + Origin gate
    ├── broadcaster.go          # mutex/cancel/WG lifecycle (StartHandle)
    ├── broadcast.go            # /api/broadcast handler + worker
    ├── auth.go                 # status, QR pairing, logout, Version()
    ├── wa_events.go            # whatsmeow → app bridge (shutdown-aware ctx)
    ├── health.go               # /healthz, /readyz
    ├── metrics.go              # expvar counters
    ├── handlers_*.go           # per-area HTTP handlers
    ├── csv_*.go + media.go     # CSV + multipart wrappers
    ├── contacts.go             # ContactStore (all methods ctx-aware)
    ├── migrations.go           # schema_migrations + runner
    ├── migrations/             # embedded .sql migrations
    ├── i18n*.go                # translation tables + ETag cache
    ├── ratelimit.go            # per-IP token bucket
    ├── waclient.go             # whatsmeow adapter interface
    └── web/                    # SPA bundle (dev source + Vite dist)
```

## Error model

Every translatable error in the backend implements `LocalizedError`:

```go
type LocalizedError interface {
    error
    LocalizedKey() string
    LocalizedArgs() []any
}
```

Why an interface and not a type assertion: it survives `fmt.Errorf("…: %w", err)`
wrapping. `errors.As(err, &le)` unwraps the chain and finds the localised
metadata even if a caller decorated the error mid-flight.

### Creating a new translatable error

1. Add the translation key to every locale (`i18n_en.go`, `i18n_pt.go`,
   `i18n_es.go`). Keys are stable once shipped — do not rename.
2. Reach for the constructor that fits:
   - `newErrKey("err_foo")` — most common; one key, optional args.
   - A custom struct that implements `LocalizedError` — when the error
     carries typed data the caller inspects (`*CooldownError` does this).
3. Register the static assertion in `errors.go`:

   ```go
   var _ LocalizedError = (*myErr)(nil)
   ```

   This makes the build break the moment the interface drifts.

### Surfacing it through HTTP

Handlers call either:

- `handleError(w, r, status, err)` — finds the localisation via
  `errors.As` and emits the structured envelope.
- `jsonErrorLocalized(w, r, status, key, args...)` — shortcut when the
  caller knows the key inline; equivalent to `jsonError(w, status, key, T(r, key, args...))`.

Wire shape (documented for integrators in `SECURITY.md`):

```json
{ "error": { "code": "err_phone_too_short", "message": "phone too short (minimum 7 digits, got 4)" } }
```

## SSE events

`Event{Type string, Data any}` serialises as `{type, data}` over the wire.
Every `data` payload is a typed struct (see `sse_events.go`). When you add
a new event type:

1. Define `XxxData` struct with JSON tags.
2. Producer constructs `Event{Type: "xxx", Data: XxxData{…}}`.
3. Frontend `sse.js` switches on `evt.type`; the dispatcher passes
   `evt.data` to the matching handler.

`SSEHub.Broadcast` is non-blocking — slow subscribers get their event
dropped (counted in `metricSSEDrops`). Never assume delivery.

## Broadcast lifecycle

`Broadcaster.Start(ctx, cooldown)` is the only legitimate entry into the
in-flight broadcast slot. It:

1. Takes `mu` (returns `ErrBroadcastRunning` if held).
2. Enforces `cooldown` (returns `*CooldownError` otherwise).
3. Wires a cancellable context.
4. Returns a `*StartHandle`.

The handle owns `mu` + the cancel func + `wg`. Exactly one of:

- `handle.Done()` — called by the goroutine on success (in a `defer`).
- `handle.Abandon()` — called by the handler if validation between
  `Start` and the goroutine launch fails.

… must run. Both go through `sync.Once`, so accidentally calling both is
safe; never calling either leaks the mutex forever (and the next caller
sees `ErrBroadcastRunning`).

## Shutdown ordering

`App.shutdownCtx` is cancelled at the start of `runShutdown`. Anything
that wants to wind down with the process derives from it:

- `wa_events.handleReceiptEvent` writes happen under
  `WithTimeout(app.shutdownCtx, 2s)`.
- `resetClientAsync` runs under
  `WithTimeout(app.shutdownCtx, 10s)`.
- The broadcast goroutine receives its ctx from `Broadcaster.Start`
  rooted at `context.Background()`; `cancelBroadcast("shutdown")` is
  what stops it, called separately by `runShutdown` before HTTP close.

## Storage

`ContactStore` methods all take `context.Context` as the first argument
and use `*Context` driver methods. SQLite is opened with `SetMaxOpenConns(1)`
so writes serialise (WAL lets readers proceed in parallel against the
same connection).

For migrations see `internal/server/migrations/README.md`.
