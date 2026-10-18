# Source consolidation decisions

This document records source selection, not a release or deployment record.

## Sources

- Public history base: `a75331786a74295db26b38db1e9e1b98d7f9aa7d` (`master`).
- Functional source: `8fef1d96333a9910ec9ba0dd6687ea2076480769`, plus the four
  preserved uncommitted history/review changes from the original checkout.
- Those histories have no common ancestor. Consolidation starts on public master
  and applies the selected source as ordinary changes on a descendant branch.
  No force-push, old-history merge, tag or deployment is required.

The original checkout remains untouched. The candidate has no runtime store,
real WhatsApp pairing, production configuration or copied credentials. Public
operational guidance omits private host addresses, identities and topology.

## Keep from the local implementation

Durable recipient/body/media snapshots, uncertain-result fences, explicit operator
review, transactional receipts/cancellation/recovery, persisted cadence, SQLite
FULL synchronous mode, revocable native sessions, trusted-proxy boundaries,
cookie-only browser credentials, private-page cleanup, production PWA generation
integrity, backup/restore helpers and same-artifact CI/release gates are preserved.

The pending history changes show the complete recorded campaign message as escaped
text, preserve line breaks and wrap long content on narrow screens. Their existing
unit/browser tests are included, without asserting real provider acceptance.

## Selectively take from the public baseline

- Remote `go.mod` and `go.sum` dependency updates, including SQLite 1.14.52,
  Whatsmeow revision `68df7a5ec7b5`, Protobuf 1.36.12 and x/time 0.16.0.
  Selection is not a claim that all dependencies are vulnerability-free.
- The current vulnerability scan rejected the original Go 1.26.8 / x/net 0.59.0
  pins. The candidate therefore uses Go 1.26.9 and x/net 0.60.0, with matching
  build/E2E/release guards, CI, documentation and a registry-verified Go image
  digest. The official Go archive checksum was verified before use. Other image
  pins retain their original verification date; no container build is implied.
- Generic accented-name/contact fixtures, retaining legitimate copyright/NOTICE.
- Correct contributor guidance for Node/Vite, handler layout, production assets,
  isolated browser testing and immutable migrations.
- Agent guidance adapted to the stronger local contracts, rather than importing
  obsolete token-in-URL or optional-auth instructions.
- Cancellation of superseded push/PR CI checks, scoped by caller workflow and ref.
  Manual release gates remain non-cancelling. Same-SHA artifact gates are retained.
- Independent review identified that build provenance excluded untracked source.
  The dirty check now includes all nonignored untracked files; a real disposable
  Git-repository regression distinguishes new source from ignored build output.

The remote Permissions-Policy regression is already covered by
`TestSecurityPermissionsPolicyOmitsInvalidBluetooth`; no duplicate test is needed.

## Deliberately not imported

- TypeScript 7 and caret dependency ranges: preserve the pinned TypeScript 6.0.3
  toolchain and lockfile; a major compiler upgrade is a separate validated change.
- Automatic localStorage drafts: proxy/SSO mode has no per-operator identity scope.
- Query-string auth tokens, browser master-token retention or non-revoking logout.
- The older broad service-worker caching strategy and development-embed Dockerfile.
- Multi-platform publishing without native CGO/browser gates for each platform.
- Automatic tag releases, unpinned tools, npm-install fallback or weakened gates.
- Wholesale documentation shortening, history replacement or attribution removal.

## Acceptance boundary

Verification logs, original pending patch, source manifest and final report are
kept outside the repository. Local tests use synthetic databases/fake adapters;
Chromium tests distinguish fixtures from the real production binary. No test
pairs WhatsApp or sends real messages. Containers, production, hosted CI, live
release and provider acceptance remain outside this consolidation's authorization.

Review the full public diff for privacy, commit/push only when authorized, then
build and validate that exact clean commit. Retain the public-baseline changelog
instead of reintroducing links to the discontinued private/old history.
Any deployment or real-store migration needs a separate approval and recovery plan.
