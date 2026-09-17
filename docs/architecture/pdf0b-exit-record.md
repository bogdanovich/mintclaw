# PDF0B Exit Record

## Decision

PDF0B is complete for `linux/amd64`. MintClaw now provides deterministic structural PDF inspection
through the shared document service and the operator-facing command:

```sh
mintclaw document inspect --input <pdf> --json
```

Parsing remains confined to the PDF0A one-shot child worker. `pdfcpu` `v0.15.0` is the sole
production backend, and Poppler `24.02.0` is the independent test-only oracle. Every other runtime
tuple remains fail-closed.

PDF1A is only the next sequenced roadmap milestone. This record does not admit extraction,
rendering, an agent tool, a PDF skill, channel delivery, OCR, provider-native input, password
handling, form writes, or XFA mutation. Any PDF1A implementation requires a new bounded goal.

## Merged revision and review

- Implementation: [PR #1117](https://github.com/bogdanovich/mintclaw/pull/1117), final reviewed head
  `92160587abb517199a67e809b5319ff1a08023a4`.
- Merge commit: `7e6b20c2f3338817f72e4db90b5b5f5bfc9b4ac6`, merged on 2026-09-08 UTC.
- Final gates: all ten required CI checks passed, the PR was clean and mergeable, every review thread
  was resolved, and the owner reviewer supplied the required rocket approval.
- One unrelated `pkg/agent` five-second timing test failed in the first Tests job; rerunning that
  failed job on the unchanged head passed. The independent Race job and all PDF suites were green.

Review tightened the same bounded backend adapter rather than expanding the architecture. The final
head proves:

- cumulative page-content limits before stream assembly, including the exact exhausted-budget case;
- conservative content tokenization that ignores PDF names and fails closed on inline-image bytes;
- catalog- and signed-field-reachable form, XFA, and restriction facts, excluding orphan xref data;
- bounded catalog-metadata decoding before pdfcpu validation instead of its 512 MiB default; and
- restoration of pdfcpu xref validation state after bounded metadata predecode, so an invalid
  metadata dictionary is still rejected.

At the fourth substantive review/fix checkpoint, all findings were still confined to
`pkg/document` and its synthetic fixtures. No new service, daemon, control plane, parser, public
tool, or later-milestone behavior was needed, so replacing or splitting the architecture was not
warranted.

## Delivered contract

The merged slice provides:

- stable, parent-validated `mintclaw.document_report.v1` inspection results;
- exact immutable source identity, size, SHA-256 digest, operation ID, and path-free reports;
- tri-state or richer facts for version, pages, encryption/password requirement, signatures,
  timestamps, certification/restrictions, AcroForm, XFA, and extractable-text signals;
- distinct typed failures for malformed input, password-required input, inspection limits,
  unsupported platforms, cancellation, worker timeout/crash/protocol errors, and unavailable
  backends;
- descriptor-only child input, scrubbed environment, bounded output/runtime/content/object/depth,
  process-group termination, concurrent isolation, and deterministic scratch cleanup; and
- capability advertising only when the packaged runtime is `linux/amd64`.

The fixture source of truth is
[`pkg/document/testdata/inspection-manifest.json`](../../pkg/document/testdata/inspection-manifest.json).
It contains 25 synthetic or repository-licensed cases with SHA-256 digests, construction and license
metadata, expected facts or terminal failures, and test mappings. No personal, production,
government, tax, immigration, medical, or financial document is present.

## Automated evidence

The final implementation head passed:

- `make fmt`, `make fmt-check`, `make lint-docs`, and `scripts/pre-push-lint.sh --changed` with zero
  issues;
- document service and CLI contract tests on Darwin plus Linux/amd64 cross-compilation;
- focused Linux backend regressions and the full `go test -race ./pkg/document` suite;
- real-process worker tests for descriptor input, environment scrubbing, malformed output, output
  limits, crash, timeout, cancellation, descendant death, concurrency, and cleanup;
- `scripts/document-worker-smoke.sh` with `MINTCLAW_PDF0B_WORKER_OK`;
- the Poppler `24.02.0` oracle over all 25 fixtures with `MINTCLAW_PDF0B_ORACLE_OK`; and
- all final PR CI checks for tests, race, lint, security, integration, frontend, portability, and
  Darwin/Windows compilation.

On the exact merged deployed checkout, this aggregate command passed:

```sh
cd /home/server/src/mintclaw
GOFLAGS=-tags=goolm,stdjson make test-document
PDF0B_POPPLER_VERSION=24.02.0 make test-document-oracle
```

The explicit `GOFLAGS` preserves the repository's normal `goolm,stdjson` tags in the nested worker
smoke while the Makefile exports `CGO_ENABLED=0`. The bare target passes its package tests but does
not currently export its Make-local tags into the nested script on that Linux host. Direct
`scripts/document-worker-smoke.sh`, the command above, PR CI, and the deployed binary all pass. This
is a developer-harness composition limitation, not a runtime fallback; a future focused tooling
change may make the bare target equivalent without changing document semantics.

## Deployment evidence

The exact implementation merge was deployed to `server@oc` on 2026-09-08 UTC.

- Immediate previous repository and runtime revision:
  `fa4f4100efb555b6b372f8c37612b882cf95695c`.
- Active repository and binary revision: `7e6b20c2f3338817f72e4db90b5b5f5bfc9b4ac6`.
- Active version: `mintclaw v0.1.0-p8a.2-1590-g7e6b20c2`.
- Active build and installed CLI SHA-256:
  `44d5400fb2bc0c2ace0106593b4de923e7edff923a08f385ae661217a27f8b70`.
- Recovery bundle: `/home/server/mintclaw-pdf0b-backup-20260908T092935Z`; its checksum manifest
  verified successfully after deployment.
- `make build`, `make build-node`, and `make build-launcher` passed before installation.
- Old and new configuration-doctor summaries were identical for main, family, nutrition, reviewer,
  and spouse. Existing plaintext-credential policy findings remained redacted and unchanged; there
  was no configuration load or schema regression.
- Only the main, family, nutrition, reviewer, and spouse gateways were restarted. Their running
  `/proc/<pid>/exe` hashes all matched the active binary hash above.
- All ten expected MintClaw services were active. Product/global failed units, legacy processes,
  per-service ten-minute errors, and aggregate ten-minute errors were all zero.
- The repository was clean at the exact deployed revision. Profile document scratch and all PDF0B
  runtime temp directories were empty. Five older cross-compile test binaries from 2026-09-06 were
  identified as non-runtime files and deliberately preserved.

The deployed document harness was:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

It returned:

```text
host=server@oc
core_sha=7e6b20c2f3338817f72e4db90b5b5f5bfc9b4ac6
fixture=pkg/document/testdata/text.pdf
sha256=bb1c32a5cbf83bc27a6828ef35ad96e598e26882c07d1cb16bc2eeb805b98797
state=succeeded
scratch=clean
marker=MINTCLAW_DOCUMENT_INSPECT_OK
```

Additional deployed CLI probes returned the documented terminal classes without retaining a path
or scratch artifact:

```text
encrypted-password-required exit=3 state=unsupported code=password_required
truncated exit=4 state=failed code=malformed_pdf
malformed-metadata exit=4 state=failed code=malformed_pdf
metadata-decoded-limit exit=5 state=failed code=inspection_limit
scratch=clean
marker=MINTCLAW_PDF0B_FAILURES_OK
```

The running main gateway was then exercised with:

```sh
mintclaw agent live \
  --config /home/server/.mintclaw/main/config.json \
  --json \
  --session deploy-pdf0b-live-smoke-7e6b20c2 \
  --message 'Reply with exactly MINTCLAW_DEPLOY_OK' \
  --timeout 2m
```

It returned `outcome=success` and `MINTCLAW_DEPLOY_OK`. The resulting trace was
`trace-turn-0594c20385c57840c7dce1ff`: schema `mintclaw.diagnostic_trace.v1`, root turn
`main-turn-2`, matching session hash, state `completed`, eight records, `redacted_content`, the
`mintclaw.config_filter.v1` redactor, and no truncation.

## Operator use

The deployed synthetic success fixture can be inspected manually with a real path:

```sh
/home/server/src/mintclaw/build/mintclaw document inspect \
  --input /home/server/src/mintclaw/pkg/document/testdata/text.pdf \
  --json
```

For an operator-owned PDF, replace only the `--input` value. A successful report proves structural
facts and immutable identity; it does not extract document text or authorize a later mutation.
Expected non-zero exits are documented in
[`Document acquisition and inspection`](../operations/document-acquisition.md).

## Rollback

PDF0B introduced no configuration or persistent-state migration. To restore the preceding runtime
binary while retaining merged source for diagnosis:

```sh
backup=/home/server/mintclaw-pdf0b-backup-20260908T092935Z
systemctl --user stop \
  mintclaw-main.service mintclaw-family.service mintclaw-nutrition.service \
  mintclaw-reviewer.service mintclaw-spouse.service
install -m 0755 "$backup/build/mintclaw" /home/server/src/mintclaw/build/mintclaw
install -m 0755 "$backup/installed/mintclaw" /home/server/.local/bin/mintclaw
systemctl --user start \
  mintclaw-main.service mintclaw-family.service mintclaw-nutrition.service \
  mintclaw-reviewer.service mintclaw-spouse.service
```

The bundle also contains the pre-deploy node and launcher binaries, complete user unit directory,
unit states, source revisions, version, SHA-256 manifest, and rollback instructions. PDF0B did not
install or restart node, launcher, queue, webhook, notification, or metrics components.

## Residual limits and next boundary

- `inspect` and `acquire` are supported only on `linux/amd64`. Linux ARM, Windows, `darwin/amd64`,
  `darwin/arm64`, and all other tuples remain typed unavailable until their explicit parity gates
  pass.
- The [macOS parity lane](pdf-support-roadmap.md#macos-parity-lane) remains open for both Apple
  silicon and Intel. Compile success is not runtime evidence.
- Inspection is structural classification. The extractable-text fact is a conservative content
  operator signal; inline-image ambiguity remains `unknown`. No text or page image is returned.
- Password-protected, signed, certified, timestamped, rights-enabled, restricted, AcroForm, and XFA
  inputs remain classification-only. Detection grants no decryption, trust, render, fill, flatten,
  or delivery authority.
- The pinned pdfcpu version is part of the security boundary. Any upgrade must rerun the full
  metadata, content-limit, malformed, lifecycle, oracle, packaging, and deployment matrix.
- The one-shot worker contains crashes, hangs, environment, descriptors, process descendants, and
  scratch lifetime. It is not a general network or arbitrary-filesystem sandbox.
- PDF1A is the next possible milestone, but it is not active. A new goal must select and bound local
  extraction/rendering, artifacts, a compact deferred agent tool and skill, authoritative routing,
  one real channel, privacy, recovery, delivery, and macOS follow-up without weakening PDF0B.
