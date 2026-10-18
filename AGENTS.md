# ZapCast contributor and agent guidance

## Scope and safety

ZapCast is a self-hosted Go/Whatsmeow/SQLite service with an embedded SPA/PWA.
The separate ZapCast Chrome extension is standalone: do not assume API integration
or shared storage. Read `README.md`, `docs/production.md` and this file first.

Do not use real contacts, credentials, paired stores or WhatsApp sends in tests.
Use fake adapters and disposable synthetic SQLite stores. Real pairing/sends,
production changes, migrations of real data, deployment, restarts and publication
require explicit current authorization. Cancellation cannot retract an accepted
message; a connection is not delivery proof. Never claim exactly-once delivery.

Preserve pending diffs. Never reset another checkout or rewrite public history.
`store/`, `.env`, session keys, backups and real message/contact data are private;
never read them for ordinary code work or include them in evidence/publication.

## Stack and build

- Go 1.26.9, `GOTOOLCHAIN=local`, CGO and a C compiler for `go-sqlite3`.
- Node 22.23.3, npm 12.2.0; strict `npm ci`, exact versions and lockfile.
- Plain HTML/CSS/JS under `internal/server/web/`; Vite/Vitest/TypeScript/Playwright.
- Development embeds the source tree. Production runs `npm run build`, then
  `python3 scripts/build.py ci` (or an explicitly reviewed release version).
- `release-artifacts/` must be empty before building. Production output is
  linux/amd64 with `-tags prod`, an injected version and provenance/checksums.
- Node is not required in the deployed application runtime.

## Architecture and contracts

- `main.go`: server entrypoint and bounded `healthcheck` CLI.
- `server.go`: App, boot, route registration and ordered shutdown.
- `contacts.go`, `delivery.go`, `outbox.go`, `migrations/`: persistence/delivery.
- `auth_config.go`, `session.go`, `session_store.go`, `stream_auth.go`: auth.
- `sse.go` + `sse_events.go`: nonblocking fan-out and typed payloads.
- `web/`: SPA, private-session lifecycle, immutable production PWA generations.

Persist the full recipient/body/media intent transactionally before provider
operations. Treat transport/ACK/timeouts and uncertain commits as ambiguous, not
proven rejection. Keep pending, inflight, uncertain, cancelled and confirmed states
distinct. Stable message IDs aid correlation, not an exactly-once guarantee.
Retries share durable cadence/quota admission. Review ambiguous legacy work through
`GET /api/delivery/review` and `POST /api/delivery/{id}/resolve`; retry requires an
explicit duplicate-risk acknowledgment. Do not recreate it as a new broadcast.

Native browser auth uses HttpOnly cookies, revocable sessions and no master-token
browser storage or query tokens. `AUTH_MODE` is `native`, `proxy` or loopback-only
`local`. Proxy mode requires an independently protected SSO/network boundary;
`TRUST_PROXY` accepts actual immediate-peer IPs/CIDRs, never a boolean.

New schema changes require new numbered migrations; never edit released migrations
in place. Keep database transitions in ContactStore methods. Use contextual calls,
structured `slog`, localized structured API errors and escaped/whitelisted UI text.
`Broadcaster.Start` returns a handle: exactly one Done/Abandon path must execute.
SSE is best effort, never a durable delivery authority.

## Verification and publication boundary

Run in an isolated checkout, with temporary files under the approved `TMPDIR`:

```sh
go mod verify
go vet ./...
go test -race -coverprofile=coverage.out -covermode=atomic ./...
npm run test:delivery
npm run test:types
npm test
npm run build
python3 scripts/build.py ci
ZAPCAST_E2E_BINARY="$PWD/release-artifacts/zapcast-linux-amd64" npm run test:e2e
python3 scripts/release.py --dry-run --version ci --artifacts release-artifacts
```

The canonical browser runner exercises synthetic browser contracts and the exact
production binary on fresh loopback ports/stores. Never reuse a real session.
Keep fixture results distinct from native-binary and provider acceptance.
CI also runs pinned lint/security/vulnerability scans and a 65% coverage floor.
Release requires same-SHA gates and consumes the tested artifact without rebuilding.
A dirty local artifact is a validation candidate, not an approved release.
Container/production/provider acceptance and publication need separate approval.

See `docs/consolidation.md` for source selection and deliberately deferred changes.
This file is authoritative over historical `.CLAUDE.md` guidance.
