# PDF2 AcroForm Implementation Goal

## Status

Completed on `linux/amd64`. Merged implementation, conformance, deployment, live-channel, privacy,
recovery, rollback, and residual-limit evidence is recorded in the
[PDF2 exit record](pdf2-exit-record.md). This goal remains the historical scope and acceptance contract;
it does not admit PDF3 or any other later milestone.

PDF0A immutable acquisition and worker isolation,
PDF0B structural inspection, and PDF1A local extract/render plus agent/channel handling are complete
prerequisites. This document is the source of truth for PDF2 scope, pull-request boundaries,
evidence, deployment, and completion.

PDF2 adds bounded writes for ordinary, unencrypted, unsigned AcroForms. It stops after an explicit
field map can produce one structurally and visually verified PDF through the shared service, CLI,
agent tool, real channel, and deployed runtime. It does not collect form facts conversationally,
decrypt inputs, mutate XFA, validate legal meaning, or add a form-specific workflow.

## Operator outcome

An operator can inspect an admitted AcroForm, obtain stable field identities and allowed values,
submit an explicit typed field map, and receive either:

1. one new PDF whose requested values independently round-trip and visibly render;
2. one verified flattened PDF when flattening was explicitly requested and is supported for every
   affected field; or
3. one typed denied, unsupported, invalid, canceled, failed, uncertain, or
   `delivery_ambiguous` result that does not claim a usable artifact exists.

The source is never changed in place. A result is not final-ready, registered, or delivered until
all structural and visual assertions pass.

The operator-facing commands remain in the existing MintClaw binary:

```sh
mintclaw document fields --input <pdf> --json
mintclaw document fill --input <pdf> --fields <json-file> --output <pdf> --json
mintclaw document verify --input <pdf> --expect <json-file> --json
mintclaw document flatten --input <pdf> --output <pdf> --json
```

`fill` performs mandatory verification. `verify` is also exposed for reproducible diagnosis; it
does not bless an artifact whose operation journal or source lineage is missing. `flatten` may be
withheld if the selected backend cannot preserve visible content under the independent oracle.

## Fixed architecture decisions

### One service, worker, tool, and artifact path

- `pkg/document` remains the sole owner of acquisition, field semantics, operation state,
  transformation, verification, and document reports.
- CLI and agent adapters call the same service. The PDF skill selects the workflow but never shells
  together parsers, writers, renderers, or delivery commands.
- Untrusted source and generated PDF bytes are parsed only in the existing one-shot document worker.
  PDF2 adds no daemon, broker, container manager, MCP server, or separately installed helper.
- The existing deferred `document` tool gains bounded `fields`, `fill`, `verify`, and only if
  qualified, `flatten` actions. It remains the only model-facing document tool.
- The existing `MediaStore`, canonical task deliverable, outbox, and channel manager retain their
  current ownership. PDF2 adds no second artifact registry or sender.

### Source and field identity

Every operation uses the immutable `DocumentRef` snapshot and source SHA-256 established by PDF0A.
An agent call may start from an authority-bound attachment or from a current-turn exact local path
admitted by the PDF1A policy. After inspection it uses only the returned opaque source ref.

The versioned field report exposes the immutable source SHA-256, bounded presentation metadata, and
an opaque `field_id` derived from the immutable source identity and normalized AcroForm object
identity. It includes the fully qualified field name, supported field kind, flags, allowed
export/display choices, required/read-only state, default-state presence, and ordered widget
descriptors with one-based pages. It never exposes an object path, host path, parser pointer,
JavaScript, or document content.

Fill maps are keyed by `field_id`, not a model-guessed display label. One logical field may own
multiple widgets; one value must update and verify every widget. Distinct logical fields that cannot
be given unambiguous stable identities fail discovery or fill. Unknown fields, duplicate assignments,
wrong value kinds, invalid choices, unsupported flags, and stale source identities fail before a
write.

The initial typed values are strings for text, multiline, and admitted date-text fields; booleans for
checkboxes; one advertised export value for radio and combo fields; and one or more advertised export
values for list fields when their flags permit it. MintClaw does not execute PDF JavaScript or infer
business meaning from names. A date field is admitted only when the fixture/backend contract can
represent its static format without executing actions; otherwise it remains ordinary text or is
reported unsupported.

### Backend admission

`pdfcpu` `v0.15.0` is the preferred Go-native writer because it is already pinned and isolated as
the PDF0B structural backend. Its fields, fill, appearance, and flatten behavior must be qualified by
executable fixtures before each corresponding capability is advertised. Qualification records exact
API behavior, binary/module identity, license and SBOM impact, startup and memory, malformed input,
cancellation, output bounds, packaging, update, and rollback.

Poppler remains the production read/render backend and independent visible-output verifier. A second
independent reader used only by the test/oracle harness must confirm normalized values and widget
states. No production fallback may use a browser, Python, runtime-downloaded package, provider-native
PDF mutation, or an agent-authored shell command. If pdfcpu cannot satisfy a field kind or appearance
invariant, that capability remains unavailable; the contract is not weakened to enable it.

### Transaction and durable operation journal

The first write-capable service call creates a durable, owner-scoped operation record before backend
execution. The record binds operation ID, owner scope, source digest, normalized request digest,
backend generation, output generation, state, artifact digest when known, verification assertions,
and logical delivery identity. It stores no raw field value, document content, local path, or output
bytes.

The state machine has one forward owner and explicit terminal outcomes:

```text
accepted -> writing -> written -> verifying -> verified -> registered -> delivery_pending
         \-> canceled | failed | uncertain
delivery_pending -> delivered | delivery_failed | delivery_ambiguous
```

Transitions are compare-and-swap or transactionally equivalent. The same operation and normalized
request return the stored terminal result or resume the provably incomplete transition. A conflicting
request is rejected. A lost result never starts another write generation or delivery identity.
Partial output stays in private operation storage and is removed or quarantined; it is never adopted
under the final filename.

### Independent verification and publication

Verification is mandatory after every fill and flatten operation:

1. the worker reopens the candidate, validates it, confirms source lineage and requested field values,
   and checks that unaffected fields retain their normalized state;
2. an independent reader in the test/conformance path confirms the expected logical values;
3. Poppler renders every affected page with the pinned PDF1A settings;
4. deterministic fixture assertions prove expected text/marks are visible and not clipped, while
   golden pixel comparisons use documented tolerances rather than compressed-byte equality; and
5. the parent validates the output descriptor, PDF signature, size, digest, page ownership, bounds,
   and absence of undeclared artifacts before atomic adoption.

Production verification uses structurally independent readback plus Poppler rendering and bounded
visible assertions that do not require sending raw pages to a model. Test goldens provide stronger
fixture-specific comparison. Model visual judgment may be supplementary but can never override a
failed deterministic assertion.

### Refusal boundary

PDF2 refuses modification of encrypted, password-required, signed, certified, timestamped,
rights-enabled or restricted, hybrid-XFA, pure-XFA, malformed, or over-limit documents. It also
refuses unsupported field actions, calculated/signature/button fields, embedded-action-dependent
behavior, invalid or ambiguous choices, missing required fonts, clipped multiline output, stale
appearances, partial repeated-widget updates, and any case that cannot be independently verified.

Detection is not trust validation. A structurally unsigned result does not imply that a document is
legally safe to modify, and MintClaw does not provide legal, tax, immigration, financial, or medical
advice through this milestone.

### Privacy and model boundary

- Write actions are sensitive by default. Raw field maps, values, rendered pages, document bytes,
  and local paths are omitted from ordinary diagnostic previews, generic task field deltas, logs,
  review artifacts, and durable model-tool history.
- Values explicitly supplied in the current request may be projected only to the active model turn
  and protected tool call needed for that operation. PDF2 does not persist them for later collection.
- Reports expose only field identities and bounded metadata, source/output digests, state, assertion
  counts, safe warnings, and opaque artifact refs.
- The PDF skill must tell the model to inspect fields first, show the intended mapping, use exact
  identifiers, request confirmation when the active request is ambiguous, and propagate typed
  failure. Multi-turn protected fact collection belongs to PDF3.

## Failure vocabulary

In addition to PDF0A-PDF1A failures, PDF2 distinguishes at least:

- `form_not_present`, `form_unsupported`, and `field_unsupported`;
- `field_not_found`, `field_ambiguous`, `field_read_only`, and `field_value_invalid`;
- `choice_invalid`, `appearance_unavailable`, `appearance_stale`, and `content_clipped`;
- `write_conflict`, `write_failed`, `journal_failed`, and `recovery_uncertain`;
- `verification_structural_failed` and `verification_visual_failed`;
- `artifact_registration_failed`, `delivery_failed`, and `delivery_ambiguous`; and
- the existing encrypted, signed, restricted, XFA, malformed, limit, timeout, crash, cancellation,
  input-mismatch, and unsupported-platform outcomes.

Messages remain bounded and path/content-safe. Backend error text is never forwarded verbatim.

## Fixture and conformance program

All fixtures are deterministic, synthetic, redistributable, and listed in a versioned manifest with
SHA-256, generator/provenance, license, field schema, requested values, normalized expected output,
affected pages, render tolerance, expected terminal state, and evidence-test mapping. No real user,
government-submission, tax, immigration, medical, or financial file enters the repository.

The positive matrix covers text, multiline wrapping, checkbox on/off, radio export values, single and
multi-select lists, editable/non-editable combo boxes, admitted date text, Unicode, required fields,
default values, and repeated widgets across pages. Negative fixtures cover duplicate names,
inheritance, invalid choices, unsupported actions, calculated/signature/button fields, missing fonts,
clipping, stale/missing appearances, malformed fields, XFA hybrids, signatures/restrictions,
encryption, cancellation, timeout, crash, output corruption, registration failure, restart at every
journal transition, confirmed delivery, definite failure, and ambiguous acceptance.

The canonical test series is one checked-in command or script that runs:

1. unit tests for schemas, normalization, field identity, values, reports, journals, recovery,
   redaction, limits, and terminal states;
2. production backend contracts and the independent reader/render oracle over the fixture manifest;
3. structural and visual goldens with intentional tolerance and clipped-output checks;
4. CLI fields/fill/verify/flatten smokes using only documented commands;
5. agent-loop tests for attachment and authorized-local-path flows, deferred schema/skill activation,
   exact mapping, failure propagation, and no unrelated-turn context cost;
6. real-channel tests for one verified PDF delivery, definite delivery failure, ambiguity, and no
   blind replay;
7. real-process tests for descriptor-only I/O, scrubbed environment, limits, cancellation, descendant
   death, corrupt output, concurrency, and cleanup;
8. journal restart/recovery tests at every transition and races between retry, cancel, and delivery;
9. privacy assertions over history, reports, traces, logs, task state, and artifacts; and
10. a deployed smoke plus a short copy-pasteable manual checklist.

The same source fixture, field-map digest, expected values, output digest, and logical delivery identity
must correlate across service, CLI, worker, agent, channel, deployed, recovery, and rollback evidence.
Unit tests alone cannot close PDF2.

## Pull-request sequence

Use one dedicated autonomous worktree and start each non-stacked PR from the latest merged
`origin/main`. Do not begin a dependent PR until its predecessor is merged.

1. **Admission:** this goal, roadmap status, frozen report/request schemas, fixture inventory, backend
   experiment plan, and PR dependencies. Documentation only.
2. **Fields:** field schema and identity, `fields` service/worker/CLI operation, positive and refusal
   fixtures, backend qualification evidence, and independent field oracle.
3. **Write core:** durable journal and recovery state machine, transactional fill, typed values,
   appearances, output adoption, structural verification, and crash/cancellation/privacy tests.
4. **Visual and flatten:** Poppler-visible verification, render goldens, clipping/font/stale-appearance
   failures, and flatten only for the proven subset.
5. **Agent and delivery:** sensitive deferred-tool actions, PDF skill workflow, artifact registration,
   task deliverable/outbox identity, agent/channel tests, and automated smoke command.
6. **Deployment and exit:** merge all code, deploy the exact merge, execute automated and live-channel
   proofs, inspect passive traces and rendered output, record rollback and residual limits, and merge
   the exit record.

A prerequisite exposed by executable evidence may become a smaller PR. It must preserve this outcome
and be linked from later PRs. Review-driven growth that crosses the PDF2 boundary triggers the
autonomous workflow architecture checkpoint rather than being absorbed silently.

## Local and deployed acceptance

Before each code PR, run `make fmt`, focused package and command tests, the relevant fixture/oracle
harness, race tests where concurrency changed, and changed-package lint. Shared persistence, agent,
task, media, or outbox changes require their broader package suites. Every code PR must pass GitHub CI,
automated review, all actionable threads, and authorized merge policy.

The final merged revision is deployed to `server@oc` with timestamped binary, unit, config, and
mutable-state rollback material before restart. Completion requires all configured profiles to load,
all expected services active, no unexplained error-level journals, exact source/runtime SHA agreement,
and no legacy runtime identity.

The deployed harness must use a checked-in synthetic AcroForm and print stable markers for field
discovery, fill, structural verification, visual verification, unchanged source digest, journal
recovery, single delivery identity, cleanup, and overall success. The manual channel test must let an
operator attach or name the synthetic form, supply the documented field map, receive one PDF, download
it, independently inspect values, and render affected pages. The corresponding completed diagnostic
trace must show only safe operation metadata and opaque correlations.

## Completion criteria

PDF2 is complete only when:

- all six PR stages are merged and the roadmap links a final exit record;
- every advertised field kind passes the fixture, independent readback, and visual matrix;
- original bytes remain unchanged and no failed/canceled/unverified output becomes final-ready;
- restart at every journal transition recovers the same operation/artifact generation;
- one logical delivery identity survives retry and ambiguous acceptance is never blindly replayed;
- CLI, service, worker, agent, real channel, deployed runtime, privacy, cleanup, and rollback proofs
  all correlate to the same checked-in fixture contract;
- raw values, paths, content, and page images are absent from ordinary durable history, task deltas,
  logs, traces, and review evidence;
- exact merged source is deployed, services and configs are healthy, and the manual test is
  reproducible; and
- the exit record lists backend versions, merge commits, deployment SHA, fixture evidence, known
  limits, rollback instructions, and the explicit stop before PDF3/PDF4/PDF5/PDF6 or macOS work.

## Explicit exclusions

PDF1B provider-native transport, PDF1C password/decryption, PDF3 protected multi-turn fact collection,
PDF4 XFA mutation, PDF5 OCR/tables/redaction/general transforms/generation, PDF6 companion placement,
and macOS implementation are not admitted. The existing macOS parity backlog remains mandatory but
separate. Arbitrary filesystem access, browser-authoritative form filling, Acrobat automation,
modification of signed/encrypted/restricted inputs, runtime dependency installation, and form-specific
USCIS/I-134 logic are also excluded.
