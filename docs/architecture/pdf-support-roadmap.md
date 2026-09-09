# Reliable PDF Support Roadmap

## Status

Selected execution program. PDF0A and PDF0B are complete for `linux/amd64`; their merged,
deployed evidence and remaining platform limits are recorded in the
[PDF0A exit record](pdf0a-exit-record.md) and [PDF0B exit record](pdf0b-exit-record.md).
PDF1A is admitted for `linux/amd64` under its bounded
[implementation goal](pdf1a-implementation-goal.md). No later PDF milestone is admitted.
This roadmap specializes the repository-level
[`Reliable Document And PDF Workflows`](../../ROADMAP.md#9-reliable-document-and-pdf-workflows)
direction into ordered, testable MintClaw milestones.

The program began with PDF0A after this roadmap and its
[architecture review](pdf-support-roadmap-review.md) merged. Later milestones
are not pre-admitted implementation scope: each requires the prior milestone's
exit evidence and a focused admission or goal that fixes its exact behavior,
authority, dependencies, and completion gates.

## Objective

An operator should be able to give MintClaw a PDF and receive one truthful
terminal operation report:

1. the requested analysis or transformation completed, with exactly the
   verified artifact set promised by the operation and, when channel delivery
   was requested, a durable logical delivery intent;
2. a structured refusal or definite failure identifying the unsupported,
   unsafe, invalid, or unavailable capability; or
3. an `uncertain` or `delivery_ambiguous` state when execution or remote
   acceptance cannot be proved. MintClaw does not claim receipt or blindly
   replay work in this state.

The implementation must work as one system. Manual CLI use, model-visible
tool use, skills, provider-native PDF analysis, local workers, companion
placement, verification, and outbound delivery must share contracts rather
than independently reimplementing document behavior.

This roadmap is document-general. Government, tax, immigration, legal,
financial, medical, and business forms may be retained as licensed or
synthetic regression fixtures, but no form number, agency, or one incident
defines the product architecture.

## Current Baseline And Gaps

MintClaw already provides useful foundations:

- inbound channel attachments are durably indexed behind `media://`
  references and ordinary turn admission binds them to exact workspace,
  agent, actor, route, and session authority plus an immutable size/SHA-256
  identity;
- `MediaStore` retains filename, content type, local backing path, lifecycle
  scope, cleanup policy, and creation time; authority-bound references also
  retain the admitted content identity, while generic unowned references do
  not;
- typed task deliverables and outbound media delivery can represent produced
  artifacts without parsing final chat prose;
- workspace-local scratch storage and cleanup conventions already exist;
- agent skills have a bounded catalog, explicitly activated full content, and
  tool search for deferred model-visible capabilities;
- the routing system already has a general heavy-route decision that a future
  authoritative attachment signal can drive;
- gateway and companion execution, file transfer, approval, and audit
  contracts can host future remote document work without inventing a second
  transport.

The current PDF path is not a document capability:

- an inbound PDF is normally reduced to a local `[file:/path]` prompt tag;
- the runtime does not inspect or classify the PDF before the model chooses a
  strategy;
- the routing feature extractor does not currently recognize `.pdf` text as
  an attachment, inbound messages carry only string media refs, and routing
  does not consume typed attachment metadata directly;
- provider attachments do not yet provide a general original-PDF byte path to
  every provider adapter that supports native PDF input;
- there is no shared document service, versioned document report, field
  ledger, authoritative render gate, or supported-feature matrix;
- current durable human interactions copy accepted free-text answers into
  canonical model history, so they cannot yet carry protected form values;
- process isolation is available on Linux and Windows but not macOS, is
  optional globally, and does not yet provide a mandatory document-worker
  boundary;
- an agent can select arbitrary workspace tools and install dependencies at
  runtime, producing environment drift and weak completion evidence;
- XFA datasets can be changed without proving that a visible form was
  rendered, which can yield a syntactically valid but blank result.

The first implementation work must close these gaps incrementally rather than
introduce a broad document framework with no admitted user flow.

## Product Scope

### In scope

- inspection and classification of local, inbound, and eventually
  companion-hosted PDF artifacts;
- text extraction, image fallback, OCR, page rendering, and bounded page
  selection;
- provider-native PDF analysis when the selected provider and model support
  it;
- AcroForm schema discovery, filling, appearance generation, validation, and
  optional flattening;
- XFA detection from the first milestone and separately admitted support for
  a verified static or dynamic subset;
- detection and truthful classification of password-protected inputs, with
  decryption or editing only after a separately admitted secret-safe crypto
  milestone;
- structural and visual verification before an edited PDF can be called
  complete;
- durable multi-turn collection of form facts without putting sensitive
  values in ordinary prompt history or diagnostic traces;
- one compact model-visible document capability and one reusable PDF skill;
- local CLI, automated fixture, agent integration, channel, and deployed
  smoke tests;
- bounded merge, split, flatten, and ordinary PDF generation only after the
  read and form correctness boundaries are stable.

### Out of scope for the initial milestones

- claiming compatibility with every historical PDF or XFA producer;
- modifying any signed, certified, timestamped, or rights-enabled PDF until a
  narrower policy is separately admitted;
- decrypting, editing, or re-encrypting password-protected PDFs before the
  protected-input and crypto milestone;
- bypassing PDF passwords, permissions, DRM, or certificate security;
- treating a browser PDF viewer as an authoritative editing backend;
- arbitrary desktop automation or hidden Acrobat UI control;
- exposing one permanent model tool for every document subcommand;
- installing global npm, pip, Homebrew, apt, or other dependencies during an
  agent turn;
- silently rasterizing an editable or signed document when semantics matter;
- executing embedded PDF JavaScript, FormCalc, URL, submit, file, or network
  actions outside an isolated deny-by-default worker;
- remote companion execution before the local gateway slice is complete.

## Architectural Rules

Every PDF milestone follows these rules.

1. **The exact attachment is the input.** The runtime resolves an explicit
   owner-authorized artifact reference. It never searches the workspace for a
   filename that looks related.
2. **Identity survives every boundary.** Acquisition copies or streams the
   exact bytes into an operation-owned immutable snapshot while hashing them.
   Source reference, original filename, detected content type, byte size,
   SHA-256 digest, owner scope, and operation identity are retained in
   structured state. Local paths are execution details, not durable or
   model-provided identities.
3. **Inspection precedes strategy.** A worker classifies encryption,
   signatures, page count, extractable text, image-only pages, AcroForm, XFA
   packets and subtype, and unsupported features before filling or flattening.
4. **One service owns document semantics.** CLI and agent adapters call the
   same document service. Skills may choose workflows but do not implement PDF
   parsing, filling, or verification in prompt text.
5. **Backends are replaceable and capability-scoped.** An extraction backend
   cannot acquire filling authority; a dataset editor cannot claim rendering;
   a renderer cannot claim structural round-trip verification.
6. **One operation has one terminal report.** Success, unsupported, denied,
   canceled, failed, and uncertain are typed outcomes. Chat wording is a
   projection, never the source of truth.
7. **Artifact creation is transactional.** Work happens in an owner-scoped
   temporary directory. A final artifact is registered and delivered only
   after verification; incomplete output is not published under the final
   filename.
8. **Verification is independent.** Edited documents pass structural checks
   and a renderer-visible check. Where practical, filling and rendering use
   different implementations so one engine cannot certify its own mistake.
9. **XFA always fails closed.** An XFA document never falls through to an
   AcroForm-only filler. Dataset mutation alone is not completion.
10. **Model judgment is bounded by deterministic evidence.** Models may map
    user facts to fields and audit rendered pages. They cannot override a
    parser failure, unsupported capability, missing artifact, digest mismatch,
    or failed verification.
11. **High-risk review cannot silently downgrade.** Form interpretation and
    final audits use the configured deliberative document model. A missing
    model produces a visible unavailable state unless policy explicitly
    permits a named fallback.
12. **Untrusted documents are parsed out of process.** The document worker is
    mandatory and fail-closed even for Go-native libraries. The minimum
    boundary is a short-lived child of the current MintClaw executable with a
    path-free versioned protocol, inherited immutable input descriptor,
    scrubbed environment, private scratch, runtime/output limits, process-group
    termination, and deterministic cleanup. Stronger network and filesystem
    confinement may use a qualified, packaged operating-system primitive, but
    MintClaw does not grow a custom sandbox manager to provide it. Unsupported
    OS/architecture tuples advertise the document capability as unavailable
    instead of parsing in the core process.
13. **Sensitive values never enter ordinary history or telemetry.** Passwords
    and raw form values use protected references and protected storage. They
    are absent, not merely best-effort redacted, from ordinary tool arguments,
    commands, model history, logs, traces, task field deltas, summaries, and
    review artifacts. Retention and deletion are explicit.
14. **No blind replay of writes.** A lost result after an accepted fill,
    flatten, or conversion returns a stored terminal result or `unknown`; it
    does not repeat the mutation under a new output identity.
15. **Every milestone proves a vertical slice.** A backend abstraction,
    worker, tool, skill, or state machine does not land without an executable
    fixture and a manual proof of the user behavior it enables.
16. **The outbox owns delivery.** A document operation creates one durable
    logical final-delivery intent derived from operation identity and the
    artifact-set digest. Confirmed, definitely failed, and ambiguous delivery
    are distinct; partial or unknown remote acceptance is never blindly
    replayed.

## Shared Component Model

```text
channel attachment / local CLI path
                |
                v
 immutable acquisition and digest
                |
                v
        owner-scoped DocumentRef
                |
                v
     Document Service (one authority)
       |       |       |       |
       |       |       |       +--> job and field-ledger store
       |       |       +----------> verification coordinator
       |       +------------------> backend capability registry
       +--------------------------> artifact transaction
                |
       +--------+---------+------------------+
       |                  |                  |
       v                  v                  v
  Go PDF/AcroForm    render/extract     admitted XFA
     backend            worker             backend
       |                  |                  |
       +------------------+------------------+
                          |
                          v
               versioned DocumentReport
                    /             \
                   v               v
       `mintclaw document`     deferred `document`
              CLI                  agent tool
                                      |
                                      v
                                PDF workflow skill
                                      |
                                      v
                          typed outbound artifact
```

The service is a logical ownership boundary, not a requirement for a new
daemon. Its control plane may be an in-process Go package, but untrusted PDF
parsing runs only in a strictly launched helper process. A persistent worker
is justified only by measured startup, memory, concurrency, or isolation
evidence.

The initial worker is deliberately smaller than a general sandbox subsystem.
It self-spawns from the installed MintClaw artifact for one operation, accepts
one immutable snapshot descriptor plus bounded JSON, and exits. It does not
add a daemon, broker, coordinator, durable worker state, or another installed
binary. If a future backend needs host-level network or filesystem confinement,
PDF0B must qualify an existing OS/container primitive and its packaging. A need
to implement namespaces, seccomp, container lifecycle, or a second control
plane inside MintClaw triggers an architecture checkpoint; it is not absorbed
into PDF0A by scope drift.

### Document reference

`DocumentRef` is the authority-bearing input descriptor. Version 1 requires:

- opaque media or local-operation reference;
- original filename and detected content type;
- exact byte size and SHA-256 digest;
- owner and workspace scope;
- source kind and creation time;
- cleanup and retention policy;
- optional password-secret reference after the protected-input milestone,
  never the password value;
- optional expected digest for caller-controlled replacement protection.

The agent tool accepts an opaque reference, not a raw local path. The local
CLI may accept a path because the operator is the authority at that boundary;
the acquisition layer opens a regular file without following symlinks, copies
or streams its exact bytes into protected operation scratch while hashing, and
then verifies the source did not change during acquisition. Workers and
provider requests receive only the immutable snapshot. A path hash followed by
a later reopen is not sufficient because it leaves a replacement race.

`DocumentRef` authority is not inferred from the existence of a current
`media://` reference. PDF0A must bind every admitted inbound reference to its
workspace, agent, actor, route, and session before acquisition. Every
derivative records source digest, normalized operation and page selection, and
output digest.

### Document inspection

The first inspection report must distinguish at least:

- `pdf.text`;
- `pdf.scan`;
- `pdf.mixed`;
- `pdf.acroform`;
- `pdf.xfa.static`;
- `pdf.xfa.dynamic`;
- `pdf.xfa.foreground` when detectable;
- `pdf.encrypted`;
- `pdf.signed`;
- approval or certification signature, timestamp, Reader Extensions, and
  DocMDP or FieldMDP restrictions when present;
- `pdf.malformed`;
- `pdf.unsupported`.

Classification may include multiple facts: a document can be encrypted,
signed, hybrid AcroForm/XFA, and mixed text/image at the same time. Strategy is
derived from facts and backend capabilities, not from one lossy enum.

### Versioned operation report

Every CLI and tool operation returns the same `DocumentReport` family. The
initial envelope contains:

- schema version, operation ID, job ID, and terminal state;
- input identity and inspection facts;
- requested operation and normalized page or field selection;
- backend names, versions, capability revisions, and isolation mode;
- warnings and structured failure code;
- produced artifact references, MIME types, sizes, and SHA-256 digests;
- structural verification assertions and outcomes;
- rendered-page artifact references and visual-audit outcome where required;
- truncation and limit facts;
- timing facts that reveal performance without retaining document content;
- redacted provenance for model-assisted mapping or review.

Large text, PNG pages, PDFs, OCR data, and debug bundles remain artifacts.
They are not embedded as unbounded base64 or prose inside model-visible JSON.

### Runtime ownership

Document state composes with existing runtime owners instead of creating a
second task or delivery system:

| Owner | Sole responsibility | Does not own |
| --- | --- | --- |
| Document service | Immutable input, document semantics, operation journal, verification, and artifact transaction | Generic task lifecycle or remote delivery attempts |
| `MediaStore` | Retained artifact references and lifecycle | Document correctness or job state |
| Task coordinator | Generic task lifecycle and the canonical `taskresult.Deliverable` | Document parsing or duplicate delivery logic |
| Interaction coordinator | Suspension, question correlation, expiry, and cancel | Raw protected field-value storage |
| Outbox and channel manager | Final delivery intent, attempts, acceptance, recovery, and ambiguity | Document transformation or verification |

`DocumentReport` is embedded in or projected through the canonical
`taskresult.Deliverable`; it is never a parallel completion or delivery
payload. Raw form values are forbidden in generic `FieldDeltas`.

### Backend registry

The service selects only a backend that advertises the required exact
capabilities. Initial candidates are evaluated, not pre-approved dependencies:

| Candidate | Intended role | License/runtime observation | Boundary |
| --- | --- | --- | --- |
| `pdfcpu` | Go-native PDF validation, AcroForm export/fill/appearance/flatten operations | Apache-2.0, evaluated inside the worker | Does not provide a general XFA renderer/editor |
| ClawPDF/PDFium WASM | Text extraction and page rendering with structured budgets | MIT wrapper, Node.js 22+ runtime | No public form-filling or XFA workflow |
| Poppler tools | Independent render/text baseline and fixture oracle | External native processes | Not an authority for interactive forms or XFA completion |
| `pypdf`/`pikepdf` | Test oracle, repair, inspection, or narrowly isolated fallback | Python runtime | XFA is packet-level data, not rendered form semantics |
| `pdfer` | XFA schema and dataset experiment | Young MIT Go project | No complete dynamic layout, FormCalc/JavaScript, or authoritative rendering |
| PDF.js | XFA parsing/rendering and print experiment | Apache-2.0 JavaScript | XFA editing and reliable save are not implemented |
| XFA-enabled PDFium | Dynamic XFA render/interaction experiment | Open-source PDFium with V8/XFA build | Experimental embedder API and substantial build/sandbox cost |
| Acrobat/commercial adapter | Optional compatibility fallback | Licensed external runtime | Never required silently and never the default open-source path |

PDF0B records measured packaging, startup, memory, cancellation, malformed-file,
and cross-platform evidence before selecting the first render/extract backend.
MintClaw must not adopt ClawPDF merely because OpenClaw can import it
in-process: OpenClaw is a Node.js runtime, while MintClaw's portable core and
companion are Go binaries and do not guarantee Node.js on every target.

### CLI

The operator-facing interface is one command family:

```text
mintclaw document acquire
mintclaw document inspect
mintclaw document extract
mintclaw document render
mintclaw document fields
mintclaw document fill
mintclaw document verify
mintclaw document flatten
mintclaw document capabilities
```

Commands use JSON input/output when invoked programmatically. Human-readable
output is a projection. Stable exit classes distinguish usage, unsupported,
denied, password-required, invalid document, limit exceeded, verification
failed, uncertain, and internal failure.

Machine mode writes only the report to stdout and diagnostics to stderr. A
file-producing command uses a caller-selected output path, refuses overwrite
unless an explicit flag is present, writes a same-directory temporary file,
fsyncs as required, verifies it, and atomically renames it. Local CLI output
enters `MediaStore` only when the caller explicitly requests retention.

The CLI and agent tool must call the same Go service API. The skill must not
shell together several CLI commands and infer success from exit text.

### Agent tool and context budget

MintClaw exposes at most one compact, deferred model-visible `document` tool.
Its actions map to the shared service and use opaque artifact refs. Tool search
or attachment-aware activation keeps the full schema out of unrelated turns.

The installed PDF skill contributes only bounded catalog metadata to ordinary
turns. Full instructions activate when:

- the current turn carries an owner-authorized `application/pdf` attachment;
- the user explicitly requests PDF or document work; or
- a continuing document job owns the current turn.

MIME-aware activation must be a typed runtime decision. It must not depend on
matching `.pdf` in prompt text, and an untrusted document cannot inject a skill
name.

### Provider-native PDF analysis

Provider-native document input is an analysis path, not an editing backend.
An adapter may transmit the original PDF bytes only when:

- the exact provider-and-model capability descriptor declares the transport,
  accepted MIME types, byte and page limits, password and page-selection
  behavior, privacy policy, and provider retention or deletion behavior;
- the exact `DocumentRef` remains authorized for the current turn;
- provider size/page/security limits are satisfied;
- the configured privacy policy permits sending the document to that
  provider; and
- the request and response remain bound to the source digest.

Page-filter and password behavior must be explicit. If the provider cannot
honor a requested constraint, MintClaw uses a local verified extraction path
or refuses; it does not silently analyze a different page set.

Document analysis and deliberative document audit are separate routing roles.
Neither is inferred from generic vision support. The configured audit role
must fail closed when unavailable unless an explicit policy names an equally
capable fallback.

### Durable document jobs and field ledger

Read-only one-shot operations need not create a durable job unless they
produce retained artifacts or cross a turn boundary. Multi-turn filling uses
one owner-scoped job with:

- source `DocumentRef` and immutable digest;
- selected strategy and backend capability revisions;
- field schema snapshot;
- per-field stable identity, normalized value, source, confidence,
  confirmation state, and blank reason;
- outstanding questions and their interaction IDs;
- artifact generations and verification reports;
- lifecycle state and terminal outcome;
- retention deadline and deletion state.

Sensitive values are not copied into ordinary chat history, task summaries,
or diagnostic traces. A dedicated persistence admission must define at-rest
protection, key ownership, restart recovery, retention, export, and deletion
before PDF3 lands. Until that admission exists, the implementation must not
claim durable sensitive form recovery.

Current `request_user_input` behavior is not a protected value channel: it
persists accepted free text in canonical history and accepts one question per
tool call. PDF3 therefore depends on a protected-value interaction mode that:

- stores raw answers only in the encrypted, retention-bounded field ledger;
- places opaque value references and non-sensitive state in canonical history;
- reveals necessary values to a model only through a protected, scoped view;
- marks `document` tool calls and results sensitive by default; and
- either defines one composite answer for grouped facts or explicitly extends
  the interaction protocol, while retaining buttons, free text, and cancel.

A minimal durable operation journal begins with the first write operation in
PDF2. Its states are `prepared`, `accepted`, `processing`, `verified`,
`registered`, and `delivery_intent`, plus terminal `canceled`, `failed`, and
`uncertain`. Recovery reconciles the same operation and artifact generation;
it never allocates a fresh output identity to replay an uncertain write.

The ledger distinguishes at least:

- supplied by user;
- extracted from the source document;
- derived deterministically;
- suggested by a model but unconfirmed;
- confirmed by user;
- intentionally blank with reason;
- not applicable;
- conflicting;
- invalid under field constraints.

### Verification and completion

A fill or transformation can be `succeeded` only when all applicable gates
pass:

1. the source digest still matches the admitted job;
2. the output exists, parses, and has the expected document identity and page
   count or an explicitly accepted page-count change;
3. the backend reports every requested field or transformation outcome;
4. round-trip field extraction matches expected normalized values;
5. required appearance streams or flattened page content exist;
6. every affected page renders within limits;
7. visible values are present and not clipped, overlapped, substituted,
   missing, or placed on the wrong page;
8. unexpected deltas outside affected regions are absent or explicitly
   accepted;
9. signatures, encryption, metadata, and accessibility changes are reported;
10. the final artifact digest is registered in `MediaStore` before delivery;
11. one durable logical final-delivery intent is created for the artifact set
    or truthful failure; its terminal outcome follows the existing confirmed,
    definite-failure, or ambiguous delivery contract.

Visual audit may use deterministic geometry/text evidence and a capable model,
but the model receives bounded rendered artifacts and the expected field map.
It does not decide whether a parser error should be ignored.

## Fixture And Test Program

### Fixture policy

Fixtures must be synthetic, redistributable, deterministic, and free of real
personal data. A generator is preferred over committing externally sourced
sensitive forms. When a real-world document is necessary for interoperability,
its license, provenance, checksum, retention, and CI eligibility must be
recorded explicitly.

The corpus begins in PDF0A and grows only with admitted behavior:

- one-page and multi-page text PDFs;
- image-only scan and mixed text/image PDF;
- Unicode, right-to-left, multiline, checkbox, radio, list, combo, date, and
  repeated-name AcroForm fields;
- AcroForm with missing or stale appearances;
- static, hybrid, foreground, and dynamic XFA samples;
- XFA scripts that attempt file or network access;
- owner-password and user-password encryption;
- signed PDF and fillable signed PDF;
- approval and certification signatures, timestamps, Reader Extensions, and
  DocMDP or FieldMDP restrictions;
- supported and unsupported encryption and permission revisions;
- incremental updates, object streams, xref streams, crop boxes, rotations,
  embedded actions and attachments, and font-substitution cases;
- malformed xref, truncated, oversized, decompression-bomb, extreme-page-size,
  excessive-page-count, and deeply nested object fixtures;
- ambiguous duplicate filenames with distinct digests;
- documents whose visible pages disagree with embedded field data;
- documents that require a page-count change after dynamic layout;
- long conversational field collection that crosses compaction and restart.

Every fixture declares expected inspection facts, supported operations,
expected structural values, expected rendered pages, expected warnings, and
expected failure codes.

### Required proof layers

Milestones select mandatory layers from this fixed proof vocabulary:

1. **Unit tests** for parsers, normalization, policy, result states, redaction,
   and limit handling.
2. **Backend contract tests** executed against every enabled implementation.
3. **Golden fixture tests** for inspection, fields, structural output, and
   rendered pages with intentional tolerance rules.
4. **CLI smoke tests** using only documented commands and stable JSON.
5. **Agent integration tests** proving the compact tool and skill use the same
   service and exact attachment.
6. **Channel E2E tests** proving inbound attachment identity and outbound PDF
   delivery.
7. **Real-process tests** for helper startup, cancellation, timeout, crash,
   malformed input, concurrent jobs, and cleanup.
8. **Deployed smoke tests** from merged `main`, including rollback and log
   redaction checks.

The canonical milestone harness prints a machine-readable evidence bundle and
a short manual checklist. Passing unit tests alone never closes a milestone.
Each milestone below names its mandatory proof layers; a missing layer is a
blocker, not an implicitly waived "not applicable" item.

## Ordered Milestones

### PDF0A: Immutable acquisition and mandatory worker boundary

#### Operator outcome

An operator can pass a local PDF to MintClaw and receive its immutable identity
or a typed acquisition refusal. No PDF parser runs in the core process, and a
platform without the mandatory worker boundary reports document processing as
unavailable.

#### Scope

- introduce the versioned `DocumentRef`, operation identity, capability
  descriptor, terminal state, and minimal `DocumentReport` envelope;
- add `mintclaw document acquire` and `mintclaw document capabilities` through
  the shared service entry point;
- bind inbound refs to exact owner authority before document admission;
- acquire only regular files, without following symlinks, into protected
  operation scratch while calculating size and SHA-256;
- detect replacement or mutation races and discard incomplete snapshots;
- add a mandatory short-lived document-worker subprocess with a path-free
  versioned protocol, cancellation, timeout, bounded output, process-group
  termination, scrubbed environment, private scratch, and deterministic
  cleanup;
- keep protected scratch outside agent-visible workspace `tmp/`;
- admit `linux/amd64` as the initial runtime tuple and advertise every other
  tuple as unavailable until separately proven;
- establish the fixture generator and initial manifest without real personal
  data; and
- provide one automatic harness and one documented manual acquisition smoke.

#### Not in scope

- agent tool registration;
- PDF object parsing, page count, extraction, or rendering;
- provider-native analysis;
- OCR, field filling, flattening, or outbound artifact delivery;
- durable document jobs;
- XFA mutation or rendering.

#### Required evidence

- required layers: unit, CLI, real-process, Linux deployed smoke, privacy, and
  cancellation/recovery tests;
- symlink, FIFO/device, path replacement, mid-copy mutation, oversized input,
  cancellation, worker crash, and concurrent acquisition fixtures have typed
  results and leave no admitted partial snapshot;
- real-process probes prove that the `linux/amd64` worker receives no source
  path or ambient secrets, cannot exceed its runtime or output limits, loses
  its complete process group on cancellation, and cannot retain protected
  scratch after cleanup;
- two files with the same name but different bytes keep distinct identities;
- ordinary inbound refs are owner-bound before acquisition and cross-route
  resolution is denied;
- reports and traces contain only opaque refs, digests, sizes, limits, states,
  and safe correlations, never local paths or document bytes;
- no dependency is downloaded or installed during an operation;
- the first admitted runtime tuple is `linux/amd64`; Linux ARM, macOS, Windows,
  and other tuples remain explicitly unavailable until a follow-up proves the
  same real-process isolation and fixture suite; and
- the manual CLI and deployed smokes reproduce checked-in expected reports.

#### Stop gate

Do not begin PDF0B until untrusted bytes can reach only an immutable snapshot
and the mandatory bounded worker process on every advertised platform. An
in-process parser does not satisfy this gate. If backend qualification later
shows that acceptable risk requires stronger host confinement, use a packaged
standard primitive or return to an explicitly documented OpenClaw-like WASM
risk model; do not build a general sandbox platform inside the document
milestone.

### PDF0B: Inspection, classification, and backend decision

#### Exit status

Complete for `linux/amd64`. The [PDF0B exit record](pdf0b-exit-record.md) records the selected
backend and oracle, 25-fixture evidence, implementation merge, exact deployment, runtime trace,
rollback, and the stop boundary before PDF1A. No other runtime tuple or PDF1A behavior is admitted.

#### Operator outcome

An operator can run `mintclaw document inspect` against a local PDF and receive
a stable report identifying the exact input, relevant document facts, and
whether later MintClaw milestones can safely process it.

#### Scope

- add inspection facts and artifact descriptors to `DocumentReport`;
- parse only inside the PDF0A worker and detect PDF magic, page count,
  encryption, signature and rights restrictions, extractable-text presence,
  AcroForm, XFA presence/subtype, malformed input, and limits;
- benchmark and security-test `pdfcpu`, ClawPDF, Poppler, and only the minimum
  alternatives necessary to select the PDF1A render/extract path;
- pin worker dependencies, fonts, licenses, SBOM inputs, packaging, update,
  rollback, visual tolerance, and supported platform tuples;
- record one selected production path and separate test oracle in a focused
  admission; and
- make encrypted, signed, rights-enabled, and XFA inputs fail closed for every
  operation not yet admitted.

#### Not in scope

- agent tool registration or channel delivery;
- provider-native analysis;
- OCR, field filling, flattening, or durable document jobs;
- password decryption or XFA mutation and rendering.

#### Required evidence

- required layers: unit, backend contract, golden fixture, CLI, real-process,
  Linux deployed smoke, privacy, and cancellation tests;
- every advertised platform produces the same normalized fixture facts;
- malformed, password-required, unsupported-XFA, signed, rights-enabled,
  oversized, canceled, and worker-crash inputs return distinct typed states;
- capability advertising is generated from passing contract evidence, not an
  optimistic static claim;
- normal logs and diagnostic traces contain no paths or content; and
- the manual inspection smoke reproduces the checked-in report.

#### Stop gate

If no backend passes malformed-input, isolation, packaging, cancellation, and
platform gates, ship acquisition and explicit inspection-unavailable only. Do
not enable a less constrained parser as a fallback.

### PDF1A: Local read, extract, render, and agent/channel slice

#### Completion status

Completed for `linux/amd64` under the [PDF1A implementation goal](pdf1a-implementation-goal.md) and
[PDF1A exit record](pdf1a-exit-record.md). Two implementation PRs, exact merged-main deployment,
the deployed CLI/agent/channel harness, and the content-safe live trace are complete. The roadmap
stops before PDF1B, PDF1C, PDF2, or the separately admitted macOS parity lane.

#### Operator outcome

An operator can attach a text, mixed, or scanned PDF and ask MintClaw a
question. MintClaw uses the exact attachment, returns a bounded answer with
page provenance, and can deliver retained page renders when requested.

#### Scope

- add shared extract and render service operations with page, character,
  pixel, byte, time, and output budgets;
- add a compact deferred `document` tool using opaque media refs;
- activate the PDF workflow skill from typed attachment MIME or explicit user
  request;
- correct model routing to consume authoritative attachment metadata rather
  than `.pdf` text heuristics;
- use the local extraction/image path for all providers in this milestone;
- retain rendered pages and extracted text as typed artifacts rather than
  unbounded tool JSON;
- bind every answer and artifact to the source digest and selected pages;
- deliver analysis or one truthful typed failure through one real channel.

#### Required evidence

- required layers: unit, backend contract, golden, CLI, agent-loop, channel,
  real-process, deployed, privacy, cancellation, and delivery-recovery tests;
- the same fixture passes CLI, tool, agent-loop, channel, and deployed smoke;
- a duplicate filename cannot cause the wrong PDF to be read;
- scans with no text reach a vision-capable route or fail explicitly;
- page/password options are never silently ignored;
- unrelated turns do not receive the full PDF skill or document tool schema;
- cancellation and worker crash leave no published final artifact;
- all logs and traces remain content-safe.

### PDF1B: Provider-native PDF analysis adapters

#### Operator outcome

When an explicitly configured provider and model genuinely support native PDF
input, MintClaw can send the same immutable document bytes for analysis and
return page-bound evidence; otherwise it uses PDF1A or refuses without silent
option loss.

#### Scope

- add provider-and-model document capability descriptors, separate from
  generic vision support;
- add document-analysis routing, explicit privacy/disclosure policy, and
  fail-closed fallback behavior;
- implement one provider adapter at a time using the immutable snapshot;
- bind the request, response provenance, selected pages, and answer to the
  source digest; and
- retain PDF1A as the only generic fallback.

#### Required evidence

- required layers: provider contract, agent-loop, channel, deployed, privacy,
  page-limit, password-option, fallback, and cancellation tests;
- native and local paths cite the expected pages on the fixture matrix;
- the adapter never sends a document when provider privacy policy, MIME, page,
  byte, password, or selection constraints are unmet; and
- each enabled provider/model pair has recorded conformance evidence.

### PDF1C: Protected input and PDF crypto admission

#### Operator outcome

An operator can explicitly supply a password for an admitted encrypted PDF
without exposing it in commands, ordinary history, logs, or traces, or receives
a precise unsupported-crypto or permission result.

#### Scope

- add a secret reference and resolver with bounded lifetime and deletion;
- admit exact user-password and owner-password behavior, supported encryption
  revisions, permission enforcement, provider disclosure, and output
  re-encryption policy; and
- keep all non-admitted encryption and certificate cases inspection-only.

#### Required evidence and stop gate

- required layers: unit, backend contract, CLI, protected-interaction,
  real-process, deployed, privacy, restart, deletion, and negative tests;
- secret values are absent from argv, environment dumps, history, reports,
  logs, traces, crash artifacts, and task state; and
- if a secret-safe path or exact crypto policy cannot be proven, retain
  `password_required` classification and do not ship decryption.

### PDF2: AcroForm discovery, filling, and verified artifact delivery

#### Operator outcome

MintClaw can inspect and fill supported AcroForms from an explicit field map,
then return a structurally and visually verified PDF.

#### Scope

- admit and integrate a Go-native AcroForm backend, with `pdfcpu` as the
  preferred candidate subject to PDF0B evidence;
- implement fields, fill, verify, and optional flatten operations through the
  shared service and CLI;
- support text, multiline, checkbox, radio, list, combo, date, Unicode, and
  repeated widget fields covered by the fixture corpus;
- generate or preserve appearance streams deterministically;
- distinguish field value, widget appearance, and flattened page content;
- detect hybrid XFA and refuse the AcroForm-only strategy;
- refuse signed, certified, timestamped, rights-enabled, encrypted, or hybrid
  input unless an earlier focused policy explicitly admitted that exact case;
- add the durable operation journal before the first write and derive each
  artifact generation and delivery ID from the same operation identity;
- mark the deferred `document` tool sensitive by default and keep field maps
  out of generic traces and task field deltas;
- use the existing outbox as the sole final-delivery owner;
- register and deliver only the verified final artifact.

#### Required evidence

- required layers: unit, backend contract, golden, CLI, agent-loop, channel,
  real-process, deployed, privacy, write-recovery, outbox, and rollback tests;
- requested normalized field values round-trip through an independent reader;
- affected pages visibly contain the expected values under the selected
  independent renderer;
- missing fonts, clipped multiline text, invalid choice values, duplicate
  names, and stale appearances have regression tests;
- source bytes never change in place;
- a failed verification cannot produce a final-ready artifact or success text;
- one logical output intent owns the expected digest and filename, and remote
  ambiguity never causes an automatic second send;
- restart at every journal transition recovers the same operation and artifact
  generation; and
- signed, certified, rights-enabled, encrypted, or hybrid inputs stop with an
  actionable typed result unless an exact admitted policy permits the case.

### PDF3: Durable conversational form workflow

#### Operator outcome

An operator can ask MintClaw to complete a supported form over multiple
messages. MintClaw reuses known facts, asks only necessary questions, survives
compaction and restart, presents a review, and fills only after the required
facts are confirmed.

#### Scope

- admit owner-scoped field-ledger persistence, at-rest protection, retention,
  deletion, and restart semantics;
- add protected-value interactions before collecting any sensitive answer,
  storing raw values only in the encrypted field ledger and opaque references
  in canonical history;
- map user facts to stable field identities with source and confidence;
- integrate durable human interactions with buttons plus free-text replies and
  define whether grouped facts use one composite answer or an explicitly
  versioned interaction-protocol extension;
- allow correction, skip, not-applicable, and cancel paths;
- route interpretation and final audit through the configured deliberative
  document model without hardcoding a provider-specific model name;
- preserve the job across compaction, service restart, provider fallback, and
  ordinary unrelated conversation;
- show a bounded review summary before an approval-bound final fill when
  policy requires it;
- create one logical verified-result delivery intent and close or retain the
  job according to policy.

#### Required evidence

- required layers: unit, protected-value interaction, agent-loop, channel,
  compaction, restart, provider-fallback, deployed, privacy, deletion, and
  delivery-recovery tests;
- a long fixture conversation crosses compaction and restart without losing,
  duplicating, or exposing field values;
- corrections supersede prior values with provenance rather than editing chat
  history;
- the agent never re-asks a confirmed field unless the source document or
  field schema changed;
- ambiguous, conflicting, low-confidence, and required-blank values stop
  completion;
- canceled and expired jobs cannot later fill or deliver a document;
- model fallback cannot bypass the required audit tier;
- diagnostic bundles contain only field IDs, states, counts, and redacted
  correlations.

### PDF4: XFA feasibility and admission gate

#### Operator outcome

MintClaw either proves one precisely declared XFA subset that it can fill and
flatten safely, or records XFA mutation as unsupported while retaining reliable
detection and refusal. A plausible blank output is never an acceptable result.

#### Scope

- investigate one candidate XFA subset from fixture evidence: static first,
  dynamic only when separately proven;
- compare a dataset/schema path such as `pdfer` with PDF.js and XFA-enabled
  PDFium rendering paths;
- run XFA scripts in a deny-network, deny-filesystem, time- and memory-bounded
  worker, or disable scripts and reject documents that require them;
- update datasets only through XML-safe typed serialization;
- render every affected page through an admitted XFA-capable engine;
- flatten successful output into ordinary PDF pages when editable XFA cannot
  be made portable;
- retain the source XFA separately and report the semantic loss caused by
  flattening;
- add an optional licensed backend interface without making it a required
  dependency or silent fallback; and
- end with an explicit `supported-subset` or `detection-and-refusal-only`
  decision.

#### Required evidence

- required layers for a shipped subset: unit, backend contract, authoritative
  XFA golden/oracle, CLI, agent-loop, channel, real-process, deployed, privacy,
  malicious-input, write-recovery, and portability tests;
- static, hybrid, foreground, dynamic, FormCalc, JavaScript, repeating group,
  page-growth, and malicious-action fixtures each have an explicit supported
  or unsupported result;
- a dataset-only change cannot pass the visual completion gate;
- values containing XML metacharacters round-trip safely;
- scripts cannot access network, arbitrary files, credentials, or environment
  secrets;
- XFA semantic correctness is proven by an independent XFA-capable oracle or
  authoritative goldens; two ordinary viewers additionally prove only the
  flattened output's portability;
- unsupported XFA never falls through to AcroForm, browser DOM fill, or
  freeform raster editing;
- the milestone records whether real operator documents justify a later
  dynamic-XFA or commercial-backend phase.

#### Hard stop

If no pinned backend can both mutate the declared subset and produce
independently verified post-edit rendering inside the mandatory sandbox and
supported platform matrix, retain detection/refusal and do not ship XFA
mutation.

### PDF5A-PDF5D: Separately admitted expansion slices

#### Operator outcome

MintClaw handles individually admitted broader PDF work without weakening the
contracts proven by earlier milestones.

#### Scope

- **PDF5A OCR:** language-aware OCR with page, region, engine, language, and
  confidence provenance.
- **PDF5B tables:** table extraction with cell coordinates, merges, confidence,
  and source-page evidence.
- **PDF5C transformations and redaction:** separately admit bounded merge,
  split, rotate, reorder, watermark, true content redaction, and flatten; prove
  that redacted bytes and recoverable metadata are absent, not merely hidden.
- **PDF5D generation:** accessible ordinary PDF generation from structured
  document artifacts with font embedding, metadata, reading order, and
  accessibility checks.

Each slice has its own operator outcome, fixture subset, CLI and agent action,
loss policy, required unit/backend/golden/CLI/agent/channel/real-process/
deployed/privacy evidence, and stop gate. This heading does not authorize all
four slices in one PR or separate permanent model tools.

### PDF6: Optional companion qualification

#### Operator outcome

An operator can run the same admitted document capability locally or on an
explicitly configured companion target, diagnose it, update it, and roll it
back without changing agent workflow semantics.

#### Scope

- admit this milestone after any stable local document operation has an actual
  remote operator use case; it does not depend on XFA, OCR, tables,
  transformations, or generation;
- add versioned typed document commands to the existing node capability model
  only for that admitted operation;
- reuse P2 file transfer, target policy, approval, artifact, invocation,
  cancellation, no-replay, and audit contracts;
- advertise exact backend capabilities and versions per target;
- bind a document job to one target and backend generation;
- add doctor, readiness, update, rollback, cleanup, and passive diagnostic
  support;
- run that operation's full production qualification matrix from merged
  `main`.

Remote placement never grants broader filesystem or shell authority, and an
active job never silently migrates between gateway and companion engines.
Gateway deployment evidence remains mandatory in each earlier user-facing
milestone; PDF6 is not the first production qualification gate.

### Dependency order

```text
PDF0A immutable acquisition + worker isolation
  -> PDF0B deterministic inspection + backend decision
     -> PDF1A local read/render + agent/channel
        -> PDF1B provider-native adapters (independent per provider)
        -> PDF1C protected input/crypto (optional, fail-closed)
        -> PDF2 AcroForm write + journal + verified delivery
           -> PDF3 protected conversational workflow
           -> PDF4 XFA feasibility gate

After the relevant read/write contract is stable:
  PDF5A OCR | PDF5B tables | PDF5C transformations | PDF5D generation
  PDF6 companion qualification for one operation with a real remote use case
```

PDF1B and PDF1C are not prerequisites for ordinary AcroForm filling unless the
selected form requires native-provider analysis or encryption. PDF4 may end
with detection-and-refusal only without blocking the other expansion slices.

## macOS Parity Lane

Linux-first delivery does not silently make a capability portable. Every
milestone or separately admitted slice that first ships on Linux carries a
matching macOS parity item. Both Apple silicon (`darwin/arm64`) and Intel
(`darwin/amd64`) remain `unavailable` until the exact tuple has its own
evidence; success on one does not advertise the other. A skill, browser
viewer, in-process parser, or unrestricted shell command is never a fallback
for a missing macOS worker.

All macOS parity work follows the same gate:

1. its Linux milestone is merged with an exit record and the normalized
   request, report, authority, limit, artifact, and failure contracts are
   frozen for that slice;
2. the identical synthetic fixture manifest and backend contract suite run on
   real macOS hardware, with platform-specific expected differences declared
   in the manifest rather than hidden in prose;
3. a packaged short-lived worker proves descriptor-only immutable input,
   scrubbed environment, private scratch, runtime and output bounds,
   descendant termination, cancellation, cleanup, and no in-process fallback;
4. dependency acquisition, universal/per-architecture packaging, signing,
   notarization where applicable, install, update, and rollback are
   reproducible and do not download software during an operation; and
5. only then does the capability registry advertise that exact macOS tuple,
   with CLI and packaged-app smoke evidence recorded in a parity exit record.

The per-slice parity backlog is explicit:

| Linux-first slice | macOS prerequisite and shared proof | macOS advertising gate |
| --- | --- | --- |
| PDF0A acquisition | Port the one-shot worker launcher without changing `DocumentRef` or the fixture manifest; prove inherited immutable input, process-tree cancellation, limits, privacy, and cleanup on both architectures. | `acquire` becomes supported only for the architecture whose real-process and packaged CLI smokes pass. |
| PDF0B inspection/backend | Package the selected parser and independent oracle with identical normalized inspection facts and malformed/encrypted/XFA refusal fixtures. | Each inspection operation is enabled only for a tuple with pinned backend, SBOM, cancellation, malformed-input, and rollback evidence. |
| PDF1A local read/render | Package the selected extractor, renderer, fonts, and PDF skill inputs; run the same page-provenance, visual-golden, agent, channel, and delivery-recovery fixtures. | `extract`/`render` and the deferred agent capability remain unavailable until CLI, packaged app, agent loop, channel, privacy, and cleanup all pass. |
| PDF1B provider-native adapters | Reuse the same immutable snapshot and disclosure contracts; prove that provider routing and the PDF1A fallback preserve digest, page limits, privacy, and cancellation on macOS. | Enable each provider/model pair independently only after adapter conformance; no native-provider claim can mask an unavailable local operation. |
| PDF1C protected input/crypto | Qualify the macOS secret resolver, process handoff, supported crypto revisions, permissions, deletion, restart, and absence from argv/environment/history/traces. | Advertise each crypto capability only after both architecture-specific packaging and secret-leak negative tests pass. |
| PDF2 AcroForm | Package the admitted writer plus independent renderer/reader and fonts; reuse structural, visual, signature/encryption refusal, journal, and outbox fixtures. | `fields`/`fill`/`verify`/`flatten` are enabled separately only after transactional write, visual verification, recovery, and delivery proof. |
| PDF3 conversational forms | Reuse the platform-neutral job, interaction, and protected-ledger contracts while proving macOS storage protection, restart, compaction, deletion, and final-delivery behavior. | The workflow is advertised only when all underlying document operations are supported on that tuple and protected-state E2E passes. |
| PDF4 XFA | Package the admitted XFA engine and independent oracle with deny-network/filesystem execution and the full static/dynamic/malicious fixture matrix. | Enable only the precisely proven subset; otherwise macOS remains detection-and-refusal-only. |
| PDF5A OCR | Pin engine, language data, models, and resource limits; run identical page/region/confidence goldens. | Enable per architecture and language pack only after packaging, accuracy, privacy, cancellation, and rollback evidence. |
| PDF5B tables | Run the same cell/merge/coordinate/provenance fixtures against the packaged extractor. | Enable only after normalized structural and visual-golden parity passes. |
| PDF5C transformations/redaction | Reuse byte-loss, metadata-removal, structural, visual, and recovery fixtures with packaged backends. | Enable each transform independently; redaction requires proof that content is absent, not hidden. |
| PDF5D generation | Package fonts and generators and run the same metadata, accessibility, reading-order, and rendering goldens. | Enable only after reproducible output and accessibility/visual verification pass. |
| PDF6 companion placement | Qualify the macOS companion worker, capability/version reporting, update, rollback, cancellation, artifact transfer, and no-replay behavior for one already-admitted operation. | Advertise per companion tuple only after the full local contract plus remote production qualification passes. |

Parity is intentionally implemented as focused follow-up slices, not folded
into the Linux milestone after the fact. A parity PR may add platform launch
and packaging code, but it may not fork document semantics or weaken a typed
failure to make a fixture pass.

## PR And Goal Discipline

- Use one active goal per milestone, with the milestone's operator outcome,
  scope, exclusions, evidence, and stop conditions copied into the goal.
- Prefer one vertical behavior per PR. A milestone may be a documented series
  of dependent PRs, but each PR must be independently coherent and tested.
- Do not start a dependent milestone until the predecessor is merged and its
  deployed or explicitly required real-process evidence is recorded.
- Backend-selection spikes end with a decision and executable evidence; they
  do not leave multiple accidental production paths enabled.
- Review comments may tighten correctness, security, recovery, or test
  evidence. They must not silently expand the milestone into later roadmap
  scope.
- After four substantive review/fix cycles or material cross-subsystem growth,
  perform the autonomous PR architecture checkpoint before continuing.
- Record completion in a short exit document containing merged revisions,
  fixture and test evidence, manual commands, deployment result, residual
  limits, rollback, and the exact next admitted boundary.

## Manual Test Contract

Every milestone documents copy-pasteable commands with real paths and fixture
names. Placeholder paths such as `/path/to/config.json` are not acceptable in
the final smoke guide.

The stable shape is:

```sh
# Local CLI proof.
mintclaw document <operation> --input <fixture-or-file> --json

# Automated evidence.
make test-document

# Agent-loop proof using the same source digest and expected marker.
scripts/document-agent-smoke.sh --config <real-config> --fixture <fixture>

# Deployed proof after merged-main rollout.
scripts/document-deployed-smoke.sh --host <configured-host> --fixture <fixture>
```

Exact command names may be admitted in PDF0A or PDF0B, but one milestone must
not invent a second incompatible harness. The manual guide states expected output,
failure modes, cleanup, and how to confirm that no sensitive content reached
logs or traces.

## Global Completion Criteria

The PDF support program is complete only when:

- exact inbound attachment identity survives routing, processing, restart,
  artifact creation, and delivery;
- text, scan, mixed, encrypted, signed, rights-enabled, AcroForm, and XFA
  fixtures produce their declared typed outcomes, including explicit refusal
  when mutation is not admitted;
- CLI, agent tool, skill, provider-native path, local fallback, and channel
  delivery use shared contracts and the same backend service;
- no document is called filled or ready until structural and visible
  verification passes;
- durable field collection survives compaction and restart with raw values
  confined to protected storage and absent from ordinary history, generic task
  deltas, and telemetry;
- unrelated turns pay only the bounded catalog cost, not the full PDF workflow
  or a large tool surface;
- no production turn installs global dependencies or depends on an unpinned
  network package;
- malicious and malformed fixtures remain bounded and cannot access network,
  arbitrary files, credentials, or other workspaces;
- uncertain writes are never blindly replayed;
- a real channel accepts a PDF and creates one logical final-delivery intent
  for a verified result or truthful actionable refusal; ambiguous remote
  acceptance is surfaced and never blindly replayed;
- gateway and any admitted companion placement pass the same conformance
  suite; and
- merged-main deployment evidence, rollback, privacy audit, and known
  limitations are recorded.

## References

- [Repository-level roadmap](../../ROADMAP.md#9-reliable-document-and-pdf-workflows)
- [Media Store Durability](media-store.md)
- [Async Task Delivery](async-task-delivery.md)
- [Durable Human Interaction](durable-human-interaction.md)
- [Routing System](routing-system.md)
- [Workspace Temp Directory](workspace-temp.md)
- [Node Companion Architecture](node-companion.md)
- [OpenClaw PDF tool](https://github.com/openclaw/openclaw/blob/main/docs/tools/pdf.md)
- [OpenClaw document-extract implementation](https://github.com/openclaw/openclaw/blob/main/extensions/document-extract/document-extractor.ts)
- [ClawPDF](https://github.com/openclaw/clawpdf)
- [pdfcpu forms](https://pdfcpu.io/form/form)
- [pypdf forms](https://pypdf.readthedocs.io/en/stable/user/forms.html)
- [pikepdf XFA boundary](https://pikepdf.readthedocs.io/en/latest/topics/interactive_forms.html#xfa-forms)
- [pdfer gaps](https://github.com/benedoc-inc/pdfer/blob/main/GAPS.md)
- [PDF.js](https://github.com/mozilla/pdf.js)
- [PDFium XFA build flag](https://pdfium.googlesource.com/pdfium/+/refs/heads/main/pdfium.gni)
- [PDFium form-fill API](https://pdfium.googlesource.com/pdfium/+/refs/heads/main/public/fpdf_formfill.h)
