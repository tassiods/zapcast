# ZapCast

Contributor/agent guidance: [AGENTS.md](AGENTS.md). Source selection and deferred
changes: [consolidation decisions](docs/consolidation.md). Production safety and
release gates: [docs/production.md](docs/production.md).

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](docs/LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.26.9-00ADD8?logo=go)](https://go.dev/)
[![Built with whatsmeow](https://img.shields.io/badge/built%20with-whatsmeow-25D366)](https://github.com/tulir/whatsmeow)
[![Docker Ready](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](#docker)

> **ZapCast** is a self-hosted web app for **WhatsApp bulk messaging**. Manage
> contacts, organize them into groups, and dispatch personalized messages with
> template variables — all from a single static binary with an embedded SPA UI.

It is built on top of [**whatsmeow**](https://github.com/tulir/whatsmeow)
(`go.mau.fi/whatsmeow`) — Tulir Asokan's unofficial Go client for the WhatsApp
Multi-Device protocol — which handles authentication, session persistence and
the actual message delivery.

> **Disclaimer.** ZapCast is **not affiliated with, endorsed by, or sponsored
> by WhatsApp or Meta Platforms, Inc.** It uses an unofficial reverse-engineered
> protocol implementation. Using this software may violate WhatsApp's Terms of
> Service and can result in your number being banned. **You are solely
> responsible for how you use it.** Do not use it for spam.

---

## Screenshots

| Broadcast composer | Linked-device login | Contacts list |
|--------------------|---------------------|---------------|
| ![Broadcast composer with template variables and recipient picker](docs/screenshots/broadcast.png) | ![QR pairing screen, ready to scan from WhatsApp on the phone](docs/screenshots/login.png) | ![Contacts page with empty state before first import](docs/screenshots/contacts.png) |

---

## Table of contents

- [Features](#features)
- [Quickstart](#quickstart)
- [Configuration](#configuration)
- [Docker](#docker)
- [REST API](#rest-api)
- [CSV import](#csv-import)
- [Message templates](#message-templates)
- [Architecture](#architecture)
- [Security](#security)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License & Attribution](#license--attribution)
- [Acknowledgments](#acknowledgments)

---

## Features

- WhatsApp authentication via QR Code (whatsmeow Multi-Device)
- Full CRUD for **contacts** and **groups**
- **CSV import** that accepts native ZapCast, **Google Contacts** and
  **Outlook** export formats (PT/EN headers)
- **Template variables** in messages: `{name}`, `{phone}`, plus user-defined
  custom variables
- **Bulk send (broadcast)** to a group or to an ad-hoc selection of contacts
- Persistent **broadcast history** with per-message status (sent / delivered /
  read), updated in real time from whatsmeow `Receipt` events
- **Server-Sent Events (SSE)** stream for connection state, broadcast progress
  and receipts
- Modern dark-themed SPA, **embedded into the binary** via `go:embed`; production
  assets are built with Vite, with no Node dependency at runtime
- **Strict E.164 phone validation** (7–15 digits, no leading `0`)
- Concurrent-broadcast protection (HTTP `429` if one is already running) +
  10s cool-down
- **Structured logging** via `log/slog` (`text` or `json`)
- Multi-stage **Docker** image, runs as a non-root user

---

## Quickstart

### Prerequisites

- **Go 1.26.9**, **Node 22.23.3** and **npm 12.2.0**
- **GCC / build-base** (required to compile `mattn/go-sqlite3` — cgo)

### Run from source

```bash
git clone https://github.com/tassiods/zapcast.git
cd zapcast
go mod download
go run .
```

The server listens on `http://127.0.0.1:3000` by default. Open it in your
browser and scan the QR Code with your WhatsApp mobile app
(**Settings → Linked Devices → Link a device**).

### Build a production binary

```bash
npm ci
npm run build
python3 scripts/build.py ci
./release-artifacts/zapcast-linux-amd64
```

The production binary embeds only the built SPA, not source/test files. CGO
SQLite requires the matching system libc; it is not automatically a fully
static executable. `Dockerfile.runtime` wraps the tested glibc binary, while
standard Docker compiles for its Alpine musl runtime. Runtime-only images
use a pinned shell-free distroless Debian13 base and the application healthcheck CLI.

---

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|---|---|---|
| `HOST` | `127.0.0.1` | Listener; unsafe unauthenticated non-loopback startup is rejected. |
| `PORT` | `3000` | HTTP port. |
| `STORE_DIR` | `store` | Owner-only database, delivery and native-session state. |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `text` | Structured logging controls. |
| `AUTH_MODE` | Derived | `native` when a token exists; otherwise loopback-only `local`. Explicit `proxy` delegates authentication to a trusted gateway and conflicts with a token. |
| `AUTH_TOKEN` | Empty | Native login master credential; CLI may use an explicit Bearer header. Browser APIs/QR/SSE use the revocable HttpOnly cookie. Query credentials are rejected. |
| `FRONTEND_ORIGIN` | Empty | Exact HTTP(S) scheme/host/port origin allowed for browser callers. |
| `TRUST_PROXY` | Empty | Comma-separated trusted immediate-peer IPs/CIDRs; governs forwarding headers, not a boolean. |
| `BROADCAST_DELAY_MS` | `500` | Shared account cadence, clamped to 100–30000 ms. |
| `BROADCAST_DELAY_JITTER` | `0.3` | Cadence jitter, 0–0.9. |
| `BROADCAST_HOURLY_CAP` | `0` | Persistent sliding-hour account admission cap shared by all initial/retry sends; zero is unlimited. |

**Network exposure is fail-closed.** A non-loopback listener needs a native
token or explicit `AUTH_MODE=proxy` behind TLS/authentication and a restricted
backend firewall. Proxy mode is not its own authentication mechanism. Native
logout durably revokes only that browser session; it does not disconnect the
WhatsApp device. Key/token rotation invalidates prior native sessions.

### Operational probes

- `GET /healthz` — 200 while the process is alive (used by the Docker `HEALTHCHECK`).
- `GET /readyz` — 200 when the application DB is reachable; WhatsApp connectivity
  is reported separately and does not prevent opening the UI to pair/reconnect.

### Runtime metrics

- `GET /debug/vars` exposes runtime counters via the stdlib `expvar` package
  as a single JSON document. Use `jq` to project a single key:

  ```bash
  curl -s http://127.0.0.1:3000/debug/vars | jq '
    {
      broadcasts: .zapcast_broadcasts_total,
      ok:         .zapcast_broadcast_messages_ok,
      err:        .zapcast_broadcast_messages_err,
      sse_drops:  .zapcast_sse_drops,
      sse_subs:   .zapcast_sse_subscribers,
      goroutines: .zapcast_goroutines,
      by_route:   .zapcast_http_by_route
    }
  '
  ```

  Counters of note:

  | Var                                  | Type | Meaning                                    |
  | ------------------------------------ | ---- | ------------------------------------------ |
  | `zapcast_broadcasts_total`           | int  | Broadcasts admitted (one per `Start`)      |
  | `zapcast_broadcast_messages_ok`      | int  | Individual sends that succeeded            |
  | `zapcast_broadcast_messages_err`     | int  | Individual sends that failed               |
  | `zapcast_broadcast_aborts`           | int  | Broadcasts cancelled mid-flight            |
  | `zapcast_sse_drops`                  | int  | Events skipped due to slow subscribers     |
  | `zapcast_sse_subscribers`            | int  | Live SSE clients (gauge)                   |
  | `zapcast_rate_limit_blocks`          | map  | 429s per route pattern                     |
  | `zapcast_http_requests`              | map  | Responses by 2xx/3xx/4xx/5xx               |
  | `zapcast_http_by_route`              | map  | Responses by registered route pattern      |
  | `zapcast_client_errors`              | map  | Counts of `window.error` / promise rejects |
  | `zapcast_goroutines`                 | func | `runtime.NumGoroutine()` at scrape time    |

- `/debug/vars` follows the same cookie/Bearer boundary as private APIs. URL tokens are never accepted. Proxy mode requires the external gateway to protect this route too.

---

## Docker

A multi-stage `Dockerfile` is provided. The runtime image is based on
`alpine`, runs as the unprivileged `app` user and persists data in
`/app/store`.

```bash
# Explicit reviewed version and image; select native mode with a securely supplied token.
export ZAPCAST_VERSION=ci  # Local candidate; publication needs a new reviewed release version.
export ZAPCAST_IMAGE=zapcast:local-reviewed
export AUTH_MODE=native
# Load AUTH_TOKEN from a protected operator environment, not shell history or Git.
docker compose up -d --build
docker compose logs -f
```

The default Compose bind is loopback, the root filesystem is read-only and
capabilities are dropped. Existing SSO installations may use explicitly configured
proxy mode, not this native-mode example. Never apply a store ownership/schema change
or restart a live campaign as part of a build. See [production delivery and
recovery](docs/production.md) for versioned artifacts, migration and rollback.

---

## REST API

| Method | Route                    | Description                                          |
|--------|--------------------------|------------------------------------------------------|
| GET    | `/api/status`            | WhatsApp connection state                            |
| POST   | `/api/auth/qr`           | Start QR Code login (SSE stream)                     |
| POST   | `/api/auth/logout`       | Disconnect from WhatsApp                             |
| GET    | `/api/contacts`          | List contacts (`?q=` search, `?page=&limit=` paging) |
| POST   | `/api/contacts`          | Create a contact                                     |
| PUT    | `/api/contacts/:phone`   | Update a contact                                     |
| DELETE | `/api/contacts/:phone`   | Remove a contact                                     |
| GET    | `/api/groups`            | List groups (`?page=&limit=` paging)                 |
| POST   | `/api/groups`            | Create a group                                       |
| PUT    | `/api/groups/:name`      | Update a group                                       |
| DELETE | `/api/groups/:name`      | Remove a group                                       |
| GET    | `/api/templates`         | List custom template variables                       |
| POST   | `/api/templates`         | Create / update a template variable                  |
| DELETE | `/api/templates/:key`    | Remove a template variable                           |
| POST   | `/api/import`            | Import contacts from CSV                             |
| POST   | `/api/broadcast`         | Enqueue a bulk send (returns `202`)                  |
| GET    | `/api/broadcasts`        | Broadcast history (`?limit=N`)                       |
| GET    | `/api/broadcasts/:id`    | Per-message detail of a broadcast                    |
| GET    | `/api/stats`             | Aggregate stats (sent / delivered / read)            |
| GET    | `/api/events`            | Cookie-authenticated real-time stream (SSE)          |
| POST   | `/api/auth/login`        | Native cookie login                                 |
| POST   | `/api/auth/session/logout` | Revoke application session (not WhatsApp logout)  |
| GET    | `/api/delivery/review`   | Bounded pending/uncertain delivery review            |
| POST   | `/api/delivery/:id/resolve` | Operator confirmation or acknowledged risky retry |
| POST   | `/api/broadcasts/:id/cancel` | Durable targeted campaign cancellation            |

Pagination on `/api/contacts` and `/api/groups` is opt-in: without query
parameters they return the full array (backward-compatible); with
`?page=N&limit=M` they return an envelope `{items, total, page, limit}`.

---

## CSV import

The importer accepts CSVs with `Name`/`Nome`, `Phone`/`Telefone` and an
optional `Group`/`Grupo` column. **Google Contacts** and **Outlook** native
exports are also auto-detected.

```csv
Name,Phone,Group
John Doe,15551234567,Customers
Jane Smith,15557654321,Suppliers
```

Phones are normalised and validated against E.164 (7–15 digits, no leading
`0`). Invalid rows are reported in the response.

---

## Message templates

Variables available inside any broadcast message body:

- `{name}` / `{nome}` — the contact's name
- `{phone}` / `{telefone}` — the contact's phone
- Any custom variable created through `POST /api/templates`

Example:

```
Hi {name}! Your reference is {order_id}. — Sent on {date}.
```

---

## Architecture

```text
zapcast/
├── main.go                 Entrypoint
├── internal/server/        HTTP/auth, durable delivery, contacts, receipts
│   ├── migrations/         Ordered additive SQLite upgrades
│   └── web/                SPA source; dist/ is the production embed
├── e2e/                    Prod-binary and synthetic browser-contract gates
├── scripts/                Identified builds, gated release, backup/restore
├── docs/production.md      Staging/deployment/rollback safety procedures
├── Dockerfile              Node/Vite + Go 1.26.9 + Alpine runtime
├── Dockerfile.runtime      Tested glibc binary + Debian runtime
└── store/                  Private runtime state; never committed
```

Key design points:

- **SQLite** in WAL mode with fully synchronous application commits.
  Campaigns, rendered recipients and media snapshots are persisted before
  acceptance/RPC. Startup resumes only never-started captured intents; inflight
  and ambiguous results require review, not blind replay. Stable provider IDs
  do not prove real exactly-once delivery. Legacy missing payloads remain
  explicitly unresolved. Cancellation cannot retract remotely accepted sends.
- Tables: `contacts` (phone PK), `groups_`, `group_members`, `template_vars`,
  `broadcasts`, `broadcast_messages`
- SSE subscribers managed as `map[chan Event]struct{}` behind a `RWMutex`;
  publish is non-blocking (drops on full channels)
- Concurrent broadcasts are serialised with a mutex (HTTP `429` to the
  second caller)
- whatsmeow event callbacks (`handleWAEvent`) update `broadcast_messages`
  through the `ContactStore` API, never via raw SQL — keeping locking in one
  place

---

## Security

ZapCast ships with the following hardening defaults; see [SECURITY.md](SECURITY.md)
for the disclosure policy.

- **CSV formula injection escape** — exported contacts are sanitized so a
  malicious name like `=cmd|...` cannot execute as a spreadsheet formula.
- **Per-IP rate limit** on `/api/auth/qr`, `/api/auth/logout`, and
  `/api/broadcast` via token-bucket middleware.
- **Bearer token** auth (`AUTH_TOKEN`) using constant-time comparison.
  Startup warns when the token is short, repeats a character class, or
  matches a well-known weak value.
- **Immutable PWA generations** with emitted-asset integrity checks and explicit
  update activation; private API/debug/SSO responses never enter the shell cache.
- **Multipart body cap** via `http.MaxBytesReader` on every upload path.
- **Durable campaign cancellation** and explicit uncertain outcomes; active
  RPCs are cancelled, but a message accepted remotely cannot be recalled.
- **CI gates**: `gofmt`, `go vet`, `golangci-lint`, `gosec`, `govulncheck`,
  `go test -race`, and a 65% coverage floor on every push and PR.

> Want to expose ZapCast on the public internet? Terminate TLS in front
> (Caddy, nginx, traefik), set a 32+ character random `AUTH_TOKEN`, and
> pin `FRONTEND_ORIGIN` to the exact origin you serve from.

---

## Roadmap

### Done

- SSE pub/sub with fan-out
- Unit-test suite (`contacts_test.go`)
- Concurrency guard on broadcast (HTTP `429` + cool-down)
- Strict E.164 phone validation, applied across API and CSV import
- Pagination on `/api/contacts` and `/api/groups`
- Structured logging (`log/slog`, `text`/`json`)
- Persistent broadcast history with per-message receipt tracking
- Live broadcast progress bar via SSE
- Docker image (multi-stage, non-root)

### Short term

- Per-user role-based authorization beyond the shared native-token / proxy modes
- HTTP webhook outputs to external systems

### Medium term

- Scheduled broadcasts (date/time, persistent worker)
- Multi-account / multi-device sessions
- CSV / PDF export of broadcast history

### Long term

- Multi-process queue scaling beyond the current single-account durable SQLite worker
- Admin dashboard with advanced stats and configuration
- Internationalization (i18n) of the SPA beyond pt-BR

---

## Contributing

Issues and pull requests are welcome at
<https://github.com/tassiods/zapcast>. Please read
[CONTRIBUTING.md](docs/CONTRIBUTING.md) and the
[Code of Conduct](docs/CODE_OF_CONDUCT.md) before getting started.

Quick checklist before opening a PR:

1. `go test ./...` must pass.
2. Run `go vet ./...` and `gofmt -s -w .`.
3. Follow the conventions in [CONTRIBUTING.md](docs/CONTRIBUTING.md)
   (handler naming, `ContactStore` for DB access, structured logs, etc.).
4. By submitting a contribution you agree that it is licensed under
   **Apache License 2.0** (see [LICENSE](docs/LICENSE)).

---

## License & Attribution

### ZapCast — Apache License 2.0

ZapCast is licensed under the **Apache License, Version 2.0**. See
[LICENSE](docs/LICENSE) and [NOTICE](docs/NOTICE).

```
Copyright 2025 Tássio Dias <https://github.com/tassiods/zapcast>
Licensed under the Apache License, Version 2.0.
```

> **Required attribution.** Per Section 4(d) of the Apache License, any
> redistribution, **fork, derivative work, or substantial reuse** of this
> source code **must retain the [NOTICE](docs/NOTICE) file** and provide visible
> credit to:
>
> **Tássio Dias — <https://github.com/tassiods/zapcast>**
>
> Removing or obscuring this attribution is a license violation.

### whatsmeow — Mozilla Public License 2.0

ZapCast depends on
[**whatsmeow**](https://github.com/tulir/whatsmeow) (© Tulir Asokan), which is
distributed under the [**Mozilla Public License, Version
2.0**](https://www.mozilla.org/en-US/MPL/2.0/). ZapCast uses whatsmeow as an
**unmodified** Go module dependency.

If you redistribute ZapCast (in source or binary form), you must comply with
both licenses simultaneously. In particular, MPL-2.0 requires that:

- Recipients can obtain the source of the MPL-covered components — for
  whatsmeow this is satisfied by linking to
  <https://github.com/tulir/whatsmeow>.
- License notices contained in MPL-covered files are preserved.
- If you modify whatsmeow source files, those modifications must themselves
  be released under MPL-2.0.

Other third-party dependencies and their licenses are listed in
[NOTICE](docs/NOTICE).

### Trademarks

WhatsApp is a trademark of Meta Platforms, Inc. ZapCast is not affiliated
with, endorsed by, or sponsored by WhatsApp or Meta.

---

## Acknowledgments

- **[Tulir Asokan](https://github.com/tulir)** and the
  [**whatsmeow**](https://github.com/tulir/whatsmeow) contributors —
  ZapCast would not exist without their work on the Multi-Device protocol.
- **[mattn/go-sqlite3](https://github.com/mattn/go-sqlite3)** for the SQLite
  driver.
- The Go standard library team — `net/http`, `log/slog` and `embed` make
  shipping a single-binary web app a pleasure.
