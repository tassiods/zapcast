# Security Policy

## Supported versions

ZapCast is currently in 1.x. The latest minor release receives security patches; older minors are unsupported.

| Version | Supported |
|---|---|
| 1.4.x | ✅ |
| < 1.4 | ❌ |

## Reporting a vulnerability

**Do not file a public GitHub issue for security problems.**

Use GitHub Security Advisories whenever possible:

<https://github.com/tassiods/zapcast/security/advisories/new>

Please include:

- affected version or commit SHA;
- reproduction steps or proof of concept;
- impact assessment;
- mitigations you have already identified.

## Response timeline

| Phase | Target |
|---|---|
| Initial acknowledgement | within 72 hours |
| Triage + severity rating | within 7 days |
| Patch or mitigation | within 30 days |

These are best-effort targets for a solo-maintained project. High-severity issues such as RCE, auth bypass and credential disclosure take priority.

## Scope

In scope:

- Go backend, HTTP handlers, persistence and auth/session logic.
- Embedded SPA under `internal/server/web/`.
- Docker image and `docker-compose.yml`.
- Release artifacts produced by this repository.

Out of scope:

- Vulnerabilities in upstream dependencies; report those to their maintainers. CI runs `govulncheck` and dependencies are updated on a best-effort basis.
- Issues requiring attacker-controlled physical access to the host.
- WhatsApp account bans or Terms-of-Service consequences from broadcast usage.

## Disclosure

We follow coordinated disclosure: a fix or mitigation is shipped before an advisory is made public. Reporters are credited in release notes unless they ask to remain anonymous.

## Hardening defaults

If you expose ZapCast beyond localhost:

- Set `AUTH_TOKEN` to a high-entropy random value, preferably 32+ characters.
- Terminate TLS in front of the listener.
- Pin `FRONTEND_ORIGIN` to the exact browser origin.
- Keep `STORE_DIR` owner-only and backed up; it contains WhatsApp session keys and app data.
- Protect `/debug/vars` the same way as the API.

Running on the public internet without TLS and authentication is unsupported.

## API error envelope

HTTP failures use a stable JSON shape:

```json
{
  "error": {
    "code": "err_phone_too_short",
    "message": "phone too short (minimum 7 digits, got 4)"
  }
}
```

- `code` is machine-readable and intended for control flow.
- `message` is localised through `Accept-Language`; do not parse it.
- HTTP status remains authoritative for retry behaviour.

### Stable codes

| Code | HTTP | Meaning |
|---|---:|---|
| `unauthorized` | 401 | Missing/invalid auth token or session. |
| `auth_disabled` | 400 | Login endpoint called when native auth is disabled. |
| `err_invalid_json` | 400 | Request body failed JSON decode. |
| `err_name_phone_required` | 400 | Contact create/update missing fields. |
| `err_phone_required` | 400 | Phone path/query missing. |
| `err_phone_too_short` | 400 | Phone < 7 digits. |
| `err_phone_too_long` | 400 | Phone > 15 digits. |
| `err_phone_starts_zero` | 400 | Phone begins with `0`. |
| `err_contact_not_found` | 404 | Contact row not found. |
| `err_contact_exists` | 400 | Duplicate phone on create. |
| `err_group_not_found` | 400 | Group row not found. |
| `err_group_exists` | 400 | Duplicate group name. |
| `err_group_empty` | 400 | Broadcast targets an empty group. |
| `err_no_contacts` | 400 | Broadcast `phones[]` matched zero rows. |
| `err_group_or_phones` | 400 | Broadcast missing both selectors. |
| `err_broadcast_running` | 429 | Another broadcast is in flight. |
| `err_broadcast_cooldown` | 429 | Broadcast cooldown window is still active. |
| `err_whatsapp_not_conn` | 400 | WhatsApp client is offline. |
| `err_qr_pending` | 429 | Another QR session is in flight. |
| `err_logout_pending` | 429 | QR session blocks logout. |
| `err_rate_limited` | 429 | Per-IP token bucket rejected the request. |
| `err_file_too_large` | 413 | Attachment exceeds the configured cap. |
| `err_upload_none` | 400 | Multipart import missing `file`. |
| `err_csv_no_contacts` | 400 | Parsed CSV produced zero valid rows. |
| `err_csv_empty` | 400 | CSV body had no rows. |

If your integration depends on a code missing from this table, open an issue so it can be promoted to the public contract.
