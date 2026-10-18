# Frontend / PWA security contract and browser verification

## Run locally

Use the pinned lockfile/toolchain and build the identified prod binary first:

```sh
npm ci
npm run test:types
npm test
npm run build
python3 scripts/build.py ci
ZAPCAST_E2E_BINARY="$PWD/release-artifacts/zapcast-linux-amd64" npm run test:e2e
```

`e2e/run.mjs` starts the exact binary in a fresh runner-owned loopback store,
with a synthetic credential, no paired WhatsApp device and no server reuse.
It first runs the synthetic HTTP/SSO browser contracts and then the real-Go
native suite. The runner stops its child and removes the disposable store.
`TMPDIR`, `E2E_ARTIFACTS` and `E2E_RESULTS` control isolated test output.

The fixture suite serves actual emitted Vite assets and exercises two real
Chromium SW updates across three builds. Its API/SSO responses are explicitly
synthetic, not Go-authorization proof. The native suite exercises actual Go
cookie login/SSE/logout/401, expired browser cookies, production PWA/icon
installation, and review UI calling the real resolution handlers. Review
inputs are synthetic rows seeded only into the fresh runner-owned database:
missing payload replay stays disabled, while operator confirmation is recorded
through the API with authoritative readback. This is not provider-confirmed
WhatsApp evidence. No pairing, QR request, real send, production data or real
credential is used. Enforced server TTL, copied-cookie revocation and stream
invalidation additionally have backend security regressions.

## App session and draft policy (F1 / F8)

- Native login submits the entered credential once in a same-origin POST to `/api/auth/login`; redirects are rejected. It is transiently present in the password input/POST body, never copied into Web Storage, Authorization headers or EventSource URLs. The input and legacy storage copies are cleared. EventSource uses `/api/events` with the browser's same-origin cookie.
- `Sair do aplicativo` posts only `/api/auth/session/logout`. It never calls the separate WhatsApp logout endpoint `/api/auth/logout` and does not cancel or disconnect WhatsApp/campaigns on the backend.
- App logout/observed auth failure synchronously clears private state, the entire private main DOM, drafts, attachments/object URLs, history and CSV module caches. In-flight private fetches/streams are aborted; late responses and pending CSV reads cannot restore private state. Other open ZapCast tabs receive a privacy-clear notification. Only ZapCast workers and `zapcast-*` caches are purged/unregistered. Unrelated origin caches remain intact. Failed server logout is explicitly reported, not claimed as revocation.
- **Draft autosave is disabled.** Draft text is only the live textarea, preserved while switching views in that page. There is no persistent draft or opt-in setting that could be inherited by another operator. Reload, logout or observed expiration discards it. This is the session/page-only safe default, not an identity-scoped persistent draft feature.
- Status is revalidated on focus and every 30 seconds while visible. 401, 403, redirected/HTML API responses and network/SSO uncertainty fail closed. Network loss also clears the private page and its PWA caches; the already loaded offline shell can show the lock screen, but an offline refresh after that purge cannot reload the shell until connectivity returns. Reopen/reload after signing in again.
- In **proxy/SSO-only mode**, `{required:false}` supplies no native operator identity. The UI cannot identify an undetected SSO account switch, instantly observe expiration in a suspended tab, or log out the identity provider using a native-cookie endpoint. No private API response is cached and no draft survives the page. The lock screen explicitly directs the operator to end the external SSO session too. Operators must close/reload the application on an SSO account change or shared-station handoff. A proxy must enforce authentication independently; this patch does **not** solve or claim an API authentication bypass.
- Raw exception text, source URLs and current-page URLs are not sent/logged by client telemetry; generic errors retain kind/line/column only.

## Immutable public shell (F3 / F4 / F5)

Vite emits the final asset list, correct manifest/icon files, per-response SHA-256 integrity and a build identifier into `/sw.js`. The identifier includes emitted bytes and the worker template, so worker bytes change with code/assets. Installation fetches without credentials and rejects redirects, no-store/private responses, wrong MIME, non-200 responses and integrity mismatches. It validates the entire release before writing; an install failure deletes only that incomplete ZapCast generation.

Runtime caching is an exact public-shell/emitted-asset allowlist, never arbitrary navigations. API/SSE/debug/SSO/health responses and query-bearing requests are not cached. HTML is the build's immutable public shell, not a network response saved as `/`. A proxy that requires SSO cookies even for shell/static files may prevent offline installation deliberately: such an authenticated/redirected shell is not precached. This is a privacy limit, not an SSO integration claim.

Update UI appears only after a complete worker is waiting. Activation/reload requires an explicit operator click. Each namespace is immutable. Old content-addressed JS/CSS requested by another still-open tab comes only from a previously validated ZapCast generation, never from a new deployment's network response. Old shell generations are retained for those tabs and purged on app logout/auth failure; unbounded release retention is a public-static quota tradeoff, not an identity cache. Source/dev Go embeds intentionally have no generated precache and cache no unversioned source modules. Only prod builds provide offline PWA caching. During migration from a legacy source-caching worker, close all ZapCast tabs or explicitly remove only the ZapCast registration/caches before reopening if a stale source-module graph prevents the new update UI from loading.

The Chromium suite additionally checks installability through CDP, every manifest icon responding 200/image, offline private-data absence, no protected data in CacheStorage, rejected SSO redirects/no-store/wrong-MIME/tampered HTML, immutable old-tab modules, and preservation of other applications' caches.

## Backend API assumptions / integration boundaries

Existing contracts only:

- `GET /api/auth/required` → `{required:boolean}` (true for native token mode; false for proxy mode).
- `POST /api/auth/login` with `{token:string}` → 2xx and a valid same-origin HttpOnly session cookie; wrong credential → 401. The frontend does not interpret a session ID or retain the credential.
- `POST /api/auth/session/logout` → revoke the native session, clear its cookie, 2xx. Backend must enforce revocation and expiration and close/revalidate active SSE streams; frontend cleanup is not an authorization boundary.
- `/api/status` and all private routes → 401/403 when the current native/proxy authorization fails. API success should be JSON, not a silently followed SSO login page.
- Cookie-only `GET /api/events`; backend query-token acceptance must be removed by the security changes independently.

The integrated UI consumes the actual durable status/review contracts: GET `/api/delivery/review`, POST `/api/delivery/:id/resolve`, and targeted POST `/api/broadcasts/:id/cancel`. Missing original payload/linkage is explicit, never reconstructed. Retry requires duplicate-risk acknowledgment; confirmation requires an operator assertion and message ID. Fixture contracts and real-Go native review readback are separate gates. Production deployment, push, restart, provider guarantees and SSO-provider configuration remain outside these tests.
