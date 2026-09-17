# PDF1A Exit Record

## Decision

PDF1A is complete for `linux/amd64`. MintClaw now provides bounded local PDF text extraction and
page rendering through the shared document service, exposes the same operations to the agent through
one deferred `document` tool and one on-demand `pdf` skill, and preserves exact source and page
provenance through the Telegram adapter and durable media delivery path.

The operator-facing commands are:

```sh
mintclaw document extract --input <pdf> --pages <selection> --output <text-artifact> --json
mintclaw document render --input <pdf> --pages <selection> --output-dir <directory> --json
```

The milestone stops here. It does not admit provider-native PDF transport, password handling, OCR,
form writes, XFA mutation, companion placement, or macOS runtime support. Those capabilities require
their own roadmap admission and evidence.

## Merged revisions and review

- Core read/render implementation: [PR #1142](https://github.com/bogdanovich/mintclaw/pull/1142),
  final reviewed head `e7a8c21abc28e567fe6187883bbc9718d36a44d9`, merge commit
  `ca4d28c4b41037ff249a43c6d37fac79032f2466`, merged on 2026-09-09 UTC.
- Agent/channel implementation: [PR #1151](https://github.com/bogdanovich/mintclaw/pull/1151),
  final reviewed head `82b9bd793f37d5ecac911b5fc583e3de8c09db7f`, merge commit
  `4659fc8980c76c1647c5e992a00bb91196bc9cab`, merged on 2026-09-09 UTC.
- Both implementation PRs passed all ten required CI checks, were clean and mergeable, had no
  actionable unresolved thread, received an exact-head automated review, and had owner rocket
  approval before merge.

The second review cycle tightened three lifecycle invariants without expanding the admitted
architecture:

- current-turn extracted text now has one aggregate 32 KiB budget rather than a separate allowance
  for every tool call;
- render permission is recomputed for the model candidate that actually issues and consumes the
  tool call, including fallback and `BeforeLLM` rewrites;
- hidden tool cleanup is independent of visibility TTL, so expired hidden tools cannot retain
  protected scratch;
- retained render media is delivery-only and never leaks into provider context; and
- successful live-context consumption scrubs both the active message list and the context-retry
  snapshot, preventing text or image resurrection after overflow recovery.

## Delivered contract

The merged implementation provides:

- one `pkg/document` service for immutable acquisition, pdfcpu inspection, Poppler extraction and
  rendering, parent-side artifact validation, and deterministic cleanup;
- descriptor-only one-shot worker input, sealed admitted backend executables, scrubbed environment,
  process-group cancellation, bounded control output, and a 30-second runtime limit;
- stable path-free `mintclaw.document_report.v1` reports with exact source digest, selected pages,
  backend identity, counts, dimensions, artifact digests, limits, warnings, and typed terminal state;
- UTF-8 JSON Lines extraction artifacts and one verified PNG per rendered page;
- exact page-selection, character, page, dimension, pixel, byte, and model-context budgets;
- atomic no-replace CLI publication and authority-scoped `MediaStore` adoption for agent artifacts;
- one hidden first-party `document` tool with `inspect`, `extract`, and `render` actions;
- typed current-turn PDF activation of the on-demand `pdf` skill and deferred tool discovery, with
  unrelated turns paying neither the full skill nor tool-schema cost;
- authoritative attachment identity and byte-confirmed MIME routing, including same-name/different-
  byte isolation and fail-closed vision gating; and
- canonical retained-render delivery through the existing deliverable, outbox, and channel
  coordinator without a second sender or blind replay.

Provider-native PDF bytes are not sent. Providers receive only bounded extracted text or selected,
verified PNG pages through the existing text and image paths. Durable tool history and diagnostic
traces contain safe structure and opaque correlations, not extracted text, raw images, protected
paths, or raw filenames.

## Backend and oracle evidence

The sole production extract/render backend is Ubuntu `poppler-utils` `24.02.0-1ubuntu9.9` on
`linux/amd64`:

- `/usr/bin/pdftotext` performs per-page UTF-8 extraction; admitted SHA-256:
  `0fb98ea179e19154a90202608c164f2a319b79f16576fa6534b2d601033565e7`.
- `/usr/bin/pdftoppm` performs selected-page PNG rendering; admitted SHA-256:
  `207dcabcaeea0ce572aefc498d07d44d56a9ca06a85b3ae1fecd050476a34bf8`.
- `/usr/bin/pdfinfo` performs crop, rotation, and pixel preflight; admitted SHA-256:
  `3293dda06d80e1e38dab859aa47368c2876aedc41cbc2e24e8fb9a4e66392078`.

ClawPDF `0.3.2` with PDFium release `7902` is the independent test oracle and is not installed as a
production fallback. The executable oracle passed normalized text-marker comparisons and fixed
144-DPI pixel comparisons over the synthetic fixture set. The production choice, package and font
versions, notices, digests, performance measurements, update gate, and rollback coupling are frozen
in the [PDF1A backend decision](pdf1a-backend-decision.md).

## Automated evidence

The implementation heads passed the repository format, docs lint, changed-package lint, package,
race, real-process, integration, portability, security, frontend, and cross-platform compilation
gates. Focused PDF coverage includes:

- page normalization, budgets, reports, artifact adoption, authority, activation, routing,
  redaction, and typed failures;
- extraction and rendering fixtures for ordinary text, Unicode, rotation, crop boxes, scans, mixed
  pages, ambiguous reading order, AcroForm appearance, XFA refusal, malformed and encrypted input,
  excessive text, dimensions, pixels, page count, and output bytes;
- descriptor-only input, backend descendants, scrubbed environment, output limits, cancellation,
  timeout, crash, process-group death, concurrency, registration failure, and cleanup;
- independent oracle text and pixel comparisons;
- exact-ref agent-loop behavior, deferred schema and skill activation, heavy/vision routing, denied
  tool behavior, bounded current-turn context, and retry cleanup; and
- Telegram inbound identity, retained render delivery, confirmed/failing/ambiguous outcomes, and no
  blind replay.

The checked-in automated deployment harness was run against the exact merged checkout:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

It returned:

```text
core_sha=4659fc8980c76c1647c5e992a00bb91196bc9cab
fixture=pkg/document/testdata/text.pdf
sha256=bb1c32a5cbf83bc27a6828ef35ad96e598e26882c07d1cb16bc2eeb805b98797
state=succeeded
scratch=clean
agent_channel=passed
marker=MINTCLAW_PDF1A_AGENT_CHANNEL_OK
marker=MINTCLAW_PDF1A_DEPLOYED_OK
```

This harness uses only checked-in synthetic fixtures. It exercises CLI acquisition, inspection,
extraction, rendering, the real-process worker, and the Telegram agent/channel vertical test, and it
fails on protected-path disclosure or retained scratch. The copy-pasteable external Telegram
operator checklist remains in
[Document acquisition and inspection](../operations/document-acquisition.md#manual-telegram-checklist);
it is intentionally manual and uses no personal document as release evidence.

## Deployment evidence

The exact final merge was deployed to `server@oc` on 2026-09-09 UTC.

- Previous repository/runtime revision: `aa91bd77d908669d4584f72dd6b78f7f42037bb5`.
- Active repository/runtime revision: `4659fc8980c76c1647c5e992a00bb91196bc9cab`.
- Active version: `mintclaw v0.1.0-p8a.2-1687-g4659fc89`.
- Active build and installed core SHA-256:
  `ba132ac33cac498aea2f47ff57b06c5958f3740c1a1025d86d1a3261176c2b34`.
- Recovery bundle: `/home/server/mintclaw-pdf1a-deploy-backup-20260909T063925Z`.
- `make build`, `make build-node`, and `make build-launcher` completed before installation.
- Main, family, nutrition, reviewer, spouse, and main-web were the only restarted services. Every
  gateway process resolved to `/home/server/src/mintclaw/build/mintclaw-linux-amd64` after restart.
- Config doctor loaded all five profiles with zero configuration errors. Exit status 2 reflects
  pre-existing deployment policy findings, including intentionally configured node approval bypass,
  broad local operator tools, and plaintext credential storage; no config was changed for PDF1A.
- All ten expected MintClaw services were active. Product/global failed units, legacy processes,
  per-service ten-minute errors, and aggregate ten-minute errors were zero. The launcher returned
  HTTP 302 and the reviewer webhook returned its expected HTTP 404.
- The deployed source tree was clean. Document scratch was clean, and active units, configs,
  scripts, and skills had zero legacy-name hits.

The running main gateway was exercised through its authenticated live channel:

```sh
mintclaw agent live \
  --config /home/server/.mintclaw/main/config.json \
  --session pdf1a-deploy-live-smoke \
  --message 'Reply with exactly MINTCLAW_PDF1A_LIVE_OK.' \
  --timeout 2m \
  --json
```

It returned `outcome=success` and `MINTCLAW_PDF1A_LIVE_OK`. The resulting trace is
`trace-turn-41c16ea40422abb824698768`: schema `mintclaw.diagnostic_trace.v1`, root turn
`main-turn-2`, content mode `redacted_content`, outcome `completed`, eight records, and no
truncation.

A separate direct `agent --stateless` probe was attempted after deployment and failed before model
execution because that standalone CLI process could not refresh its OpenAI OAuth credential. It did
not exercise PDF code and is not the production gateway path. The authenticated `agent live` probe,
deployed PDF harness, and all service checks succeeded. Repairing standalone CLI credential leasing
is outside PDF1A and should remain a separate runtime-auth task.

## Operator use and manual verification

For a local PDF on the admitted host:

```sh
mintclaw document inspect --input example.pdf --json
mintclaw document extract --input example.pdf --pages 1-3 --output extracted.jsonl --json
mintclaw document render --input example.pdf --pages 2 --output-dir rendered-pages --json
```

For Telegram, attach the exact PDF in the current turn and ask MintClaw to inspect it before
extracting selected pages. Ask for page citations. Request rendering only for pages that require
visual interpretation, and say explicitly when a rendered page should be sent back. Use the
checked-in manual checklist to test text, render delivery, false `.pdf` MIME, and same-filename
identity without using sensitive material.

## Rollback

PDF1A introduced no configuration or persistent-state migration. To restore the previous binaries
while preserving merged source and runtime data for diagnosis:

```sh
backup=/home/server/mintclaw-pdf1a-deploy-backup-20260909T063925Z
systemctl --user stop \
  mintclaw-main.service mintclaw-family.service mintclaw-nutrition.service \
  mintclaw-reviewer.service mintclaw-spouse.service mintclaw-main-web.service
install -m 0755 "$backup/repo-build/mintclaw-linux-amd64" \
  /home/server/src/mintclaw/build/mintclaw-linux-amd64
install -m 0755 "$backup/local-bin/mintclaw" /home/server/.local/bin/mintclaw
install -m 0755 "$backup/local-bin/mintclaw-node" /home/server/.local/bin/mintclaw-node
install -m 0755 "$backup/local-bin/mintclaw-launcher" /home/server/.local/bin/mintclaw-launcher
systemctl --user start \
  mintclaw-main.service mintclaw-family.service mintclaw-nutrition.service \
  mintclaw-reviewer.service mintclaw-spouse.service mintclaw-main-web.service
```

The backup contains the previous core, node, and launcher binaries plus the complete user systemd
unit tree. Its files were independently hashed after creation. Mutable configuration and workspace
data must not be rolled back over newer state.

## Residual limits and next boundary

- Extraction and rendering are available only on `linux/amd64`. Both macOS architectures, Linux
  ARM, Windows, and every other tuple remain fail-closed. The macOS parity lane remains in the
  roadmap; compilation is not runtime evidence.
- Password-required PDFs remain unsupported. Signed, certified, timestamped, rights-enabled,
  restricted, AcroForm, and XFA facts remain inspectable, but PDF1A does not grant trust validation,
  decryption, fill, flatten, or XFA mutation authority.
- Image-only pages require an already configured vision-capable model path. PDF1A does not add OCR
  or silently treat empty extraction as a complete answer.
- Poppler package or executable changes intentionally disable extraction and rendering until a
  focused qualification repeats the fixture, oracle, packaging, deployment, and rollback gates.
- The one-shot worker is a bounded parser containment boundary, not a general network or arbitrary-
  filesystem sandbox.
- The external Telegram checklist is deliberately manual; the automated vertical test and deployed
  harness are the reproducible release gates.
- PDF1B provider-native PDF adapters, PDF1C later refinements, PDF2 form understanding, PDF3 form
  filling, PDF4 XFA feasibility, PDF5 packaging expansion, PDF6 advanced semantics, and macOS
  implementation all require separate admission.

PDF1A stops here. This record does not start the next roadmap milestone.
