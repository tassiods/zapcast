# Production delivery and recovery

**Source-only operational guidance. This document does not authorize production
changes and does not describe a live deployment.** Source selection is recorded
in [consolidation.md](consolidation.md); use the exact reviewed Git commit and its
artifact provenance, not a historical audit SHA, as a deployment candidate.
A local green build does not certify WhatsApp delivery or a production restore.

## Deployment topology to preserve

Maintain an access-controlled deployment inventory outside this public repository:
host identities, addresses, firewall rules, proxy/SSO routes, images and runtime
paths are installation-specific. Verify that inventory before an approved change.
Preserve the existing authenticated proxy/network boundary and do not broaden
backend access, change orchestration or replace working routes incidentally.

Keep the existing authenticated reverse-proxy route, hostname, TLS and SSO redirects.
For an independently protected SSO-only path choose `AUTH_MODE=proxy` explicitly; an empty
AUTH_TOKEN is intentional only in that mode with the network boundary intact.
Set `TRUST_PROXY` to the actual trusted immediate-peer IPs/CIDRs, not a
boolean. Docker NAT can make the immediate peer the bridge gateway; validate
that address in staging and preserve backend firewall restrictions. Proxies must
overwrite forwarding headers. Supply the exact HTTPS
public origin as `FRONTEND_ORIGIN`; do not invent a hostname from this document.

For native authentication choose `AUTH_MODE=native` and supply AUTH_TOKEN from
an access-controlled secret store or untracked operator environment. Compose
passes AUTH_MODE, AUTH_TOKEN, FRONTEND_ORIGIN and TRUST_PROXY explicitly.
The application config validation rejects native mode without a token
and unprotected nonloopback binds unless proxy mode was explicitly
selected. Docker defaults to native mode, so an unconfigured deployment must
fail closed. Do not treat localhost publishing as a substitute for native
validation: the process listens on all container interfaces.

## Toolchain and release policy

- Go is exactly **1.26.9**, the supported patched line selected
  from official Go release JSON. Do not let automatic toolchain download
  silently change it (`GOTOOLCHAIN=local`). CGO is required for SQLite.
- Node is **22.23.3**, npm **12.2.0**. `npm ci` is mandatory; failure never
  falls back to `npm install`. Direct dependencies are exact versions; transitive
  integrity is recorded by package-lock. Refresh dependencies deliberately and
  rerun audit, types, units, production build and browser tests together.
- Current selected frontend versions: Playwright 1.63.0, jsdom 30.1.2,
  TypeScript 6.0.3, Vite 8.3.2 and Vitest 5.0.3.
- Lint is golangci-lint 2.14.0, including gosec. Govulncheck is 1.1.4.
  G101 remains enabled: only that rule's translation false positives in the
  exact `internal/server/i18n_es.go` and `i18n_pt.go` paths are excluded.
- Actions use verified full commit SHAs and all container bases use registry
  digests. `scripts/delivery-pins.json` records the read-only resolution.
  OS package repositories are still a build input: the base digest is immutable,
  but apk/apt packages are not a full snapshot lock. Record package inventory,
  scan the built image and review it before deployment; do not claim bit-for-bit
  container reproducibility from the base pin alone.

The autonomous semantic-release flow is **retired**, not silently trusted.
Its dependency tree included an unfixed braces advisory even at the current
upstream release. Changelog/git/exec/npm publication plugins and commitlint
were removed rather than forcing incompatible overrides or suppressing audit.
`.releaserc.json` is an inert dry-run/no-plugin migration guard, not a release
entrypoint. This deliberately changes automatic versioning to an explicit
reviewed version; future automation must meet the same artifact/security gates.

`release-semver.yml` now only plans a version, without credentials or writes.
`release.yml` is explicit workflow_dispatch with `publish=false` by default;
it calls `ci.yml` directly for the same workflow SHA and chosen version. CI
runs formatting, module verification, vet, pinned lint/security, vulnerability
scan, race+coverage (minimum 65%), frontend types/units, Vite, identified Go
`-tags prod` build, actual Chromium E2E and artifact validation. Only then is
`tested-release-<SHA>` uploaded. Publication downloads that exact artifact,
validates source/version/checksum, and never rebuilds it. Configure required
reviewers on `production-release` before permitting publication. No push/tag
from GITHUB_TOKEN is expected to start another workflow. Failed/skipped gates
cannot reach the publish job.

The supported tested release target is currently **linux/amd64**.
The former untested cross-platform matrix is removed, not represented
as certified binaries. Restore additional platforms only with their own native
CGO, production and browser gates. Publication uses the GitHub REST API with
explicit draft/upload/verify/finalize operations; live writes remain untested
until an authorized real release. Existing versions are never overwritten.

Offline commands (run only in an isolated source/staging workspace):

```sh
npm ci
npm audit --audit-level=moderate
npm run test:delivery
npm run test:types
npm test
npm run build
python3 scripts/build.py ci
ZAPCAST_E2E_BINARY="$PWD/release-artifacts/zapcast-linux-amd64" npm run test:e2e
python3 scripts/release.py --dry-run --version ci --artifacts release-artifacts
```

The E2E entrypoint requires an explicit executable binary with Go 1.26.9 and
`-tags=prod` metadata. It fails rather than running the old dev `go run` config.
The frontend fix must start `ZAPCAST_E2E_BINARY` in a disposable store, on a fresh
port, with no reuse of another server. SW installation/update, manifest assets,
SSO expiry/redirect caching and native session/logout browser tests remain
integration acceptance gates, not assertions made by this runbook.

## Containers and deployment approval checklist

The standard Dockerfile builds Vite first and embeds its bundle with `-tags
prod`, injects the explicit VERSION, and uses UID/GID 10001. It avoids copying
host node_modules over the npm-ci layer. Alpine builds use musl; the runtime-only
shell-free distroless Debian13 image wraps the tested glibc binary. Do not substitute a locally built
Debian 14 binary without checking its glibc symbol requirements against Debian
13. Image builds and runtime compatibility must be exercised in the approved
isolated Docker workspace; no local Docker daemon was available during this fix.

Compose defaults host publishing to 127.0.0.1, drops all capabilities, enables
no-new-privileges, limits PIDs/resources/logs and uses a read-only root with a
restricted tmpfs and writable store volume. Review existing store UID/GID before
switching images. Test/chown only a restored staging copy for UID/GID 10001;
never run ownership changes against production as part of source preparation.

`/healthz` is liveness (healthy process); `/readyz` checks DB and WhatsApp state.
The Docker liveness check intentionally does not cause restart loops when an
account is unpaired/offline. Before admitting real traffic, probe readiness from
an authorized proxy, inspect DB/WA status, verify SSO unauthenticated API access
redirects to the configured identity provider, and verify unauthorized direct backend probes are denied.
Readiness is necessary, not proof of reliable message delivery.

Before any future deployment, obtain separate approval for downtime/restart,
image transfer/load, backup access and production writes. Then, under that
approval, an operator must:

1. Record exact source SHA, image digest, binary SHA, Go metadata, injected
   version, package inventory, vulnerability scans and all gate logs.
2. Build/test the standard and runtime-only images in an approved
   isolated Docker workspace; test nonroot store permissions, read-only root,
   tmpfs, token fail-closed/proxy modes and health/readiness behavior.
3. Verify the production service/proxies/firewalls without changing working SSO/native paths.
4. Quiesce new campaign admission; reconcile/cancel uncertain/active work using
   the corrected durable-state policy. Never roll back while a campaign may
   still send or retry.
5. Take coordinated SQLite backups plus the separately approved host/guest recovery
   point; validate an isolated restore before replacing any runtime or schema.
6. Deploy only the identified approved artifact, preserving env/volume/network
   configuration; review readiness, auth paths, logs, version and static asset
   absence (`/ui.test.js` must not be served).
7. Perform end-to-end tests only with an explicitly authorized test account and
   consenting synthetic/test recipient. Never use production broadcasts as QA.
8. Observe reconnect/cancellation/recovery, rate limiting, memory/OOM and logs in
   the approved observation window; retain a documented rollback decision.

Rollback is NOT "run the old image on the new database". Migrations can be
forward-only. Keep the previous exact image/config and the matching pre-upgrade
backup. Stop the candidate before restoring its old database into a new volume;
review uncertain provider outcomes first to avoid replay. Keep both versions
from connecting the same WhatsApp session concurrently. Full host/guest restore
requires an isolated guest drill and separate host access/approval.

## Backup and restore procedure

`scripts/backup.py` uses SQLite's online Backup API, not copying a live DB/WAL.
It discovers SQLite files recursively, captures every table/schema (including
future durable campaign/recipient/outbox/session tables), records row counts,
checks integrity and foreign keys, hashes the snapshot and publishes a completed
private directory. WAL/SHM/journal sidecars are not copied: committed WAL rows
are incorporated into the self-contained database. Non-DB store files are
included unless excluded by the explicit session policy; symlinks and corrupt
DB-looking files fail. Synthetic tests keep the writer DB open in WAL mode and
verify new/unknown schema rows after restore.

Choose a policy explicitly, never accidentally:

- `--session-policy invalidate` omits both `session.key` and `sessions.json`.
  Restored native sessions must log in again; WhatsApp device state is not
  invalidated by this application-session policy.
- `--session-policy preserve` includes both owner-only files. Quiesce session
  changes for a coordinated snapshot; old tokens/keys/registry must correspond.
  AUTH_TOKEN and external SSO secrets come from the operator's protected secret
  store, never Git or backup scripts reading ambient credentials.

```sh
# Examples only: replace these with approved nonproduction locations.
python3 scripts/backup.py backup /isolated/store /protected/backup-new --session-policy invalidate
python3 scripts/backup.py restore /protected/backup-new /isolated/restored-new --session-policy invalidate
```

Restore refuses an existing target, path traversal, symlinks, policy mismatch,
checksum corruption, integrity/foreign-key errors and schema/row-count mismatch.
Ownership must be assigned for the staging runtime after validation. Neither
backup nor restore starts the application, network or WhatsApp client.

Each DB is transactionally consistent individually; snapshots are NOT atomic
across databases or changes to session.key. Quiesce campaign/session mutation
(or take an approved stopped/full-guest snapshot) for a coordinated recovery point.
Never make a SQLite checkpoint the substitute for online backup. Prevent
restored pending/uncertain campaign replay while conducting the restore drill:
start offline with egress denied and inspect/reconcile durable states first.

The manifest detects accidental corruption, not a hostile editor of both data
and manifest. Protect backup directories (0700)/files (0600), encrypt with
approved authenticated encryption, ship off-host, restrict access, rotate and
test retention. Backups contain WhatsApp credentials and personal data even
when session.key is omitted. Never place a backup, .env or real key in Git.
RPO/RTO, encryption keys, off-host retention and scheduled backup rollout are
operator policy/approval items; source preparation does not change running schedules.

## Integrated contracts and deployment boundary

- Authentication modes are `native`, `proxy`, `local`; `TRUST_PROXY` is an
  IP/CIDR allowlist of immediate peers, not a boolean. Browser credentials are
  HttpOnly cookies; query tokens and master-token browser storage are removed.
- Native logout is durable, individually revocable and enforced on open
  streams. Preserve/invalidate backup policies cover `session.key` AND
  `sessions.json` and must be chosen explicitly.
- The canonical E2E runner starts the identified `ZAPCAST_E2E_BINARY` on a fresh
  loopback port with an empty synthetic store. Fixture browser contracts remain
  labelled synthetic; native tests exercise the actual binary.
- History/recipient DTOs expose pending, uncertain, cancelled, interrupted and
  unresolved work. Legacy missing payload/link flags disable unsafe replay in
  the UI. Manual retry needs duplicate-risk acknowledgment; confirmation needs
  an explicit provider assertion and an operator-supplied message ID. This is
  not provider proof or an exactly-once guarantee.
- An accepted campaign has durable original recipient/body/media intentions.
  Recovery, cancellations, receipts, retries and quotas use transactional state.
  Never treat a 5xx/timeout or generic non-success counter as proof of rejection.
- Builds use fixed Go/Node/tool versions, strict lockfiles, verified action/image
  pins, production-only embed and injected artifact version. Native-runtime
  probes use the application's bounded `healthcheck` CLI, not a shell or wget.
  Standard images are musl/Alpine; runtime-only glibc images are shell-free
  distroless Debian13. UID/GID is10001; restored store ownership is a separate
  deployment action, never implicit build behavior.
- Reusable same-SHA CI gates the exact production artifact. Release is explicit
  reviewed version planning/publication, not autonomous semantic-release.
  Actual hosted CI/publication still requires an authorized branch push/run.

The final verification report and logs record the tested commit and gates.
Image builds/runtime checks require a dedicated approved QA environment with
network-none disposable stores. They do not certify production deployment, real
WhatsApp deduplication, physical host power loss, workload soak, IdP logout
semantics or host/guest recovery. Those remain separately authorized operational
checks. No production/proxy/firewall/DNS changes or real message sends are included
in this source correction.
