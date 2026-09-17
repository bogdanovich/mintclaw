# PDF0B Implementation Goal

## Status

Completed implementation contract for PDF0B. The implementation and exact deployment evidence are
recorded in the [PDF0B exit record](pdf0b-exit-record.md). This document remains the historical
source of truth for the completed milestone and does not admit PDF1A behavior.

The public operator interface is part of MintClaw:

```sh
mintclaw document inspect --input <pdf> --json
```

It is a thin adapter over the shared document service. The selected parser may be a packaged
library or executable behind the worker boundary, but it is not a separate public CLI contract and
does not become a permanent model-visible tool. Agent and channel use remain deferred to PDF1A.

## Objective

Close PDF0B on `linux/amd64` by delivering deterministic, fail-closed PDF inspection exclusively
inside the bounded worker introduced by PDF0A. Select exactly one production inspection backend and
one independent test oracle through executable evidence, deploy merged `main`, and record a milestone
exit decision.

An operator receives one stable `mintclaw.document_report.v1` tied to the exact immutable input
identity. The report either contains proven normalized facts or a typed terminal failure. Missing or
unproven facts are `unknown`; they are never silently reported as `false`.

## Required operator behavior

For a supported PDF, `document inspect` reports, where deterministically knowable:

- exact PDF0A source reference, size, SHA-256 digest, and operation identity;
- PDF syntax/version and page count;
- encryption and whether a password is required;
- signatures, certification, timestamps, Reader Extensions, and document or field restrictions;
- AcroForm presence and bounded field count;
- XFA presence and subtype when distinguishable;
- whether extractable text is present, absent, mixed, or unknown; and
- warnings, backend identity, normalized capability disposition, and terminal state.

Distinct typed outcomes are required for malformed input, unsupported feature, password-required,
resource limit, cancellation, worker timeout, worker crash, invalid worker response, unavailable
backend, unsupported platform, and internal failure. Encrypted, signed, certified, timestamped,
rights-enabled, restricted, and XFA PDFs are classification-only in PDF0B. No later operation is
authorized by detecting them.

## Architecture invariants

1. Reuse `DocumentRef`, `DocumentReport`, acquisition, protected scratch, capabilities, and the
   versioned one-shot worker. Do not add a daemon, durable document job system, second service, or
   general sandbox manager.
2. Untrusted PDF bytes are parsed only in the child worker. Core code may validate a typed worker
   response but may not import or invoke a PDF parser as a fallback.
3. The worker receives an already-acquired immutable descriptor plus a bounded request. It receives
   no original path, MintClaw configuration, credentials, ambient environment, or network authority.
4. Acquisition identity and authority survive unchanged through inspection. Reports, logs, traces,
   errors, and backend commands contain no local or protected path and no document bytes.
5. Capability advertising is derived from the qualified packaged backend for an exact runtime tuple.
   A compiled code path or installed developer dependency is not evidence of availability.
6. There is one production inspection backend. The independent oracle is test-only and cannot become
   an automatic production fallback.
7. Parsing limits are deterministic and enforced outside and inside the backend where possible:
   input bytes, pages, objects/depth where supported, output bytes, runtime, and descendant lifetime.
8. Cancellation terminates the entire worker/backend process group and removes operation scratch.
9. A backend disagreement is an explicit fixture or normalization decision, not an undocumented
   heuristic.

## Backend qualification and decision

Evaluate `pdfcpu`, ClawPDF/PDFium, Poppler, and only the minimum additional candidate needed to close
a demonstrated evidence gap. Each candidate receives executable probes for:

- the normalized facts in this goal;
- malformed, truncated, adversarial, encrypted, signed, AcroForm, and XFA inputs;
- deterministic exit/error mapping and bounded output;
- cancellation, timeout, crash containment, and descendant cleanup;
- CPU and memory behavior on the admitted fixture limits;
- pinned version, license, transitive/native runtime, fonts if relevant, SBOM inputs, and reproducible
  Linux packaging;
- install, update, and rollback without downloading dependencies during a document operation; and
- suitability as production backend, independent oracle, or rejected candidate.

Record the selected production backend and oracle with concrete reasons. Do not retain multiple
production strategies “just in case.” If no candidate passes, keep `inspect` unavailable and close
the milestone only with an honest failed qualification decision and executable evidence.

## Fixture and evidence contract

Extend the checked-in synthetic/licensed fixture manifest with at least:

- ordinary text, image-only, and mixed-page PDFs;
- multiple pages and same-name/different-bytes identity cases;
- AcroForm without XFA;
- XFA-only and AcroForm-plus-XFA samples, with subtype expectations only where proven;
- password-required/encrypted input without storing a password;
- unsigned, signed, certified/restricted, and rights-enabled samples where licensing permits;
- malformed header/xref/trailer/object structures, truncation, oversized declarations, and bounded
  adversarial nesting/object cases; and
- cancellation, timeout, backend crash, malformed backend response, concurrent inspection, and
  cleanup probes.

Every fixture entry records digest, provenance or generator, license, expected normalized facts,
allowed platform-specific differences, expected outcome, and test mapping. Do not use personal,
production, government-submission, tax, immigration, medical, or financial documents.

Required proof layers:

- unit tests for normalization, capability state, terminal errors, limits, and privacy;
- backend contract tests against the production backend;
- oracle comparisons and golden fixture reports with explicit tolerances or declared differences;
- real-process tests for worker/backend startup, crash, timeout, cancellation, process-group death,
  output limits, concurrency, and cleanup;
- copy-pasteable local CLI and automated harness tests;
- compile and packaging evidence for `linux/amd64`;
- merged-main deployed inspection smokes plus one live gateway smoke and completed redacted diagnostic
  trace; and
- rollback and zero-legacy/service-health evidence.

## Platform contract

PDF0B may advertise `inspect` only for a packaged and tested `linux/amd64` backend bundle. Linux ARM,
Windows, `darwin/arm64`, `darwin/amd64`, and every other tuple remain typed unavailable. The existing
macOS parity lane stays mandatory: each Darwin architecture must later run the identical normalized
fixture/contract suite on real hardware and prove worker containment, packaging, signing, update,
rollback, privacy, cancellation, and cleanup before advertising inspection.

## Pull request and deployment sequence

Use at most two dependent pull requests:

1. A focused implementation and backend-decision PR containing the production slice, fixtures,
   executable qualification evidence, tests, operational smoke, and documentation. It must pass
   formatting, targeted and broad affected tests, race tests, changed-package lint, docs lint,
   Linux compile/packaging coverage, required CI, automated review, and owner rocket approval.
2. After the first PR merges and only merged `main` is deployed, a strictly docs-only exit-record PR.
   It records merge/deploy revisions, backend decision, fixture and privacy evidence, commands and
   expected reports, service/trace results, rollback, residual limits, and the exact PDF1A admission
   boundary. It receives local docs validation and immediate docs-only merge.

Deployment uses the `mintclaw-deployed-ops` workflow on `server@oc`: clean preflight, backup, exact
fast-forward, build/install, minimal affected-service restart, service and bounded-journal health,
checked-in `document inspect` smokes, live-agent smoke, a new completed redacted trace, clean checkout,
and recorded rollback.

## Complete criteria

PDF0B is complete only when all of the following are true:

1. The production backend and independent oracle are selected by checked-in executable evidence;
   versions, licenses, packaging, SBOM inputs, failure semantics, and rejected alternatives are
   documented.
2. `mintclaw document inspect` uses the shared service and PDF0A acquisition path and emits a stable,
   path-free, content-free typed report retaining exact immutable identity.
3. Every required normalized fact has a tri-state or richer representation that preserves `unknown`;
   the required failure classes are distinct and fail closed.
4. Parsing is child-only. Real-process tests prove descriptor-only input, scrubbed environment,
   timeout, cancellation/process-group termination, output/runtime limits, crash handling, concurrent
   isolation, and deterministic scratch cleanup.
5. The manifest and synthetic/licensed fixtures cover the required normal, feature, malformed,
   adversarial, and lifecycle matrix with digest, provenance/license, expectations, and test mapping.
6. Production contract tests and applicable oracle comparisons agree after explicit normalization;
   privacy checks find no path, content, password, or ambient secret in report/log/trace evidence.
7. The capability registry advertises `inspect` only for the proven `linux/amd64` package; every other
   tuple remains unavailable, including both macOS architectures.
8. The implementation PR is merged after all local/CI/review/rocket gates.
9. The exact merge commit is deployed and passes acquisition regression, checked-in inspection
   smokes, gateway runtime smoke, new completed redacted trace, health, cleanup, and clean-worktree
   checks, with a usable backup and rollback.
10. The docs-only exit PR is merged and admits only a new, separately scoped PDF1A goal.

The goal ends immediately after criterion 10. It does not implement PDF1A extraction/rendering,
provider-native PDF, protected password handling, AcroForm writes, conversational forms, XFA
mutation, OCR, transformations, generation, macOS runtime, or companion placement.

## Stop conditions

- If no backend passes malformed-input, containment, cancellation, deterministic packaging,
  licensing, and deployed Linux gates, leave inspection unavailable; do not add a weaker fallback.
- If satisfying a backend requires a custom namespace/seccomp/container control plane, new daemon,
  or unrelated sandbox architecture, stop at an architecture checkpoint instead of expanding PDF0B.
- Apply every architecture-checkpoint threshold in `mintclaw-autonomous-pr`; repeated fixes may cause
  a bounded refactor, prerequisite PR, split, or replacement rather than unbounded patching.
