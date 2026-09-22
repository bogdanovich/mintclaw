# PDFI1 Natural-Language PDF Form Orchestration Exit Report

## Exit decision

PDFI1 is complete as of 2026-09-22. A short ordinary request can select the deferred `document` tool, inspect the exact
source, enter the existing protected PDF3 form workflow, continue the same job through opaque protected receipts, use
the existing review and approval boundary, and deliver one verified output without exposing protected values to normal
model context.

This exit does not admit PDFI2-PDFI5. Composite intake, cross-document fact planning, large-form qualification, and a
generic protected-input extraction remain not started. XFA expansion, PDF crypto, and macOS parity remain in their
existing roadmap lanes.

## Merged changes

- Admission: [#1332](https://github.com/bogdanovich/mintclaw/pull/1332), merge commit
  `35cef4e6a80126dad2829980adcb65ec65dfecd5`.
- Implementation: [#1334](https://github.com/bogdanovich/mintclaw/pull/1334), merge commit
  `7a4f9977a5da3b629e7b25d5c52fc94a0e86f785`.
- The implementation preserves the deferred-tool boundary and updates the bundled PDF skill plus the compact
  model-facing `document` contract. It does not add another daemon, state machine, writer, delivery queue, or protected
  store.
- Direct `fill` remains the expert one-shot path for an explicitly complete, unambiguous stable-ID assignment map.
  Ordinary incomplete conversational form work routes through `form/start` instead.

## Review and CI evidence

- Exact reviewed head: `197e77a471e86f0eaefcd75886896658b2422b2b`.
- GitHub Actions run `35702361963` passed Frontend, Linter, Security Check, Tests, Race, Darwin and Windows compilation,
  macOS Portability, Integration Tests, and Browser Windows.
- Automated re-review reported no high-confidence issues on the exact head and recorded reviewer writeback
  `59a856a36cfe99672ce2`.
- The owner supplied a PR-level rocket reaction before merge. The final pre-merge check found no unresolved review
  threads and reported the PR clean and mergeable.
- Focused Linux vertical tests passed for natural protected answers and approval-bound commit/delivery. The macOS
  hard-cancel/process-loss regression passed five consecutive focused runs after accepting the valid POSIX `EPIPE`
  terminal outcome while retaining strict terminal-state assertions.

## Deployment evidence

- Host: `server@oc`.
- Previous core revision: `f41b47d52142a68f96238942619744583d99562a`.
- Deployed core revision: `7a4f9977a5da3b629e7b25d5c52fc94a0e86f785`.
- Recoverable backup: `/home/server/mintclaw-deploy-backups/pdfi1-20260922T082233Z`.
- Core, node, and launcher builds completed for the merged revision. Installed CLI copies were replaced atomically.
- The affected core profile services and launcher were restarted. Final status reported all expected services active,
  zero product and global failed units, zero legacy processes, zero error-level entries in the ten-minute window,
  launcher HTTP `302`, and the expected reviewer webhook HTTP `404`.
- The live authenticated gateway smoke returned `MINTCLAW_DEPLOY_SMOKE_OK`.
- The resulting passive trace was `trace-turn-612674c018ec10fe79ce4ed9`: schema
  `mintclaw.diagnostic_trace.v1`, status `completed`, eight records, redacted content, and no truncation.

## Acceptance evidence

| Invariant | Exit evidence |
| --- | --- |
| Ordinary entry | Agent vertical test begins with `Fill this attached form. Ask me only for the missing information.` and performs deferred discovery, inspect, and `form/start` |
| Protected continuation | Natural channel replies resume the same job through opaque receipts; no `/answer`, field ID, or interaction ID is required from the operator |
| Interaction lifecycle | Existing channel-owned questions, status, correction, cancel, review, approval, commit, and terminal states remain on the owner-bound PDF3 job |
| Privacy | `MINTCLAW_PDF3_PRIVACY_OK` and focused negative assertions keep raw protected sentinels out of ordinary model context, history, traces, public state, and delivery metadata |
| Correctness | `MINTCLAW_PDF3_SOURCE_UNCHANGED_OK`; PDF2 fill, structural verification, visual verification, and recovery markers passed |
| Delivery | `MINTCLAW_PDF3_SINGLE_DELIVERY_OK` and the agent vertical test prove one verified artifact delivery across continuation and recovery |
| Natural orchestration | `MINTCLAW_PDFI1_NATURAL_INTAKE_OK` passed from the exact deployed source tree |
| Deployment | Aggregate smoke emitted `MINTCLAW_PDFI1_DEPLOYED_OK` with `core_sha=7a4f9977a5da3b629e7b25d5c52fc94a0e86f785` and `scratch=clean` |

## Operator test

The copy-pasteable test is in [PDFI1 natural form test](../operations/pdfi1-natural-form-test.md). Its opening request is
ordinary language and does not tell the operator to call a tool, select an action, or provide internal IDs. The test
also covers status, correction, cancel, approval, one attachment, and truthful unsupported states.

## Next admission boundary

PDFI2 is the next ordered candidate, but it is not automatically admitted by this exit. It should begin only when
grouped protected questions are worth the added validation and partial-error semantics. PDFI5 remains conditional and
must not begin until a second accepted non-PDF consumer demonstrates that extraction is useful rather than speculative.
