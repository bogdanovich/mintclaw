# PDF3 Exit Record

## Decision

PDF3 is complete for `linux/amd64`. MintClaw can run one owner-scoped conversational workflow for a supported
AcroForm: retain protected answers across turns and restart, map them to the frozen PDF2 field schema, accept natural
text and channel choices, apply corrections, present a bounded review, require the configured approval, commit the
exact reviewed assignment set through PDF2, and deliver one independently verified PDF through the existing outbox.

The source remains unchanged. Missing, ambiguous, conflicting, invalid, low-confidence, stale, unaudited,
unreviewed, unapproved, canceled, expired, deleted, or unauthorized state cannot reach the PDF2 mutation boundary.
This is a workflow capability of the existing deferred `document` tool; it does not add another model tool, service,
worker binary, task registry, writer, or sender.

## Merged implementation series

| Scope | Pull request | Merge commit |
| --- | --- | --- |
| Admission and frozen architecture | [#1249](https://github.com/bogdanovich/mintclaw/pull/1249) | `b6c58b369ca9b3b5d62bcb36f85936fc311ec979` |
| Protected job store and encrypted ledger | [#1251](https://github.com/bogdanovich/mintclaw/pull/1251) | `b932cc4dbfe6704c3835534bf0265378f2046bbe` |
| Protected interactions and natural replies | [#1260](https://github.com/bogdanovich/mintclaw/pull/1260) | `33dca139853332aded6e5a1fa67e78f4a884166c` |
| Schema-bound mapping and correction | [#1263](https://github.com/bogdanovich/mintclaw/pull/1263) | `8905619dd91e063c6f7842daf1b0589938e6bc31` |
| Deliberative audit and redacted review | [#1266](https://github.com/bogdanovich/mintclaw/pull/1266) | `a6c4057435cdf19f004a94a7ea195fe476de87a4` |
| Approval-bound PDF2 commit | [#1272](https://github.com/bogdanovich/mintclaw/pull/1272) | `240d4058b3e24733ab3e489588e2692fc52982bd` |
| One-result delivery and deployed harness | [#1273](https://github.com/bogdanovich/mintclaw/pull/1273) | `23fdcbdad70c445c9d70660de1a2d784cb8ee3e7` |
| Terminal status projection | [#1274](https://github.com/bogdanovich/mintclaw/pull/1274) | `170512c8e73ae40335544b3130cdc1203d17e695` |
| Durable local-selector projection | [#1275](https://github.com/bogdanovich/mintclaw/pull/1275) | `e471f30ff021be88bb14a0772267db96a4cbe0e2` |
| Tool-feedback selector projection | [#1276](https://github.com/bogdanovich/mintclaw/pull/1276) | `601a885f911bfef0eb5db335e7cf85da3d214248` |

Every code PR passed the ten required CI checks, exact-head automated review, clean mergeability, zero actionable
unresolved threads, and owner rocket approval. The four-cycle architecture checkpoint on #1275 confirmed that the
changes remained inside `pkg/agent` and `pkg/tools`; fan-out projection was centralized instead of adding another
state machine or caller-owned privacy rule.

## Delivered contract

The completed slice provides:

- one public job snapshot plus an append-only XChaCha20-Poly1305 protected ledger, atomically stored under the
  profile authority with owner- and revision-bound associated data;
- deterministic start, continue, status, correction, cancel, expiry, deletion, recovery, and terminal behavior;
- one protected interaction sink that accepts a button, reply text, ordinary natural text, or voice transcription
  without exposing the answer to canonical history or requiring `/answer` syntax;
- stable PDF2 field IDs, deterministic type/choice validation, provenance, confidence, ambiguity/conflict state,
  confirmed-fact reuse, and append-only superseding corrections;
- one configured deliberative audit role and an ephemeral job-scoped protected view; a fallback is accepted only when
  it is explicitly frozen as equivalent by policy;
- one value-free review/approval binding over the owner, source/schema, revision, assignment digest, action, and
  expiry;
- one deterministic PDF2 operation identity and one outbox-owned media delivery identity across restart, retry,
  cancellation, timeout, definite failure, and ambiguous acceptance; and
- bounded, path-free status and failure projections for the agent, interactions, jobs, tasks, runtime events, logs,
  traces, and delivery records.

Raw answers are materialized only in the protected ledger and the in-memory PDF2 handoff. Ordinary history contains
opaque event references and state. A channel may retain the operator's own inbound message according to that
channel's policy; MintClaw does not copy it into its ordinary durable model history.

## Schemas, model policy, and document backends

The shipped durable and audit revisions are:

- public form job: `document_form_job.v1`;
- protected envelope: `document_form_envelope.v1`;
- review: `mintclaw.document_form_review.v1`;
- audit prompt: `mintclaw.document_form_audit.v1`;
- PDF2 fill map: `mintclaw.document_fill_map.v1`;
- PDF2 write journal: `mintclaw.document_write_operation.v1`; and
- document report/capability envelope: `mintclaw.document_report.v1` and
  `mintclaw.document_capabilities.v1`.

The deployed `main` profile loaded `tools.document.audit_model=gpt-5.6-sol` with no equivalent fallback. All five
active profiles passed config/schema loading. The runtime continues to use the qualified PDF2 backends: `pdfcpu`
`v0.15.0` for field semantics/writing and Ubuntu Poppler `24.02.0` for independent visible verification.

## Automated validation

The series added and passed focused unit, persistence/AEAD, authority, privacy-negative, interaction/channel,
mapping, correction, audit-policy, approval, PDF2 handoff, restart/compaction, race/cancellation, expiry/deletion,
journal recovery, outbox no-replay, agent-loop, and delivery tests. Shared-package changes also passed the full
`pkg/document`, `pkg/tools`, and `pkg/agent` suites and affected race tests. Every branch was formatted with `make fmt`
and passed changed-package lint before CI.

The checked-in aggregate release gate is:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

On the final deployed revision it returned a clean scratch directory, an unchanged source digest, a successful
agent/channel operation, and all PDF1A/PDF2 markers plus:

```text
core_sha=601a885f911bfef0eb5db335e7cf85da3d214248
marker=MINTCLAW_PDF3_PROTECTED_STORE_OK
marker=MINTCLAW_PDF3_INTERACTION_UX_OK
marker=MINTCLAW_PDF3_RESTART_COMPACTION_OK
marker=MINTCLAW_PDF3_MAPPING_REVIEW_OK
marker=MINTCLAW_PDF3_APPROVAL_OK
marker=MINTCLAW_PDF3_PDF2_COMMIT_OK
marker=MINTCLAW_PDF3_SOURCE_UNCHANGED_OK
marker=MINTCLAW_PDF3_SINGLE_DELIVERY_OK
marker=MINTCLAW_PDF3_PRIVACY_OK
marker=MINTCLAW_PDF3_CLEANUP_OK
marker=MINTCLAW_PDF3_DEPLOYED_OK
```

That run produced operation `document_write_72c72e8c1a9e425e92dcac84d17f0bf1`, output SHA-256
`712296b3eb9269ca373b9e8f503bab2e29508d3e2782b5abc57bebc49828ff93`, and retained no protected scratch.

## Deployment and health

The final merge was deployed to `server@oc` on 2026-09-19 UTC.

- Immediate previous repository/runtime revision: `e471f30ff021be88bb14a0772267db96a4cbe0e2`.
- Active repository/runtime revision: `601a885f911bfef0eb5db335e7cf85da3d214248`.
- Active version: `mintclaw v0.1.0-p8a.2-2053-g601a885f` built with Go `1.26.6`.
- Installed core SHA-256: `f85e073bab8fc9d9b40bb4255cc2e03ecffae833568d70502de75a2f109f7aec`.
- Installed companion SHA-256: `27d4109444c4ab06d8f16b8257056fec88b1b3ec224da8c3d5e6017c7f4a5941`.
- Installed launcher SHA-256: `7db162e8a91b8f3c349a5d6d618adcdcf3872b6de760372a2ed35b2d591746af`.
- Final compact rollback set:
  `/home/server/mintclaw-pdf3-privacy-hotfix-backup-20260919T163926Z` (26 files, 143,925,741 bytes,
  `SHA256SUMS` verified).
- Earlier retained PDF3 rollback points:
  `/home/server/mintclaw-pdf3-final-backup-20260919T160450Z`,
  `/home/server/mintclaw-pdf3-hotfix-backup-20260919T124713Z`, and
  `/home/server/mintclaw-pdf3-backup-20260919T112950Z`.

`make build`, `make build-node`, and `make build-launcher` completed before installation. Main, family, nutrition,
reviewer, spouse, main-web, and `node-p5a-canary` were restarted explicitly; all expected user/system services were
active afterward. Product, global user, and global system failed-unit counts, legacy process count, and bounded
ten-minute error journals were zero. Launcher HTTP was 302, reviewer webhook HTTP was its expected 404, and public
HTML was its expected unauthenticated 401.

`mintclaw doctor` loaded every active profile with `error=0`. Exit status 2 represented pre-existing policy findings:
main 14 fail/8 warning/7 info; family 7/5/4; nutrition 7/6/1; reviewer 8/5/2; spouse 9/4/6. PDF3 required no live
configuration or mutable-state migration during the final hotfix deploy.

## Live workflow, verification, and one delivery

The full authenticated live workflow used session `pdf3-deployed-20260919b` and reached completed job
`form_job_445d7bfde0070d921b49a412b06672e7`. Approval committed the reviewed state through PDF2 operation
`document_write_0f05ea6195864f709ef71318ccec3458` without changing the source.

The resulting `filled-document.pdf` is 3,571 bytes with SHA-256
`0bd43ecac4ca8f2acd3abf5bbfa0de24d1bf9f7fdc825541c6c69be6b4b4fe7e`. Independent verification recorded eight
structural assertions, four visual assertions, and three checked fields. The retained opaque artifact ref is
`media://node-transfer-e465cb7bd1b733273bc0f1b1b094ea65`.

Exactly one media outbox record, `out_07610392f40d08228ac4b65773090ff9`, owns that artifact. It is `delivered`,
`recovery_settled=true`, and `attempts=1`; its recovery metadata binds the same form job, PDF2 operation, owner digest,
and domain delivery identity. Restart and status reconciliation did not allocate another fill or media send.

## Privacy incidents and final proof

The exit process intentionally treated live privacy scans as release gates rather than documentation checks. Two
gaps were found and fixed before completion:

1. The first deployed local-selector exercise showed that canonical root-message and runtime fan-out projections were
   not one coherent boundary. #1275 centralized longest-first, non-mutating selector projection and added durable,
   steering, runtime-event, and diagnostic regressions.
2. The next deployed exercise found the same selector twice in session JSONL inside assistant
   `ToolFeedbackExplanation`: once through the user-message fallback for tool search and once through a document call.
   It was absent from traces, logs, public job state, memory, media, artifacts, and journals. #1276 projects both
   fallback and provider-supplied explanations before session/context persistence and outbound tool feedback, and
   classifies any unprojected explanation as sensitive diagnostic evidence.

No historical evidence was rewritten or deleted. The synthetic incident session and trace remain available for
regression diagnosis.

After deploying #1276, a new unique synthetic local selector was admitted through an authenticated live request. The
agent returned the exact marker `MintClaw text fixture`, page 1, total pages 1. Session
`pdf3-privacy-601a885f-20260919` contains three `[local PDF selector omitted]` receipts and zero copies of the exact
selector. Exact scans also returned zero matching files in sessions, traces, all public state, jobs, memory,
artifacts, and logs, plus zero matching journal lines.

Trace `trace-turn-005751f153606065a6ab91eb` is `mintclaw.diagnostic_trace.v1`, completed, `redacted_content`, 29
records, and untruncated. It records one successful `tool_search_tool_bm25`, two successful `document` calls/results,
a completed turn, and a sent final delivery without retaining the selector. The live request ID was
`43dcfc14-e2bc-4520-95c1-e2d920764674`.

All disposable source copies, rendered-page copies, and operation scratch used by the deployed tests were removed
from their active paths. The 8.4 GB regenerable PDF3 staging directory was permanently purged after verification;
small fixtures and visuals were moved to the host/user trash. Durable job/outbox evidence, passive traces, historical
incident sessions, delivered artifacts, and rollback sets were preserved.

## Manual channel checklist

Use only `pkg/document/testdata/acroform-fields.pdf` or another synthetic disposable supported AcroForm:

1. Attach the fixture in a fresh Telegram conversation and ask MintClaw to start a conversational form job, discover
   its fields, collect only the values needed for a verified result, and avoid shell or ordinary file reads.
2. Answer one prompt with a channel button and another with ordinary natural text. Do not use `/answer` or an
   interaction ID. Confirm that one answer advances one field and duplicate delivery does not append twice.
3. Ask to correct one completed field. Confirm that the next review reflects the correction and the earlier review or
   approval can no longer commit.
4. Restart only the profile gateway, return to the same chat, and ask for form status. Confirm that the job resumes,
   confirmed fields are not re-asked, and no raw value is repeated in status text.
5. Resolve every required blocker, inspect the bounded review, and approve the exact final action. Confirm that one
   `filled-document.pdf` arrives and no second file arrives after another status request or gateway restart.
6. Independently run `pdftotext` and render each affected page. Confirm the requested synthetic values are present,
   legible, unclipped, and mapped to the intended widgets; confirm the source digest is unchanged.
7. Start a second job, cancel it before review, and confirm that later continue/commit requests return a terminal
   refusal and produce no artifact.
8. Run `scripts/document-deployed-smoke.sh --host server@oc`; all PDF3 markers above must be present before accepting
   a new deployment.

The checked-in automated suites remain the deterministic release gates. This checklist is an operator-visible repeat
of the workflow, not a substitute for privacy scans, independent verification, and outbox inspection.

## Rollback, retention, and deletion

The final hotfix changed no configuration or durable schema, so its safe binary rollback target is the immediately
previous `e471f30f` runtime in `/home/server/mintclaw-pdf3-privacy-hotfix-backup-20260919T163926Z`. Stop only the seven
units listed above, restore each executable by the digest in `binary-map.tsv`, restore a config/unit file only if it
was changed independently, start the same units, and rerun status, doctor, live smoke, and privacy scan. Verify
`SHA256SUMS` before restoration.

Do not roll mutable protected-job, interaction, session, PDF2 journal, media, or outbox state backward after a newer
runtime has written it. A full downgrade to a pre-PDF3 binary is unsupported while PDF3 jobs exist: preserve the
profile key and ledger generation and deploy a forward fix. An unsupported schema/envelope version fails closed rather
than falling back to plaintext or guessing state.

Cancel and expiry make a job permanently non-committable. Explicit deletion removes the job's protected records and
leaves only the bounded path/value-free tombstone required for replay refusal and retention enforcement. Filesystem
deletion has the normal host-storage limitation: it does not claim forensic erasure from snapshots, backups, or an
already compromised root account. Loss or mismatch of the profile key makes protected records unreadable and
uncommittable; it never enables plaintext recovery.

## Residual limits and next boundary

- PDF3 is available only on qualified `linux/amd64`. macOS implementation remains in the roadmap parity lane and must
  independently prove protected storage, permissions, restart/compaction, deletion, PDF2 worker/backends, packaging,
  live channel behavior, privacy, recovery, and rollback before advertising the workflow.
- PDF3 supports the AcroForm field/backend matrix admitted by PDF2. It does not add XFA mutation, flattening,
  password/decryption support, signed/restricted-document modification, OCR, tables, redaction, transforms,
  generation, or companion placement.
- Model interpretation is a bounded proposal/audit step, not legal, tax, immigration, financial, medical, or business
  correctness advice. Unsupported or unresolved semantics remain fail-closed.
- Browser PDF viewers remain non-authoritative. All final mutation and verification goes through PDF2.
- PDF1B provider-native transport, PDF1C protected passwords, PDF4 XFA, PDF5 broader transformations, PDF6 companion
  placement, and the macOS parity lane each require a separately admitted goal. This exit record starts none of them.
