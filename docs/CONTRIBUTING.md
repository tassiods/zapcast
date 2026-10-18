# Contributing to ZapCast

Thanks for your interest in improving ZapCast! This document describes how
to set up the project locally, the conventions the codebase follows, and
the rules for opening issues and pull requests.

By participating, you agree to abide by the
[Code of Conduct](CODE_OF_CONDUCT.md).

---

## Reporting issues

Before opening an issue, please:

1. Search [existing issues](https://github.com/tassiods/zapcast/issues) to
   avoid duplicates.
2. Use the appropriate **Issue Template** (Bug Report or Feature Request).
3. For bug reports, include:
   - ZapCast commit / version
   - Go version (`go version`)
   - OS / architecture
   - Reproduction steps
   - Expected vs. actual behaviour
   - Relevant log lines (preferably with `LOG_LEVEL=debug LOG_FORMAT=json`)

> **Never** include real WhatsApp session data (`store/device.db`),
> phone numbers of real contacts, message bodies, or anything from
> `store/zapcast.db` in a bug report.

### Security issues

Do **not** open a public issue for security vulnerabilities. Instead, send
a private report to the maintainer via
[GitHub security advisories](https://github.com/tassiods/zapcast/security/advisories/new).

---

## Development setup

### Prerequisites

- **Go 1.26.9**, with `GOTOOLCHAIN=local`
- **Node.js 22.23.3 and npm 12.2.0** for frontend tests and production assets
- **GCC / build-base** (cgo, required by `mattn/go-sqlite3`)
- Automated tests use isolated synthetic stores and fake WhatsApp adapters.
  Pairing or real sends require separate explicit approval and a dedicated test account.

### Bootstrap

```bash
git clone https://github.com/tassiods/zapcast.git
cd zapcast
go mod download
npm ci
go run .
```

The server listens on `http://127.0.0.1:3000`. SQLite databases are
created on first run under `./store/`.

### Useful commands

```bash
go test ./...                 # run the unit-test suite
go vet ./...                  # static analysis
gofmt -s -w .                 # format the codebase
npm run test:delivery         # release/backup/integration contracts
npm run test:types
npm test
npm run build                # production web assets
python3 scripts/build.py ci  # identified linux/amd64 production binary
ZAPCAST_E2E_BINARY="$PWD/release-artifacts/zapcast-linux-amd64" npm run test:e2e
```

Use a new isolated checkout and an empty `release-artifacts/` for the build.
The E2E runner creates disposable stores and tests both synthetic browser
contracts and the supplied production binary; it never reuses a paired store.
See [production delivery gates](production.md) for the complete release policy.

---

## Code conventions

These conventions exist to keep the codebase navigable; please follow them.

### Go code

- **Formatting:** `gofmt -s` clean. PRs that aren't will fail review.
- **Vet:** `go vet ./...` must pass.
- **Imports:** standard library first, then third-party, then internal.
- **API errors** use `handleError`, `jsonErrorLocalized` or the structured
  `jsonError` envelope; avoid raw `http.Error` for API routes.
- **Logging** uses `log/slog`; always pass key/value pairs:
  `logger.Info("broadcast started", "id", id, "count", n)`. Don't
  reach for `log.Printf`.
- **HTTP handlers** live in `internal/server/handlers_*.go` or area-specific
  files and use `handle*` or clear area verbs such as `listContacts`.
- **Database access** goes exclusively through `ContactStore` methods in
  `contacts.go`. The store owns its locking — do not write SQL directly
  from handlers or whatsmeow event callbacks.
- **whatsmeow events** mutate `broadcast_messages` only via
  `ContactStore` methods; keep state transitions inside the event
  handler so handlers don't double-write.
- **Phone numbers** must always be normalised through `cleanPhone` and
  validated through `validatePhone` (E.164: 7–15 digits, no leading
  `0`).
- **SSE publish** is non-blocking by design — keep it that way.

### Frontend (`web/`)

- Plain HTML/CSS/JS with Vite for production bundling and Vitest for units.
  Keep the app framework-light; do not add a framework incidentally.
- The SPA is embedded into the binary via `go:embed`; **rebuild** the
  binary after editing `web/` (`go run .` re-embeds automatically).
- Default Go builds embed the development source tree. Production uses
  `npm run build` followed by `go build -tags prod`; prefer `scripts/build.py`
  to identify the exact tested artifact. Browser tests must use that artifact.

### Tests

- Unit tests live alongside the code (`*_test.go`).
- Use the `newTestStore(t)` helper to obtain an isolated, temp-backed
  `ContactStore`.
- New behaviour should ship with a test where reasonable.
- Run `go test ./...` locally before pushing.

### Commit messages

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <short, imperative summary>

<optional body explaining the why>
```

Common types in this repo: `feat`, `fix`, `refactor`, `perf`, `docs`,
`chore`, `test`. Examples:

- `feat(broadcast): support ad-hoc contact selection`
- `fix(auth): handle GetQRChannel error and tighten SSE unsubscribe`
- `refactor: extract assignGroups helper`

Keep the summary line under ~72 characters.

---

## Pull requests

1. **Fork** and create a topic branch: `git checkout -b feat/short-name`.
2. Make focused commits — one logical change per commit when practical.
3. Ensure the checklist passes locally:
   - `gofmt -s -w .`
   - `go vet ./...`
   - `go test ./...`
4. Open the PR against `master`. Fill in the
   [PR template](../.github/PULL_REQUEST_TEMPLATE.md) — describe the
   motivation, what changed, and how you tested it.
5. Be responsive to review comments. Squashing or rebasing on request is
   fine.

### Scope

Small, focused PRs are merged faster than large ones. If you're planning
something significant (new endpoint, schema migration, frontend
restructure), **open an issue first** to align on the approach.

### Schema migrations

`ContactStore` performs migrations on startup. New columns/tables must:

- Be additive when possible (avoid destructive changes).
- Tolerate older databases (use `IF NOT EXISTS` / `ALTER TABLE … ADD
  COLUMN` guards).
- Be covered by a test that runs against a freshly initialised store.
- Ship as new numbered migration files; never edit an already released migration.

---

## Licensing of contributions

ZapCast is licensed under the [Apache License 2.0](LICENSE).

By submitting a contribution, you certify that:

- You wrote the contribution yourself or have the right to submit it
  under the project's license.
- Your contribution is licensed to the project under **Apache License
  2.0**, and you grant the patent license described in Section 3 of
  that license.
- You agree that the [NOTICE](NOTICE) file's attribution requirement
  applies to your contribution as a whole work.

New source files should carry an SPDX header:

```go
// SPDX-FileCopyrightText: <year> <Your Name> <optional contact>
// SPDX-License-Identifier: Apache-2.0
```

---

## Project values

- **Operate within the law and within WhatsApp's Terms of Service.**
  ZapCast is a tool for legitimate, opt-in messaging (customer
  notifications, alerts, internal communication). Features whose only
  purpose is to enable spam, evade rate limits, or impersonate users
  will not be accepted.
- **Privacy first.** Never log message bodies, phone numbers, or session
  material at default verbosity. Be careful with what flows into
  telemetry.
- **Single-binary deployment.** Node/Vite are build-time tools, not production
  runtime dependencies. Keep one Go service with embedded production web assets.

Thanks again for contributing!
