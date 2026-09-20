# PDF4B Static XFA Implementation Goal

## Status

Admitted as a bounded `linux/amd64` implementation goal, conditional on Stage 0 below. PDF4A selected a
`supported-subset-candidate`; it did not enable production XFA writes. Until every completion criterion in this goal
passes and an exit record is merged, MintClaw must continue to refuse all XFA mutation and rendering requests.

The source decision and feasibility evidence are in the
[PDF4A XFA feasibility decision](pdf4a-xfa-decision.md). That decision is normative when this goal is ambiguous.

## Operator Outcome

For an admitted pure static-XFA PDF, an operator can list deterministic fillable fields, submit a bounded typed field
map, and receive either:

1. one new editable XFA PDF whose requested values are structurally present and independently proven visible;
2. a typed refusal that identifies the unsupported or unsafe XFA feature; or
3. a definite or uncertain failure that never claims or blindly replays a write whose outcome is unknown.

The source remains byte-identical. MintClaw never reports success from dataset mutation alone.

## Exact Supported Subset

An input is eligible only when all of these predicates are positively established by production inspection:

- the catalog-reachable `/AcroForm/XFA` is a well-formed name/stream packet array;
- `config`, `template`, and `datasets` packets are present once, bounded, and well formed;
- `/NeedsRendering` is true and `/Fields` is absent or empty, so there is no hybrid authority conflict;
- configuration and template prove static rendering, fixed positions, and fixed page count;
- each exposed field has one deterministic template binding and one bounded scalar dataset value;
- the document contains no repeat, occurrence growth, overflow, dynamic layout, foreground/hybrid authority, script,
  event, action, submit, external data connection, remote reference, or unknown executable construct; and
- the PDF is unsigned, uncertified, unrestricted, rights-free, unencrypted, and not password protected.

Unknown, ambiguous, duplicate, malformed, over-limit, or unsupported facts fail closed. PDF4B does not infer a safe
subset from a successful render.

## Fixed Architecture

PDF4B extends the existing document stack; it does not create a parallel PDF product:

| Existing owner | Added responsibility | Must not own |
| --- | --- | --- |
| document inspection/backend | normalized XFA feature facts and the exact supported-subset decision | mutation, approval, or delivery |
| document worker | one-shot packet update, structural readback, isolated render, and visible verification | durable jobs or a network API |
| document service/operation journal | source identity, idempotency, cancellation, uncertain outcomes, and artifact commit | XFA parsing heuristics |
| PDF3 form workflow | protected answer collection, mapping/review, and approval-bound handoff | raw PDF mutation or renderer lifecycle |
| existing CLI and deferred document tool | expose the same list/fill contracts and typed reports | a second XFA-specific tool surface |
| artifact/outbox path | private artifact ownership and exactly-once logical delivery intent | execution or semantic verification |

The packet updater is the exact pinned `pdfer` revision qualified by PDF4A, wrapped by MintClaw-owned feature policy,
XML-safe typed serialization, bounds, and readback. No caller passes arbitrary XML to the backend.

The independent visible verifier is a pinned PDF.js build packaged as a one-shot child of the existing document
worker. It is renderer-only. It never accepts edit actions, browser automation commands, a persistent profile, or
network navigation. Production packages all runtime assets ahead of time; no `npm`, browser, or Python installation
occurs during an operation.

## Stage 0: Renderer Packaging Hard Gate

This stage precedes every production mutation change. In a focused PR, prove that the proposed Linux package can:

- start from the existing document worker with no daemon or second service;
- render the qualified fixture from the actual packaged assets on a clean supported host;
- disable the scripting manager and evaluator, reject script/action-bearing inputs, and abort all non-private-origin
  requests, redirects, `file:` access, downloads, dialogs, workers, or child launches outside the admitted set;
- run with a private scratch directory, a minimal environment without credentials, bounded process descendants,
  memory, CPU time, wall time, pages, pixels, output bytes, and concurrency;
- terminate and clean up deterministically on success, error, timeout, cancellation, worker crash, and service restart;
- emit a machine-readable render/readback result tied to the exact input SHA-256 and renderer version; and
- carry its license, notices, version pins, SBOM data, build/release packaging, health check, and rollback behavior.

The gate fails if it needs browser-authoritative filling, desktop automation, an always-on browser, ambient network or
filesystem authority, privileged execution, a second document service, runtime dependency installation, or an
unbounded permanent dependency. Failure ends PDF4B as `detection-and-refusal-only`; no classifier or mutator work
proceeds under this goal.

## Field And Write Contract

The list operation returns only fields whose binding is deterministic. Each field identity derives from normalized
template and dataset paths plus an occurrence index; labels, types, required state, current-value presence, bounds,
and provenance are explicit. Friendly labels never serve as write identity. Duplicate or conflicting bindings refuse.

The fill request uses the source artifact identity, inspection revision, exact field IDs, typed scalar values, and an
idempotency key. It cannot carry arbitrary packet names, XPath, XML, scripts, or output paths. Text serialization must
round-trip XML metacharacters, Unicode, combining characters, supplementary-plane characters, empty values, and
maximum admitted lengths without entity expansion or encoding drift.

One write follows this order:

1. revalidate source identity, inspection revision, approval, and idempotency state;
2. classify every XFA feature again inside the worker and refuse any fact outside the supported subset;
3. update only the selected scalar dataset nodes into a new private scratch artifact;
4. reopen the emitted bytes and prove structural validity, packet preservation, exact typed readback, fixed page
   count, source immutability, and absence of newly introduced executable or external references;
5. render every affected page from the emitted PDF with the independent packaged renderer and prove the expected
   values are visible, not clipped, hidden, blank, stale, or only present in raw XML;
6. commit the verified artifact atomically through the existing document artifact transaction; and
7. hand any channel delivery to the existing outbox exactly once.

Any failure before artifact commit publishes no successful output. An uncertain worker or delivery result follows the
existing no-blind-replay rules.

## PR Sequence

Each PR is a complete, reviewable boundary from latest `origin/main`; later PRs do not start before their dependency
merges unless explicitly stacked and labeled.

1. **PDF4B0 renderer package:** Stage 0 implementation, hermetic/malicious probes, release assets, and clean-host
   qualification. Stop permanently on a failed gate.
2. **PDF4B1 inspection:** production packet parser/classifier, limits, normalized field identities, typed refusal
   reasons, CLI list surface, and conformance fixtures. No mutation.
3. **PDF4B2 write core:** pinned updater adapter, MintClaw serializer, structural readback, independent per-output
   visible verification, artifact transaction, cancellation, crash, and recovery tests. Tool exposure remains off.
4. **PDF4B3 service and tool:** one shared service contract for CLI and the deferred document tool, capability
   advertisement, agent-loop behavior, PDF3 handoff, privacy and approval enforcement. No new model-visible tool.
5. **PDF4B4 system exit:** real-process, channel, deployed `linux/amd64`, rollback, diagnostics, cleanup, and exit
   record. Only this PR may change the roadmap status to complete.

## Required Test Matrix

The stable synthetic or redistributable corpus must include:

- ordinary AcroForm without XFA, to prove no regression or routing confusion;
- admitted pure static packet-array XFA with text, required/optional, empty, Unicode, and XML-metacharacter values;
- XFA stream representation, missing/duplicate packets, malformed XML, bad namespaces, entities, and over-limit
  packets;
- hybrid AcroForm/XFA, foreground rendering, dynamic rendering, repeating subforms, occurrence/page growth, overflow,
  ambiguous/duplicate bindings, and unsupported field types;
- JavaScript, FormCalc, events, actions, submit/connect/external-data references, remote URLs, local paths, and hostile
  redirect attempts;
- signed, certified, timestamped, rights-enabled, restricted, password-required, and encrypted PDFs; and
- timeout, cancellation, renderer crash, worker crash, service restart, concurrent same-source writes, stale approval,
  unknown outcome, artifact-commit failure, and delivery ambiguity.

Every fixture records a stable ID, SHA-256, construction/source, license, expected classification, expected operation
result, and expected visible/render result. Personal, government, tax, medical, immigration, or otherwise sensitive
documents are forbidden as committed fixtures.

## Acceptance Criteria

PDF4B is complete only when all of the following are true:

1. Stage 0 passes from clean packaged `linux/amd64` artifacts with no runtime package installation, external network,
   ambient secrets, privileged process, persistent browser profile, orphan process, or scratch residue.
2. Inspection admits exactly the positive subset above and every negative fixture receives a stable typed refusal;
   existing pure/hybrid-XFA refusal tests remain green until the final capability gate is enabled.
3. Field listing is deterministic across repeated processes and restarts; field IDs bind to document structure rather
   than model prose, display labels, or iteration accident.
4. XML-safe serialization and exact readback pass the full Unicode/metacharacter/bounds corpus; direct raw XML and
   caller-supplied paths or expressions are impossible through public contracts.
5. The source is byte-identical, the output is a separately owned artifact, and repeated use of one idempotency key
   cannot create divergent writes or deliveries.
6. Every successful output passes both structural readback and independent XFA-visible rendering of every affected
   page. Dataset-only, hidden, clipped, blank, stale, or page-count-changing output fails.
7. Scripts, actions, dynamic/foreground/hybrid layout, external access, signatures/restrictions, encryption,
   malformed input, limits, cancellation, crashes, and unknown states cannot publish a successful artifact.
8. Worker isolation, process-tree termination, resource accounting, cancellation, cleanup, and service-restart
   recovery pass under the actual packaged renderer, not a test stub.
9. CLI, shared service, deferred document tool, agent loop, PDF3 approval handoff, and one real channel all exercise
   the same contracts. Model choice or fallback cannot bypass inspection, approval, verification, or artifact state.
10. Diagnostic traces, logs, status reports, and failure messages contain document IDs, field IDs, counts, hashes,
    classes, and redacted correlations only; protected values, dataset XML, credentials, and document bytes do not
    leak.
11. Merged-main deployment on the supported host passes automated smoke and a real operator flow, including visible
    verification and one logical delivered artifact; health, rollback, no-legacy, and cleanup evidence is recorded.
12. An exit record names exact commits, package versions, fixture hashes, commands, markers, supported limits,
    rollback steps, known viewer-portability limits, and all deferred work.
13. macOS remains disabled and fail-closed until its separately admitted platform-parity qualification passes the same
    clean-package, security, semantic, and visible-verification contract.
14. The final diff contains no XFA-specific daemon, broker, queue, database, model-visible tool, browser editing API,
    desktop automation, dynamic-XFA support, flattening, or commercial fallback.

## Manual And Automated Verification

PDF4B must extend the existing document smoke harness with stable markers for renderer isolation, subset
classification, XML round-trip, source immutability, structural verification, visible verification, refusal matrix,
cancellation/recovery, privacy, single delivery, cleanup, and deployed success. The same harness accepts an explicit
fixture/output directory and leaves inspectable evidence only when requested.

The exit record must also give an operator a short manual flow: list fields from a known admitted fixture, fill values
containing `&`, `<`, non-ASCII text, and an emoji, inspect the before/after verification images, download the emitted
PDF, and run one unsupported dynamic/scripted fixture to observe a typed refusal. Manual inspection supplements the
automated oracle; it never replaces it.

## Stop Gates

Stop this goal without shipping mutation if any of these remains true:

- production renderer packaging or isolation fails Stage 0;
- the supported subset cannot be decided before mutation from bounded document facts;
- deterministic field binding or XML-safe scalar mutation cannot be proven;
- the emitted PDF cannot be independently rendered and visibly verified per operation;
- scripts, actions, external data, dynamic layout, signatures/restrictions, encryption, or unknown features can cross
  the positive gate;
- production support requires another service/control plane or browser-authoritative edit path; or
- deployed privacy, cancellation, cleanup, recovery, rollback, or no-blind-replay evidence fails.

On a stop, preserve the useful detection and typed-refusal work, disable capability advertisement, record the failed
gate, and keep all XFA writes unsupported.

## Non-Goals

This goal does not include dynamic or foreground XFA, hybrid authority, FormCalc or JavaScript execution, external
data connections, repeated subforms, page growth, arbitrary XML editing, flattening, signatures, certification,
usage rights, encryption/password handling, legal or business validation, form-specific rules, commercial backends,
provider-native PDF transport, OCR, tables, redaction, general transformations, PDF generation, companion placement,
or macOS implementation.
