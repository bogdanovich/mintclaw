# PDF0A Exit Record

## Decision

PDF0A is complete for `linux/amd64`. The immutable-acquisition and mandatory-worker stop gate is
satisfied on that tuple. PDF object parsing remains unavailable, and no other OS or architecture is
advertised.

PDF0B inspection and backend selection is admitted as the next independently scoped milestone. This
record does not implement PDF0B, select a parser, or admit any parsing capability. A new PDF0B goal
must copy its operator outcome, scope, exclusions, evidence, and stop gate from the
[PDF support roadmap](pdf-support-roadmap.md#pdf0b-inspection-classification-and-backend-decision).

## Merged revisions

- Implementation: [PR #1102](https://github.com/bogdanovich/mintclaw/pull/1102), reviewed head
  `1760baaf5e725b5afcb7b19c8e149eb9ac7e6127`.
- Merge commit: `65feb5e68144abc5a670c41a45a720992a65699e`, merged on 2026-09-08 UTC.
- Final automated review: no high-confidence issues; all earlier authority and immutable-identity
  findings were resolved before merge.

The merged slice provides:

- typed document capabilities, references, reports, outcomes, and local acquisition CLI;
- regular-file, no-symlink acquisition into an operation-owned immutable snapshot;
- size and SHA-256 identity, mutation and replacement refusal, limits, and cleanup;
- a mandatory one-shot, descriptor-only document worker on `linux/amd64`;
- exact workspace, agent, actor, route, and effective-session authority for inbound `media://` refs;
- authority-checked open descriptors for document acquisition and node-file upload consumers; and
- synthetic fixtures, a manifest, automated tests, and a deployed acquisition smoke harness.

It does not provide PDF parsing, inspection, extraction, rendering, OCR, form support, an agent PDF
tool, provider-native PDF input, or macOS document execution.

## Automated evidence

The final implementation head passed:

- `make test-document`;
- full affected `pkg/agent`, `pkg/document`, `pkg/gateway`, `pkg/media`, and `pkg/tools` suites;
- race checks for acquisition, persisted media identity, session isolation, common inbound admission,
  and descriptor-based node transfer;
- repeated same-inode mutation and gateway transfer regressions;
- `scripts/pre-push-lint.sh --changed`, `make lint-docs`, and shell syntax validation;
- Linux real-process worker tests for timeout, crash, malformed output, bounded output, environment
  scrubbing, process-group cancellation, concurrency, and scratch cleanup;
- compile gates for Darwin ARM64 and Windows AMD64; and
- all final PR CI jobs: linter, tests, race, integration, security, frontend, portability, and platform
  compilation.

The fixture manifest is
[`pkg/document/testdata/acquisition-manifest.json`](../../pkg/document/testdata/acquisition-manifest.json).
It contains only synthetic inputs and maps the acquisition requirements to executable tests.

## Deployment evidence

Merged `main` was deployed to `server@oc` on 2026-09-08 UTC.

- Previous repository revision: `07f3aed9dc97a4922f07b5bb25949cbf5d904fcd`.
- Active repository and binary revision: `65feb5e68144abc5a670c41a45a720992a65699e`.
- Rollback bundle: `/home/server/mintclaw-pdf0a-backup-20260908T033059Z`.
- Build gates: `make build`, `make build-node`, and `make build-launcher` passed before restart.
- Configuration doctors returned status 2 for the existing policy findings in all five profiles; no
  configuration load or schema failure occurred.
- Only the main web and main, family, nutrition, reviewer, and spouse gateways were restarted.
- All ten expected MintClaw services were active afterward; product and global failed-unit counts,
  legacy process count, ten-minute per-service errors, and aggregate ten-minute errors were all zero.
- The deploy checkout was restored to a clean worktree after smoke testing.

The deployed acquisition command was:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

Its stable evidence was:

```text
host=server@oc
core_sha=65feb5e68144abc5a670c41a45a720992a65699e
fixture=pkg/document/testdata/acquisition-fixture.pdf
sha256=853a1115909cd9f5877e2f6410c593563c1de600505e455303820360afa7b78a
state=succeeded
scratch=clean
marker=MINTCLAW_DOCUMENT_ACQUIRE_OK
```

The live gateway pipeline was then checked through the running main gateway, rather than a separate
process with a different environment:

```sh
mintclaw agent live \
  --config /home/server/.mintclaw/main/config.json \
  --json \
  --session deploy-pdf0a-live-smoke \
  --message 'Reply with exactly MINTCLAW_DEPLOY_OK'
```

It returned `outcome=success` and `MINTCLAW_DEPLOY_OK`. The resulting diagnostic trace was
`trace-turn-4fa2b3eb627afd6232c05625`: schema `mintclaw.diagnostic_trace.v1`, state `completed`, eight
records, `redacted_content`, the `mintclaw.config_filter.v1` redactor, and no truncation. An earlier
direct stateless CLI probe was correctly rejected when it omitted the systemd-only
`MINTCLAW_AUTH_FILE`; it is not acceptance evidence and produced no accepted LLM response. `agent
live` is the reproducible gateway-level smoke interface.

## Rollback

If a PDF0A regression is found, stop only the six restarted units, restore the four backed-up runtime
artifacts, restart the same units, and rerun service status and the previous-version smoke:

```sh
backup=/home/server/mintclaw-pdf0a-backup-20260908T033059Z
systemctl --user stop \
  mintclaw-main-web.service mintclaw-main.service mintclaw-family.service \
  mintclaw-nutrition.service mintclaw-reviewer.service mintclaw-spouse.service
install -m 0755 "$backup/binaries/local/mintclaw" /home/server/src/mintclaw/build/mintclaw
install -m 0755 "$backup/binaries/local/mintclaw" /home/server/.local/bin/mintclaw
install -m 0755 "$backup/binaries/build/mintclaw-node" /home/server/src/mintclaw/build/mintclaw-node
install -m 0755 "$backup/binaries/local/mintclaw-node" /home/server/.local/bin/mintclaw-node
install -m 0755 "$backup/binaries/local/mintclaw-launcher" /home/server/.local/bin/mintclaw-launcher
systemctl --user start \
  mintclaw-main-web.service mintclaw-main.service mintclaw-family.service \
  mintclaw-nutrition.service mintclaw-reviewer.service mintclaw-spouse.service
```

The backup includes SHA-256 checksums, the previous core revision, unit definitions, drop-ins, and
unit states. Runtime rollback does not require rewriting repository history; retain the merged source
for diagnosis and forward repair.

## Residual limits and next boundary

- `acquire` is supported only on `linux/amd64`. Linux ARM, macOS, Windows, and other tuples remain
  fail-closed with `unsupported_platform`.
- The explicit [macOS parity lane](pdf-support-roadmap.md#macos-parity-lane) remains open for both
  `darwin/arm64` and `darwin/amd64`. Compile success is not runtime evidence; each tuple needs the same
  worker, fixture, packaging, privacy, cancellation, cleanup, CLI, and deployed proof before it can be
  advertised.
- PDF0A proves byte identity and containment, not PDF semantics. A successful acquisition must never
  be described as inspection, extraction, rendering, or form support.
- The one-shot worker is a crash, hang, environment, descriptor, and cleanup boundary. It does not
  claim a general network or arbitrary-filesystem sandbox.
- The next implementation boundary is PDF0B only: deterministic inspection and a backend decision
  inside the existing worker. If no candidate passes malformed-input, packaging, isolation,
  cancellation, licensing, and platform gates, inspection remains unavailable.
