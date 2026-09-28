# PDFI2 Agent-Owned PDF Form Dialogue Exit Report

## Exit decision

PDFI2 is complete as of 2026-09-28. A supported PDF form now remains one owner-bound durable job while the agent,
rather than raw field order, decides what to ask and when. Job preparation does not open a question. Each protected
question is explicitly bound to one exposed stable field, and an accepted answer returns control to the agent instead
of silently selecting the next field.

The conversation can distinguish a protected answer from ordinary guidance. Clarify, back, optional blank, and cancel
are typed navigation intents; they are not stored as field values. Review readiness is a hard conversational boundary:
the agent presents a value-free summary, waits for a separate finish request, then uses the existing approval-bound
PDF3 transaction to mutate, verify, and deliver exactly one artifact.

This exit does not admit PDFI3-PDFI5. Large-form semantic planning, conditional-section qualification, composite
protected answers, and generic protected-input extraction remain outside this milestone.

## Merged changes

- Admission: [#1354](https://github.com/bogdanovich/mintclaw/pull/1354), merge commit
  `a2203fb83e824bfbbbaeffad71e044f3435f7157`.
- Core agent-led controls: [#1370](https://github.com/bogdanovich/mintclaw/pull/1370), merge commit
  `84ad205559cb758298cdbe7ba2fd8d887dd1e5e0`.
- Planning, recovery, bounded context, and protected receipt consumption:
  [#1387](https://github.com/bogdanovich/mintclaw/pull/1387),
  [#1394](https://github.com/bogdanovich/mintclaw/pull/1394),
  [#1395](https://github.com/bogdanovich/mintclaw/pull/1395), and
  [#1403](https://github.com/bogdanovich/mintclaw/pull/1403).
- Follow-up, correlation, review, and typed navigation hardening:
  [#1414](https://github.com/bogdanovich/mintclaw/pull/1414),
  [#1417](https://github.com/bogdanovich/mintclaw/pull/1417),
  [#1418](https://github.com/bogdanovich/mintclaw/pull/1418),
  [#1420](https://github.com/bogdanovich/mintclaw/pull/1420),
  [#1421](https://github.com/bogdanovich/mintclaw/pull/1421),
  [#1423](https://github.com/bogdanovich/mintclaw/pull/1423), and
  [#1425](https://github.com/bogdanovich/mintclaw/pull/1425).
- Live-qualification fixes: [#1427](https://github.com/bogdanovich/mintclaw/pull/1427),
  [#1429](https://github.com/bogdanovich/mintclaw/pull/1429),
  [#1431](https://github.com/bogdanovich/mintclaw/pull/1431), and
  [#1432](https://github.com/bogdanovich/mintclaw/pull/1432).
- Missing-skill regression isolation: [#1411](https://github.com/bogdanovich/mintclaw/pull/1411).

The sequence adds no form-, agency-, locale-, or field-name-specific production rule. It reuses the PDF3 form ledger,
approval, writer, verifier, artifact transaction, and outbox; it adds no daemon, broker, secret store, or delivery
queue.

## Review and CI evidence

- Final exact reviewed head: `e5f5878d944f528648e1bcee203dc9437740079c`.
- Final code merge: `8987f3a3d3a820f9af4838dd933914b0f7562ef0`.
- All 16 required exact-head checks passed for [#1432](https://github.com/bogdanovich/mintclaw/pull/1432), including
  tests, race, linter, security, integration, browser Windows, all compile targets, macOS portability, and PDFium/WASM
  qualification on Linux and macOS.
- The automated reviewer reported no high-confidence issue on the exact head at 2026-09-28T20:14:03Z. The final merge
  followed owner rocket authorization, zero unresolved review threads, and the required quiet window.
- Local final-head validation passed the complete `pkg/agent` suite, changed-file lint with zero findings, and the
  Linux integration-test binary build.

## Deployment evidence

- Host: `server@oc`.
- Previous core revision: `75908c0c518f83137c110b25cc151f24da6ada7e`.
- Deployed code revision: `8987f3a3d3a820f9af4838dd933914b0f7562ef0`.
- Recoverable backup: `/home/server/mintclaw-pdfi2-review-boundary-pre-20260928T202527Z`.
- Core, node, and launcher were built from the exact merged revision. Build and installed hashes matched before the
  affected services were restarted.
- Final status reported every expected service active, zero product, user, and system failed units, zero legacy
  processes, and zero error-level entries in the ten-minute window. Launcher HTTP was `302`; reviewer webhook HTTP
  was the expected `404`.
- All five production profiles loaded without parse or schema errors. Their existing non-zero `doctor` policy result
  was unchanged and was not treated as a configuration-load failure.

## Live acceptance evidence

The deployed test used a generic two-page synthetic AcroForm with eight fields. The initial request asked MintClaw to
explain the form and bounded plan, correct one existing value, collect only two missing values, preserve everything
else, and wait for a separate final confirmation.

| Invariant | Exit evidence |
| --- | --- |
| Summary before collection | The first response described the two-page form and plan before opening the first protected question |
| Agent choice | Every question followed an explicit stable-field selection; the backend did not walk schema order |
| Clarification | Typed clarify returned an explanation and a replacement question without appending a field value |
| Free text | Explicit protected free-text answers resumed the same job and never appeared in the ordinary response |
| Navigation | Typed back revisited the intended field; typed skip left the optional date blank; typed cancel terminated two separate test jobs |
| Correction | An existing value was corrected twice without treating the completed correction as unresolved |
| Review boundary | The agent stopped with a value-free review: 8 fields, 2 confirmed, 1 intentionally blank, 5 preserved, 0 unresolved, and 0 blockers |
| Approval | A separate finish turn produced one approval request; `allow_once` authorized only the reviewed write and verification action |
| Transaction | Form job `form_job_44a7bb29363ee5c48bcb51f858210307` completed at revision 14 with operation `document_write_a34691b867b2484ca1ea8355b90ceef7` |
| Source identity | Source SHA-256 remained `33c467eee9a6c23eaa154dc1810f6b733ba51aa65d43146fa8da19aff33868dc`; the immutable two-page source was unchanged |
| Verification | Structural assertions: 8; checked fields/widgets: 2/2; unchanged fields: 6; visual assertions: 3; rendered pages: 1 |
| Delivery | Exactly one new PDF artifact, `node-transfer-ec85d9bbfcf3a2ecf7d4db5a47055081.pdf`, was delivered after approval |
| Privacy | Both protected sentinels had zero hits in workspace state, diagnostic traces, public form-job state, write journals, and runtime logs |
| Diagnostics | Successful session turns 12-28 were redacted and untruncated; final trace `trace-turn-93e1458a4a5ed06d9b425f66` completed |

Protected test values are intentionally absent from this document.

## Qualification notes

- In a normal channel, a reply carries transport-owned interaction metadata and can be accepted as the protected
  answer. The headless `agent live` client has no reply event, so its equivalent is an explicit `/answer` or
  `--auto-answer-question`; an arbitrary new live message correctly supersedes the question as conversational guidance.
- A request to collect a field that already contains a value is ambiguous. The qualified correction request says to
  correct that value. This is test intent, not a field-name rule in production.
- Typed Cancel was verified with the installed production CLI rather than the temporary qualification helper. The
  helper and all plaintext sentinel temp files were removed after the privacy scan. Historical diagnostic traces,
  the verified artifact, and the deployment backup were retained.

## Operator test

The repeatable small-form procedure is in
[PDFI2 agent-led form dialogue test](../operations/pdfi2-agent-led-form-test.md). It separates normal channel behavior
from headless CLI correlation so a CLI message is not accidentally mistaken for a channel reply.

## Next admission boundary

PDFI3 is the next ordered candidate but is not admitted by this exit. It must qualify bounded semantic planning,
conditional sections, focused ambiguity handling, and model-context budgets on both small and large generic forms.
PDFI4 remains the coherent-section/composite-answer and large-form qualification milestone. PDFI5 remains conditional
on a second accepted non-PDF consumer.
