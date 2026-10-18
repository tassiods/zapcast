# Security Policy

## Supported Versions

ZapCast is currently in 1.x. The latest minor version receives security
patches; older minors do not.

| Version | Supported          |
| ------- | ------------------ |
| Latest released 1.x minor | :white_check_mark: |
| Older minors | :x: |

## Reporting a Vulnerability

**Do not file a public GitHub issue for security problems.**

Send a private report through GitHub Security Advisories:

Open a draft advisory at
   <https://github.com/tassiods/zapcast/security/advisories/new>. Reports
   posted here remain private until coordinated disclosure.


Please include:

- A description of the issue and the version (or commit SHA) affected.
- Reproduction steps or a proof-of-concept.
- Impact assessment (what an attacker can achieve).
- Any mitigations you already identified.

## Response Timeline

| Phase                    | Target          |
| ------------------------ | --------------- |
| Initial acknowledgement  | within 72 hours |
| Triage + severity rating | within 7 days   |
| Patch released           | within 30 days  |

These are best-effort targets for a solo-maintained project. High-severity
issues (RCE, auth bypass, credential disclosure) get priority over the rest.

## Scope

In scope:

- The Go binary in this repository (handlers, persistence, crypto).
- The embedded SPA (`web/index.html`).
- The Docker image and `docker-compose.yml`.

Out of scope:

- Vulnerabilities in upstream dependencies (`whatsmeow`, `mattn/go-sqlite3`,
  etc.) — please report those to their respective maintainers. We track
  upstream advisories via `govulncheck` in CI and bump dependencies on a
  best-effort cadence.
- Issues that require attacker-controlled physical access to the host.
- WhatsApp account bans resulting from how you use the broadcast feature —
  that's a Terms-of-Service matter with Meta, not a security defect.

## Disclosure

We follow coordinated disclosure: a fix is shipped (or a mitigation is
documented) before the advisory is made public. Reporters are credited in
the release notes unless they ask to remain anonymous.

## Hardening Defaults

If you intend to expose ZapCast beyond localhost, please:

- Set `AUTH_TOKEN` to a high-entropy value (32+ random characters).
- Terminate TLS in front of the listener (nginx, Caddy, traefik).
- Restrict the `STORE_DIR` to mode `0700` and back it up — it contains the
  WhatsApp session keys; losing it forces a re-pair.
- Set `FRONTEND_ORIGIN` to the exact origin you serve the SPA from.

Running on the public internet without TLS and a token is unsupported.

## API Error Envelope

All HTTP failure responses carry a stable JSON shape so integrators can
react to specific failure modes without parsing free-form text. The wire
format is:

```json
{
  "error": {
    "code": "err_phone_too_short",
    "message": "phone too short (minimum 7 digits, got 4)"
  }
}
```

- `code` is a machine-readable identifier; treat it as a stable string.
  New codes may be introduced; existing codes are not renamed without a
  major-version bump.
- `message` is localised via `Accept-Language` (currently `en-US`,
  `pt-BR`, `es-ES`). Do not parse it for control flow.
- HTTP status is not proof that a message was rejected by the provider.
  Do not blindly repeat a broadcast/RPC after a timeout, disconnect or 5xx: inspect
  the durable campaign and review uncertain outcomes with an explicit duplicate-risk
  acknowledgment. Native-session logout and WhatsApp logout are distinct operations.

### Stable codes

The following codes are part of the public contract. Other codes (e.g.
`internal`) may appear for unforeseen failures but are not guaranteed
to keep their wording or status.

| Code                         | HTTP | Meaning                                  |
| ---------------------------- | ---- | ---------------------------------------- |
| `unauthorized`               | 401  | Missing/invalid bearer token             |
| `err_invalid_json`           | 400  | Request body failed JSON decode          |
| `err_name_phone_required`    | 400  | Contact create/update missing fields     |
| `err_phone_required`         | 400  | Phone path/query missing                 |
| `err_phone_too_short`        | 400  | Phone < 7 digits                         |
| `err_phone_too_long`         | 400  | Phone > 15 digits                        |
| `err_phone_starts_zero`      | 400  | Phone begins with `0` (missing CC)       |
| `err_contact_not_found`      | 404  | Phone has no row in `contacts`           |
| `err_contact_exists`         | 400  | Duplicate phone on create                |
| `err_group_not_found`        | 400  | Group name has no row                    |
| `err_group_exists`           | 400  | Duplicate group name                     |
| `err_group_empty`            | 400  | Broadcast targets a group with no rows   |
| `err_no_contacts`            | 400  | Broadcast `phones[]` matched zero rows   |
| `err_group_or_phones`        | 400  | Broadcast missing both selectors         |
| `err_broadcast_running`      | 429  | Another broadcast holds the mutex        |
| `err_broadcast_cooldown`     | 429  | Per-broadcast cooldown window not done   |
| `err_whatsapp_not_conn`      | 400  | WhatsApp client offline                  |
| `err_qr_pending`             | 429  | Another QR session is in flight          |
| `err_logout_pending`         | 429  | QR session blocking logout               |
| `err_rate_limited`           | 429  | Per-IP token bucket rejected the request |
| `err_file_too_large`         | 413  | Attachment exceeds 16 MiB                |
| `err_upload_none`            | 400  | Multipart import without `file` field    |
| `err_csv_no_contacts`        | 400  | Parsed CSV produced zero valid rows      |
| `err_csv_empty`              | 400  | CSV body had no rows at all              |

If you depend on a code that is missing from this table, please file an
issue so we can promote it formally.
