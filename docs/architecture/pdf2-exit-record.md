# PDF2 Exit Record

## Decision

PDF2 is complete for `linux/amd64`. MintClaw can discover supported AcroForm fields, accept an explicit typed field
map, produce a new PDF without modifying the source, verify its normalized values and visible appearances, register
one owner-scoped artifact, and deliver it through the existing outbox. The same service backs the CLI and the single
deferred `document` agent tool.

The operator-facing commands are:

```sh
mintclaw document fields --input <pdf> --json
mintclaw document fill --input <pdf> --fields <json-file> --output <pdf> --json
mintclaw document verify --input <pdf> --expect <fill-report.json> --json
```

`fill` always performs structural and visible verification before publication. `verify` is a reproducible diagnostic
check against the exact journaled operation and registered artifact; it cannot bless an unrelated copy. `flatten`
remains explicitly unavailable because `pdfcpu` `v0.15.0` has no qualified form-flatten operation and discarding
annotations without independently proving incorporated appearances could lose data.

## Merged implementation series

| Scope | Pull request | Merge commit |
| --- | --- | --- |
| Admission and frozen goal | [#1201](https://github.com/bogdanovich/mintclaw/pull/1201) | `1a457c969ca420011ef5c390efb1b9323ae1cfad` |
| Field discovery and independent oracle | [#1206](https://github.com/bogdanovich/mintclaw/pull/1206) | `c41712a2627ec483816d96d5530b6d2ebeafc9b2` |
| Durable write journal | [#1219](https://github.com/bogdanovich/mintclaw/pull/1219) | `ae55686fdbb25817c2295403262d496d0983e8b8` |
| Isolated writer and structural verification | [#1224](https://github.com/bogdanovich/mintclaw/pull/1224) | `ee7e0cbf2fca9ef6d5d592769b96f442994ba927` |
| Poppler appearance verification | [#1228](https://github.com/bogdanovich/mintclaw/pull/1228) | `ebcac3c762d648efcc718bb1c1b5b9f5d838f7c6` |
| Transactional coordinator and recovery | [#1234](https://github.com/bogdanovich/mintclaw/pull/1234) | `63cda8c42ccf3f8da1e9e2779b5d517ffca3bd78` |
| Public CLI workflow | [#1237](https://github.com/bogdanovich/mintclaw/pull/1237) | `c3fc40fff3ac4d4529ab4a655dde9c290e73917b` |
| Deferred agent tool and durable delivery | [#1240](https://github.com/bogdanovich/mintclaw/pull/1240) | `4aa7922c614b393b464244cf576fe8417d1af880` |
| Deployed acceptance harness | [#1245](https://github.com/bogdanovich/mintclaw/pull/1245) | `bc89c8cfe374860a95f5cf6cd8cc3f3eeb703868` |
| Journal-bound cross-turn verify authority | [#1246](https://github.com/bogdanovich/mintclaw/pull/1246) | `97d5469ce7f89e50bdd7b13ba721297f472d2489` |

Every non-documentation PR passed all ten required CI checks, exact-head automated review, clean mergeability, no
actionable unresolved thread, and owner rocket approval. Review-driven changes remained inside the admitted document,
agent delivery, MediaStore, outbox, and gateway recovery boundaries; they did not add another service, worker type,
sender, model tool, or persistence system.

## Delivered contract

The completed slice provides:

- one `pkg/document` authority for field semantics, typed assignments, immutable source identity, write state,
  worker execution, structural and visual verification, and path/value-free reports;
- one-shot, descriptor-only worker execution with a scrubbed environment, private operation storage, bounded input,
  output, pages, pixels, time, descendants, cancellation, and deterministic scratch cleanup;
- stable source-bound field and widget IDs for ordinary text, multiline and static date text, checkbox, radio,
  editable combo, multi-select list, Unicode, and repeated widgets covered by the synthetic fixture matrix;
- typed refusal for invalid or ambiguous values, unsupported fields/actions, missing glyphs, clipping, stale or
  misattributed appearances, overlapping widgets, limits, XFA, encryption, signatures, restrictions, malformed input,
  verification failure, journal uncertainty, registration failure, and delivery uncertainty;
- a durable owner-scoped state machine from acceptance through writing, verification, registration, and one terminal
  delivery outcome, with exact-request retry returning the same generation;
- atomic no-replace CLI publication and one `MediaStore`/outbox-owned agent artifact named `filled-document.pdf`;
- one deferred `document` tool and one on-demand PDF skill, so unrelated turns do not pay for separate form tools; and
- journal-bound later verification: the current owner must present the exact operation and artifact ref, and the
  stored size and SHA-256 must still match. Arbitrary historical media refs remain denied.

Raw field values, form bytes, rendered pages, and host paths remain inside the active protected call or private
generation. Ordinary durable tool history, reports, task metadata, outbox metadata, logs, and diagnostic traces retain
only hashes, counts, typed state, backend identity, operation/delivery IDs, and opaque refs.

## Backend, fixture, and oracle evidence

Production field discovery and writing use the already pinned Apache-2.0 `pdfcpu` `v0.15.0` module inside the existing
worker. Visible verification uses Ubuntu Poppler `24.02.0` at 144 DPI:

- `pdftoppm` SHA-256: `207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8`;
- `pdftotext` SHA-256: `0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7`; and
- independent test reader: BSD-3-Clause `pypdf` `6.1.1`, installed only in an ephemeral qualification environment.

The canonical synthetic fixture is `pkg/document/testdata/acroform-fields.pdf`, SHA-256
`33c467eee9a6c23eaa154dc1810f6b733ba51aa65d43146fa8da19aff33868dc`. It has eight logical fields and ten widgets
across two pages. The versioned field and visual manifests also bind the refusal fixtures, backend versions,
executable hashes, expected values and states, affected pages, 8-by-8 normalized difference goldens, and tolerance.
No personal, government, tax, immigration, medical, or financial document is release evidence.

Both independent oracles were rerun on the exact deployed checkout in a newly created and then removed environment:

```text
production=pdfcpu version=v0.15.0
oracle=pypdf version=6.1.1
marker=MINTCLAW_PDF2_FIELDS_ORACLE_OK
page=1 changed_pixels=28601 mean_difference=0.000000
page=2 changed_pixels=1647 mean_difference=0.000000
visual=poppler version=24.02.0 dpi=144
oracle=pypdf version=6.1.1 golden=normalized-difference-grid tolerance=0.002
marker=MINTCLAW_PDF2_FORM_WRITE_ORACLE_OK
```

The reproducible local commands are:

```sh
make test-document-fields-oracle
make test-document-form-write-oracle
make test-document-form-cli
make test-document-form-agent
```

The oracle targets require the pinned `PDF2_PYTHON`; the ordinary runtime does not install or invoke Python.

## Deployment and automated smoke

The final merge was deployed to `server@oc` on 2026-09-14 UTC.

- Immediate previous repository/runtime revision: `bc89c8cfe374860a95f5cf6cd8cc3f3eeb703868`.
- Active repository/runtime revision: `97d5469ce7f89e50bdd7b13ba721297f472d2489`.
- Active version: `mintclaw v0.1.0-p8a.2-1952-g97d5469c`.
- Active build and installed core SHA-256:
  `00cea8088b55c439cce03120f5af898822bb44efa0f6ec68dff90904bd775547`.
- Initial PDF2 recovery bundle: `/home/server/mintclaw-pdf2-deploy-backup-20260914T213259Z`.
- Pre-hotfix recovery bundle: `/home/server/mintclaw-pdf2-verify-hotfix-backup-20260914T221027Z`.
- `make build`, `make build-node`, and `make build-launcher` completed before both restarts.
- Main, family, nutrition, reviewer, spouse, and main-web were the only restarted services. All five gateway process
  hashes matched the active core; the launcher process matched its newly built artifact.
- Config doctor loaded all five profiles. Exit status 2 represented the existing policy findings; no load/schema error
  occurred and PDF2 changed no config.
- All ten expected services were active. Product/global failed units, per-service and aggregate ten-minute errors,
  legacy processes, and active legacy-name files were zero. Launcher HTTP was 302 and reviewer webhook HTTP was its
  expected 404.
- The source checkout had zero tracked changes. The previously installed Playwright `node_modules` remained the one
  known untracked runtime dependency and was preserved across both fast-forward operations.

The checked-in aggregate command:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

ran on the final deployed checkout and returned all component markers plus:

```text
core_sha=97d5469ce7f89e50bdd7b13ba721297f472d2489
state=succeeded
scratch=clean
source_unchanged=true
marker=MINTCLAW_PDF2_FIELD_DISCOVERY_OK
marker=MINTCLAW_PDF2_FORM_FILL_OK
marker=MINTCLAW_PDF2_STRUCTURAL_VERIFY_OK
marker=MINTCLAW_PDF2_VISUAL_VERIFY_OK
marker=MINTCLAW_PDF2_SOURCE_UNCHANGED_OK
marker=MINTCLAW_PDF2_JOURNAL_RECOVERY_OK
marker=MINTCLAW_PDF2_SINGLE_DELIVERY_ID_OK
marker=MINTCLAW_PDF2_CLEANUP_OK
marker=MINTCLAW_PDF2_DEPLOYED_OK
```

## Live channel, artifact, and trace proof

An authenticated `agent live` request named a workspace-local copy of the canonical fixture, supplied one unambiguous
`full_name` mapping, prohibited shell and `send_file`, and requested normal document delivery. The running main gateway
returned `outcome=success` and `MINTCLAW_PDF2_LIVE_FORM_OK` in 22.3 seconds.

The durable operation `document_write_04076b56426241c392933edd23aa5962` reached `delivered` at revision 8. Its one
artifact was 2,693 bytes with SHA-256 `ea945672c8ed7f195edffac5965b69d7a71c8452342cafd60e198804ce0db442`.
Verification recorded eight structural assertions, two visual assertions, one checked field and widget, seven unchanged
fields, and one rendered page. Its single media outbox intent was delivered in one attempt and bound the same operation,
domain delivery ID, outbox ID, and opaque artifact ref.

The first live trace, `trace-turn-2a9ee7898abd0f98243e6a4f`, used schema
`mintclaw.diagnostic_trace.v1`, `redacted_content`, 45 records, completed outcome, and no truncation. Records 14/15,
21/22, and 28/31 show successful `inspect`, `fields`, and `fill`; records 29/30 show the one artifact delivery. The model
then made an unnecessary diagnostic `verify` call. It failed `source_not_authorized` because the generated output ref
was not in the current-input allowlist even though its operation journal owned it. This was the first divergence, not a
fill or delivery failure, and produced the focused regression and fix in #1246.

After deploying #1246 and restarting the gateway, a second request resumed the same durable session and asked only to
verify the prior output without refilling or redelivery. It returned
`MINTCLAW_PDF2_CROSS_TURN_VERIFY_OK`. Trace `trace-turn-9bd237de9e11479f708df877` completed with 36 records and no
truncation; records 28/29 contain exactly one successful `document verify`, with no `fill`. The journal remained revision
8 and the original outbox remained delivered with `attempts=1`.

Exact scans found zero live value or host-path occurrences in both traces, the operation journal, media outbox, and
other ordinary workspace state. The registered artifact digest matched the journal. Independent Poppler text readback
found the synthetic value, and a fresh page-1 render was visually inspected at 1224 by 1584 pixels: the value was legible
inside its widget with no clipping or overlap. The inspected PNG SHA-256 was
`e200260cd901306c955fe65c357cc4411c21ed2b346fd9ee86f88ab1c3516119`; its temporary copies were removed.

## Manual channel checklist

Use only the checked-in synthetic fixture or another non-sensitive disposable form:

1. Attach `pkg/document/testdata/acroform-fields.pdf` to a fresh Telegram turn.
2. Send: `Inspect this AcroForm, discover its fields, fill only full_name with "MintClaw manual PDF2 check", and send
   the one verified filled PDF. Do not use shell or send_file.`
3. Confirm that exactly one `filled-document.pdf` arrives and the source attachment is unchanged.
4. Download the result and run `pdftotext filled-document.pdf -`; the requested value must appear exactly once.
5. Render page 1 with `pdftoppm -f 1 -l 1 -singlefile -png filled-document.pdf pdf2-page-1` and visually confirm that
   the complete value is legible inside the first text widget.
6. Reply in the same conversation: `Verify the PDF you just produced using its exact artifact ref and operation ID.
   Do not fill or send it again.` Confirm success and no second PDF delivery.

The checked-in CLI, agent, channel, recovery, and deployed smokes are the deterministic release gates; this checklist
lets an operator repeat the same visible behavior without using personal data.

## Rollback

PDF2 introduced no configuration schema or mutable-data migration. For a hotfix-only rollback to the reviewed #1245
runtime, preserve current journal/outbox/media state, stop only the six affected services, restore the previous bytes,
and start the same services:

```sh
backup=/home/server/mintclaw-pdf2-verify-hotfix-backup-20260914T221027Z
repo=/home/server/src/mintclaw
systemctl --user stop \
  mintclaw-main.service mintclaw-family.service mintclaw-nutrition.service \
  mintclaw-reviewer.service mintclaw-spouse.service mintclaw-main-web.service
install -m 0755 "$backup/build/mintclaw-linux-amd64" "$repo/build/mintclaw-linux-amd64"
install -m 0755 "$backup/build/mintclaw-node" "$repo/build/mintclaw-node"
install -m 0755 "$backup/build/mintclaw-launcher-linux-amd64" \
  "$repo/build/mintclaw-launcher-linux-amd64"
install -m 0755 "$backup/local-bin/mintclaw" /home/server/.local/bin/mintclaw
install -m 0755 "$backup/local-bin/mintclaw-node" /home/server/.local/bin/mintclaw-node
install -m 0755 "$backup/local-bin/mintclaw-launcher" /home/server/.local/bin/mintclaw-launcher
systemctl --user start \
  mintclaw-main.service mintclaw-family.service mintclaw-nutrition.service \
  mintclaw-reviewer.service mintclaw-spouse.service mintclaw-main-web.service
```

Both recovery bundles contain source state, service state, user units and drop-ins, binary copies, and checksum
manifests. Do not roll back mutable document, media, outbox, task, or session data over newer writes. After restoration,
run status, doctor, and `scripts/document-deployed-smoke.sh --host server@oc` again.

## Residual limits and stop boundary

- PDF2 is available only on `linux/amd64`. `darwin/arm64`, `darwin/amd64`, Linux ARM, Windows, and all other tuples
  remain typed unavailable until the separate macOS/platform parity lane passes the same worker, fixture, oracle,
  packaging, CLI, agent, channel, privacy, recovery, deployment, and rollback gates.
- Signed, certified, timestamped, rights-enabled, restricted, encrypted, malformed, hybrid-XFA, pure-XFA, calculated,
  signature, pushbutton, action-dependent, ambiguous, over-limit, missing-font, clipped, stale-appearance, and
  unsupported-field inputs remain fail-closed.
- `flatten` is withheld. The current output remains an AcroForm PDF with verified appearances.
- The milestone accepts an explicit field map for one protected active call. It does not collect or retain sensitive
  facts conversationally, infer legal/business meaning, validate a completed form's real-world correctness, or provide
  legal, tax, immigration, financial, or medical advice.
- PDF1B provider-native transport, PDF1C passwords/decryption, PDF3 protected multi-turn fact collection, PDF4 XFA,
  PDF5 OCR/tables/redaction/transforms/generation, PDF6 companion placement, and macOS implementation require their own
  admitted goals. This exit record does not start them.
