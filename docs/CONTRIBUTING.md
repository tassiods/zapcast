# Contributing to ZapCast

Thanks for your interest in improving ZapCast. This document covers local setup, code conventions and pull-request expectations.

By participating, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Reporting issues

Before opening an issue:

1. Search existing issues to avoid duplicates.
2. Use the bug or feature request template.
3. For bugs, include ZapCast version/commit, Go version, OS/architecture, reproduction steps, expected vs. actual behaviour and relevant redacted logs.

Never include real WhatsApp session data (`store/device.db`), real contact phone numbers, message bodies or `store/zapcast.db` contents in a public issue.

### Security issues

Do not open a public issue for vulnerabilities. Use GitHub Security Advisories:

<https://github.com/tassiods/zapcast/security/advisories/new>

## Development setup

### Prerequisites

- Go 1.26+
- Node.js 22+ and npm for frontend tests/builds
- C toolchain for cgo (`mattn/go-sqlite3`)
- A spare WhatsApp account for QR testing; do not use your primary number

### Bootstrap

```bash
git clone https://github.com/tassiods/zapcast.git
cd zapcast
go mod download
npm ci
go run .
```

The server listens on `http://127.0.0.1:3000`; runtime DBs are created under `./store/`.

### Useful commands

```bash
gofmt -s -w .
go vet ./...
go test ./...
go test -race -coverprofile=coverage.out ./...

npm test
npm run test:types
npm run build
npm run test:e2e

go build -trimpath -o zapcast .
go run ./cmd/seed
```

## Code conventions

### Go

- Keep `gofmt -s` clean and `go vet ./...` passing.
- Public HTTP failures should use the structured JSON error envelope; avoid raw `http.Error` for API routes.
- Logs use `log/slog` with key/value pairs.
- Handlers live in `internal/server/handlers_*.go` or area-specific files and should be named `handle*` or clear area verbs such as `listContacts`.
- Database access goes through `ContactStore`; handlers and WhatsApp callbacks should not write raw SQL directly.
- `ContactStore` methods take `context.Context` first.
- Phone numbers must be normalised/validated through the shared helpers.
- Schema changes require a new migration file under `internal/server/migrations/` and a test.

### Frontend

- Source lives under `internal/server/web/`.
- Keep the app framework-light: plain HTML/CSS/JS plus Vite for bundling/tests.
- Default Go builds embed the raw web directory; production builds run `npm run build` and then `go build -tags prod`.
- Frontend behaviour should have Vitest coverage where practical; browser-level flows belong in `e2e/` Playwright specs.

### Tests

- Go unit/integration tests live next to code as `*_test.go`.
- Use existing test helpers such as `newTestStore(t)` and fake WhatsApp adapters.
- Run relevant Go and npm tests before opening a PR.

## Pull requests

1. Fork and create a topic branch.
2. Keep changes focused.
3. Update docs when routes, env vars, build steps or public behaviour change.
4. Fill in the [PR template](../.github/PULL_REQUEST_TEMPLATE.md).
5. Be responsive to review comments.

For large changes such as new endpoints, schema migrations, delivery semantics or frontend restructuring, open an issue first to align on approach.

## Commit messages

Use Conventional Commits where practical:

```text
<type>(<optional scope>): <short imperative summary>
```

Common types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`, `chore`.

## Licensing of contributions

ZapCast is licensed under [Apache License 2.0](LICENSE). By contributing, you agree that your contribution is licensed to the project under Apache-2.0 and that the [NOTICE](NOTICE) attribution requirement applies.

New source files should carry an SPDX header:

```go
// SPDX-FileCopyrightText: <year> <Your Name> <optional contact>
// SPDX-License-Identifier: Apache-2.0
```

## Project values

- Operate within the law and WhatsApp's Terms of Service.
- Reject spam, evasion and impersonation features.
- Protect user privacy: never log message bodies, contact phone numbers or session material at default verbosity.
- Keep the deployment model simple: one Go service with embedded web assets and optional Docker packaging.
