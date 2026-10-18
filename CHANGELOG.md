# Changelog

All notable changes to ZapCast are documented here. This repository history was reset at `v1.4.0`; older pre-reset commit links and tags are intentionally not part of the public baseline.

## [Unreleased]

### Added

- Durable recipient/body/media intentions, explicit uncertain-delivery review and transactional receipt reconciliation.
- Recovery and cancellation fences with shared persisted cadence for initial attempts and retries.
- Revocable native sessions, private-page cleanup and immutable production PWA generations.
- Complete escaped campaign messages in history, including narrow-screen coverage.
- Identified production builds, exact-artifact browser gates and offline backup/restore tooling.

### Changed

- Consolidated the reviewed functional source onto the existing public history without a history rewrite.
- Selected updated SQLite/Whatsmeow dependencies and patched Go 1.26.9 / `golang.org/x/net` 0.60.0.
- Kept strict dependency pins and explicit same-SHA publication; superseded push/PR CI checks may be cancelled without cancelling manual release gates.
- Build provenance now treats nonignored untracked source as dirty.
- Public operational guidance excludes installation-specific topology and private contact details.

## [v1.4.0] - 2026-10-05

### Public baseline

- Self-hosted WhatsApp bulk messaging app built with Go, SQLite and `whatsmeow`.
- Embedded single-page web UI with Vite production bundling, Vitest unit tests and Playwright smoke tests.
- Contact, group and template management with CSV import/export support.
- Broadcast history with per-recipient state, WhatsApp receipt handling and SSE updates.
- Cookie-based browser session flow, optional `AUTH_TOKEN` API protection, CORS origin controls and security headers.
- Outbox retry/dead-letter support for transient delivery failures.
- Docker image, GitHub Actions CI and release workflow.

[Unreleased]: https://github.com/tassiods/zapcast/compare/v1.4.0...HEAD
[v1.4.0]: https://github.com/tassiods/zapcast/releases/tag/v1.4.0
