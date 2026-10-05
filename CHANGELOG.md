# Changelog

All notable changes to ZapCast are documented here. This repository history was reset at `v1.4.0`; older pre-reset commit links and tags are intentionally not part of the public baseline.

## [v1.4.0] - 2026-10-05

### Public baseline

- Self-hosted WhatsApp bulk messaging app built with Go, SQLite and `whatsmeow`.
- Embedded single-page web UI with Vite production bundling, Vitest unit tests and Playwright smoke tests.
- Contact, group and template management with CSV import/export support.
- Broadcast history with per-recipient state, WhatsApp receipt handling and SSE updates.
- Cookie-based browser session flow, optional `AUTH_TOKEN` API protection, CORS origin controls and security headers.
- Outbox retry/dead-letter support for transient delivery failures.
- Docker image, GitHub Actions CI and release workflow.

[v1.4.0]: https://github.com/tassiods/zapcast/releases/tag/v1.4.0
