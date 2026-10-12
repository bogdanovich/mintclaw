# Local Coding Agent P8.3 State Lifecycle Production Evidence

Date: 2026-10-12 UTC

This record contains only synthetic-state identities, hashes, counts and
operational facts. Real user threads were not deletion-canary targets.

## Source and qualification

- PR: `#1618`.
- Exact reviewed head: `56704d2133c27c8906a6b3add384070c27e87153`.
- Merge and deployed core: `3bcd308a96d5ba49a4ef6372fc93e037e4920698`.
- Prior deployed core: `37577fd81318f1d0f66830b294bb279d0c01abdf`.
- All 17 required checks passed, with clean exact-head review, no unresolved
  review threads and owner PR-level rocket approval.

Qualification covers formatting, changed-package lint, config/thread/coding
CLI/diagnostic packages, ownership inventory/runtime layout, lifecycle race
cases, shared-blob lifetime, and native Windows thread publication and purge.
The Windows matrix exposed held-writer directory-rename failure. The final
implementation retires the one-shot writer under the catalogue admission gate,
pins the destination parent and preserves no-clobber publication. Native tests
verify contention, failed retry with a fresh lease, cross-parent publication
and existing-destination refusal. The same full CI matrix passed afterward.

Local macOS and deployed Linux disposable fixtures both exercise the full CLI
plan/purge/GC sequence. No automatic startup purge, worktree cleanup, external
effect rollback or physical secure-erase guarantee is implied.

## Deployment and rollback

The clean server `main` checkout fast-forwarded to the merged source; core,
node and launcher were built before installing CLI-facing binaries and
restarting only the affected gateways and web launcher.

- Backup: `/home/server/mintclaw-core-backup-20261012T015637Z`.
- Backup manifest SHA-256:
  `3cff4a57571e802335719c41801b6341028dfc5e2f20e3bfed44fd05ebe4fcb1`.
- Core binary SHA-256:
  `3eff0519500d0972ce10e3f8d2a1d67c126eefc481ced1f2582251dde1f9407a`.
- Node binary SHA-256:
  `a237612d3fb1010d1c7a2e4abaa73872653e778dc5f34cee29f25e1db3d4e7b3`.
- Launcher binary SHA-256:
  `2ca1331cf9dce4fcdeb5bb75a7589853ce7b40815fb9cd295850b9d9f3e75128`.

The private backup preserves prior installed/build binaries, user units and
drop-ins, previous source commit and service states. Its manifest was checked
before build and again after rollout. Rollback stops only affected units,
restores these exact binaries to both installed and executed paths, restores
units only if changed, then restarts and verifies health. It does not restore
mutable thread data automatically. H4 adds no state-format migration.

## Disposable-state canary

Fixture root: `/home/server/mintclaw-p8-h4-state-canary-Ii4b0V`.
The fixture contains a 45-day-old trashed thread, a two-day-old trashed thread,
an active thread, one old shared blob referenced by the old trash, and a
separate checksummed project sentinel.

| Check | Observed result |
| --- | --- |
| GC before purge | One referenced blob; zero deletion candidates |
| Store-wide purge dry run outside a repository | Two roots scanned; only the expired root selected; zero deletions |
| Explicit confirmed purge | One attempted and completed root; no incomplete root |
| Isolation after purge | Recent trash and active thread remain; project/source unchanged |
| Blob after purge | Still present until separate GC |
| Separate confirmed GC | One unreferenced blob removed, 35 bytes |
| Receipt privacy | No private title, project sentinel content or absolute server path |
| Receipt digests | All six receipt payloads match their checksum manifest |

The purged synthetic trash ID is
`e987dd13-2bf9-4155-a808-3f48b235ffa1-20260828T020403.800178614Z-d6be3a39-1318-4244-9ba9-201ffa16ba4c`.
The project sentinel SHA-256 remained
`5185d92506ce663b5a0d29a17b41e7c343c400d575111e2a295a693918c4684e`.
The receipt manifest SHA-256 is
`4d3ebe3866ee6348d30fcf427693e49157b2383d27d5af364f367c037f0e6572`.
The source checkout remained clean at the deployed commit. The retained
fixture is private evidence, not production user state.

## Runtime and diagnostics

The deployed profile completed a stateless harmless marker turn without tool
calls or Telegram delivery. Its exact response SHA-256 is
`50e96b69e616ca338a935bac9946f58baffde002714a15a3279ec3ad1c03215d`
(16 bytes). Passive trace `trace-turn-2d95cd2ef73ff6d115713946` records:

- schema `mintclaw.diagnostic_trace.v1` and the full deployed commit;
- terminal status `completed`, six records and no truncation/drop;
- `redacted_content` with `mintclaw.config_filter.v1`; and
- turn/model/event/session-hash joins and no tool-call records.

Metadata-only `diagnostics show` used exact-ID mode, scanned one 17,537-byte
file and returned one valid trace with zero corrupt, expired, incomplete or
problem files. No rich preview was needed to verify terminal truth.

## Observation window

At `2026-10-12T02:13:31Z`, more than ten minutes after restart, all ten expected
user services were active. Product/global failed-unit counts, legacy process
count and every ten-minute error-level journal count were zero. Launcher HTTP
returned 302 and reviewer webhook GET returned the expected 404. The clean
source checkout remained at the deployed commit, the backup manifest verified
again, and the preserved previous CLI executed `version` successfully without
opening live state.

H6 remains the separate integrated release gate; H4 completion alone does not
declare the whole coding surface stable.
