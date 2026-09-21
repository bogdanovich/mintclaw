# PDF4H Exit Record

## Decision

PDF4H is complete for its admitted `linux/amd64` slice. MintClaw can positively classify a bounded hybrid
AcroForm/XFA document, fill an explicit unambiguous field map, regenerate appearances, remove the admitted redundant
XFA and usage-rights state, flatten the widgets, preserve the allowed permission envelope, independently verify the
result, and deliver exactly one print-ready PDF through the existing document service and outbox.

The source stays immutable. The derivative is deliberately non-editable: it does not claim to preserve XFA,
AcroForm interactivity, signatures, usage rights, accessibility, or producer-specific behavior. Unknown authority,
dynamic XFA, scripts, content signatures, certification, passwords, insufficient permissions, unsafe actions,
ambiguous mappings, or failed verification still produce a typed refusal. No I-134 filename, digest, form identifier,
field identifier, agency rule, or synthetic value was added to production admission, mapping, or write logic.

## Merged implementation series

| Scope | Pull request | Merge commit |
| --- | --- | --- |
| Goal, safety boundary, and acceptance contract | [#1293](https://github.com/bogdanovich/mintclaw/pull/1293) | `90d26c826af0d4972e40944dfff1f0f190a2bf09` |
| Safe inspection and complete blocker projection | [#1296](https://github.com/bogdanovich/mintclaw/pull/1296) | `36d2a6cec71706879c77d0d1cadd035883cc896c` |
| Hybrid authority, permission, and usage-rights admission | [#1304](https://github.com/bogdanovich/mintclaw/pull/1304) | `18c55d4d9b724583b154d9b0207a173fb96f57d8` |
| Generic fill, appearance, flatten, and CLI qualification | [#1313](https://github.com/bogdanovich/mintclaw/pull/1313) | `b22ef7952d4b25664a97c91440647a58f3299c8f` |
| Live harness, trace privacy, and one-delivery validation | [#1320](https://github.com/bogdanovich/mintclaw/pull/1320) | `1c21aa477cb659d3b82b00b9e84da9791bf441de` |
| Worker startup isolation | [#1323](https://github.com/bogdanovich/mintclaw/pull/1323) | `ecb5a2993217fa9e7f8dc1507d548ccbe49b2fa9` |
| Exact deferred-tool discovery evidence | [#1324](https://github.com/bogdanovich/mintclaw/pull/1324) | `7a0f53812c8cc2c991599a138805d3baa1e9398c` |
| Verify authority bound to operation and artifact | [#1325](https://github.com/bogdanovich/mintclaw/pull/1325) | `d55a6f6945d26ebb2e40bbbac76f40ec80d1cf55` |
| Local-path redaction in persisted CLI evidence | [#1326](https://github.com/bogdanovich/mintclaw/pull/1326) | `2ca32ba4e0ae0b03ba78ac7bff2234ad558c9b1e` |

Every code PR passed its focused tests, required CI, exact-head automated review, actionable-thread resolution, and
authorized PR-level rocket gate. The series reused the existing document worker, journal, media store, outbox, and
single deferred `document` tool. It added no second daemon, broker, database, model-visible PDF tool, browser editor,
desktop automation path, approval bypass, or form-specific production branch.

## Reproducible qualification

The checked-in end-to-end command is:

```sh
MINTCLAW_BINARY=/path/to/deployed/mintclaw \
scripts/document-hybrid-live-qualification.sh \
  --input /path/to/pinned-input.pdf \
  --fields /private/path/fill-map.json \
  --case-prompt /private/path/case-prompt.txt \
  --private-values /private/path/private-values.txt \
  --output /private/path/verified-output.pdf \
  --config /path/to/profile/config.json \
  --expected-sha256 e887f9dbb4660ff9eec47d740c00358e45fcfb9c04a9776d8d95cda2e7a5e0d8 \
  --expected-pages 10 \
  --expected-fields 250 \
  --expected-assigned-fields 11 \
  --evidence-dir /private/path/evidence
```

The harness first runs the deterministic CLI oracle, then starts a real authenticated `mintclaw agent live --json`
turn. The prompt requires exactly one deferred-tool discovery followed by `inspect`, `fields`, one `fill`, and one
`verify`, and prohibits shell, exec, browser, Python, external PDF libraries, manual drawing, protected-form fallback,
and `send_file`. Success is decided from typed reports, journals, artifacts, outbox state, and a correlated passive
trace, never from conversational prose.

The live case used the official USCIS I-134 edition `01/20/25`: 511,302 bytes, 10 pages, 250 AcroForm fields, packet
array XFA, restricted encryption without an open password, and an admitted usage-rights signature without DocMDP or
FieldMDP. The official file is not committed and its SHA-256 was pinned to
`e887f9dbb4660ff9eec47d740c00358e45fcfb9c04a9776d8d95cda2e7a5e0d8`.

The synthetic request assigned exactly 11 unambiguous values: filing basis, supporter and beneficiary names,
supporter mailing address, country, and the same-physical-address choice. SSN, A-Number, signatures, attestations,
beneficiary addresses, unknown required fields, and every other field stayed blank. This was a technical test only;
the derivative is not legal advice and was not submitted.

## Deployed live evidence

The successful run on 2026-09-21 returned `MINTCLAW_PDF4H4_LIVE_QUALIFICATION_OK` and
`state=flattened_print_ready_live_verified`.

- Source SHA-256 remained `e887f9dbb4660ff9eec47d740c00358e45fcfb9c04a9776d8d95cda2e7a5e0d8`.
- Output SHA-256 is `c0716abb79cc7915d7f5c0f6c53f70853849dd511db2597a1bd3a0a3728257d0`;
  size is 324,267 bytes and page count is 10.
- Durable operation `document_write_2771f6e5c557472e88ec40bd6689132e` reached journal revision 8.
- Artifact `media://node-transfer-a25c0eea364d2d206cb7f207e5cc1ee8` was delivered exactly once by delivery
  `delivery_eecf93ae7f988739af87fec751c4d9d97b4d0a9241e2fb5d5c5c9960cae45705` and outbox
  `out_25bce2d30a6df6620cbb0efc102159a5`; delivery attempts were exactly 1.
- Correlated trace `trace-turn-d807931df97d9d18f3f275be` records exactly the required document actions and no
  prohibited tool. Its session hash is
  `31c024a42143b855cd08e583834e90ca11653f425724f5e957fbe120e41ec377`.
- Scans found zero source paths or private literals in the passive trace and persisted evidence.
- Private document-worker scratch was identical before and after the turn.

The output reopens as PDF 1.7 with 10 pages, no AcroForm, XFA, signature dictionary, usage-rights entry, or JavaScript.
It remains encrypted with printing allowed and document changes denied. Structural verification confirmed all 11
assignments before flattening and every forbidden field remained blank. Production and independent render hashes for
all 10 pages matched the deterministic CLI baseline. A separate human review of every rendered page found no
clipping, overlap, missing content, stale appearance, or corruption; the two filled pages were legible and pages 2,
3, and 5-10 retained their expected blank fields and signature lines.

## Fail-closed iterations

Four failed qualification attempts were retained only long enough to identify one concrete generic defect each:

1. Worker scratch contained an eagerly bootstrapped system skill bundle, so the harness stopped with
   `artifact_invalid`; #1323 skips that unrelated bootstrap for the exact internal document-worker process.
2. The first successful document transaction was rejected by the harness because deferred-tool discovery was not
   causally admitted; #1324 requires exactly one bounded BM25 discovery and correlates it to the document calls.
3. A fill succeeded but verify received only the operation ID, not the returned artifact reference, and correctly
   failed `source_not_authorized`; #1325 binds verify to both values in the schema, tool contract, and harness.
4. Fill and verify succeeded, but Poppler persisted the local input path in `pdfsig` evidence; #1326 records the same
   facts with the document path omitted.

No iteration weakened a refusal gate or introduced I-134-specific behavior. Failed outputs, evidence, private prompt
and value files, field maps, render copies, diagnostic traces, validation worktrees, and worker scratch were removed
after the successful record was captured. Those temporary deletions are irreversible. The canonical output and its
private final evidence bundle remain available to the operator.

## Deployment, health, and rollback

The final source checkout on `server@oc` was
`2ca32ba4e0ae0b03ba78ac7bff2234ad558c9b1e`. The running binary was built from
`d55a6f6945d26ebb2e40bbbac76f40ec80d1cf55`; #1326 changed only scripts and documentation, so no binary rebuild or
restart was required. All expected MintClaw services were active after qualification, product/global/system failed
units were zero, legacy processes were zero, and aggregate and per-service ten-minute error counts were zero.

Recovery bundles were captured before each deployed transition:

- `/home/server/mintclaw-pdf4h4-deploy-backup-20260921T131219Z`;
- `/home/server/mintclaw-pdf4h4-worker-fix-backup-20260921T141421Z`;
- `/home/server/mintclaw-pdf4h4-discovery-backup-20260921T155514Z`;
- `/home/server/mintclaw-pdf4h4-verify-backup-20260921T162914Z`; and
- `/home/server/mintclaw-pdf4h4-evidence-backup-20260921T170148Z`.

Rollback uses the exact pre-transition bundle selected for the desired boundary: stop only the affected user
services, restore its recorded binary bytes and source revision, restart the same services, then run the deployed
status and document smoke checks. Do not overwrite newer document journals, media, outbox, task, session, or trace
state. PDF4H introduced no configuration or mutable-data migration.

## Optional Telegram check

Use only a disposable supported form and synthetic values:

1. Attach the PDF and ask MintClaw to use only document tools to inspect and list fields.
2. Supply an explicit unambiguous subset, forbid identifiers and signatures, and request one filled PDF plus verify.
3. Confirm exactly one PDF arrives and the source attachment remains unchanged.
4. Download the derivative, open all pages, and confirm requested values are visible without clipping while forbidden
   fields and all signature lines stay blank.
5. Ask in the same conversation to verify the exact previous artifact and operation without refilling or redelivery.
   Confirm verification succeeds and no second PDF arrives.

The checked-in CLI and live harnesses remain the authoritative release gates; this checklist is only a channel-level
operator sanity check.

## Residual boundary

- Only `linux/amd64` is admitted. macOS, Linux ARM, Windows, and other tuples remain fail-closed until a platform
  parity goal repeats worker packaging, oracle, security, visual, privacy, recovery, and deployment evidence.
- Dynamic, foreground-authoritative, pure, script-dependent, externally bound, repeating, page-growing, or otherwise
  unknown XFA remains unsupported.
- Password-required, content-signed, certified, timestamped, DocMDP/FieldMDP, insufficient-permission, malformed,
  ambiguous, over-limit, unsafe-action, unsupported-field, and unverifiable inputs remain typed refusals.
- PDF4H does not collect real facts, determine what a legal form requires, validate legal sufficiency, sign, attest,
  submit, OCR, repair arbitrary PDFs, preserve editable hybrid-XFA semantics, or use browser/desktop automation.
