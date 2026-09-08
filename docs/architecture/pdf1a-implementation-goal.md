# PDF1A Implementation Goal

## Status

Admitted implementation contract for PDF1A on `linux/amd64`. PDF0A immutable acquisition and the
mandatory one-shot worker, plus PDF0B structural inspection, are complete and remain prerequisites.
This document is the source of truth for PDF1A scope, pull-request boundaries, evidence, deployment,
and completion.

PDF1A adds read-only local extraction and page rendering, then exposes that same service through one
compact deferred agent tool and one on-demand PDF skill. It ends after one real-channel and deployed
vertical slice. It does not admit provider-native PDF transport, password handling, form writes,
durable form state, XFA mutation, OCR, arbitrary document formats, or companion placement.

## Operator outcome

An operator can give MintClaw one exact local or inbound text, mixed, or scanned PDF and ask a
question. MintClaw either:

1. extracts bounded page text and answers with explicit page provenance;
2. renders selected pages, uses an already configured vision-capable model path, and answers with
   explicit page provenance;
3. retains and delivers requested page-render artifacts through the existing media and outbox path;
   or
4. returns one typed unavailable, unsupported, denied, canceled, failed, or uncertain outcome without
   claiming that another file was read or an artifact was delivered.

The operator-facing commands remain part of the existing MintClaw binary:

```sh
mintclaw document extract --input <pdf> --pages <selection> --output <text-artifact> --json
mintclaw document render --input <pdf> --pages <selection> --output-dir <directory> --json
```

The extractor or renderer selected behind the worker is a private backend implementation. It is not
a second public MintClaw CLI, daemon, MCP server, or permanent model-visible tool.

## Product and architecture decisions

### Reuse the existing worker and document service

- `pkg/document` remains the sole owner of immutable input, inspection, extraction, rendering,
  normalization, limits, and artifact adoption.
- CLI and agent adapters call the same Go service operations. The PDF skill selects a workflow but
  never shells together parsers or treats command prose as truth.
- Untrusted PDF bytes are opened only through PDF0A acquisition and parsed only in the existing
  one-shot child. No parser or renderer fallback runs in the gateway process.
- The child may launch one pinned backend executable as its descendant. It receives PDF bytes through
  the inherited immutable descriptor or bounded standard input, never through the source path. The
  existing process-group cancellation, scrubbed environment, deadline, and cleanup rules cover the
  descendant.
- No persistent worker, new service unit, broker, coordinator, container manager, namespace manager,
  or general sandbox is admitted.

### Private artifact handoff

Extraction and rendering cannot place document bytes in worker JSON. The worker writes only
allowlisted relative artifact names in its private operation directory and returns path-free
descriptors. Before deleting worker scratch, the parent:

1. opens each result beneath the known worker root without following symlinks;
2. checks type, count, declared size, aggregate byte and pixel budgets, SHA-256, MIME signature, and
   page ownership;
3. copies or atomically adopts valid files into the operation-owned snapshot artifact directory;
4. marks them read-only and exposes internal handles to the service caller; and
5. removes every unadopted or partial file on success, failure, cancellation, timeout, or crash.

Local CLI callers copy verified artifacts to caller-selected destinations and do not retain them in
`MediaStore` unless explicitly requested. The agent adapter registers verified artifacts in the
existing authority-scoped `MediaStore` before it returns refs or requests delivery. A report never
contains a protected local path.

### Stable request, report, and artifact contract

PDF1A extends `mintclaw.document_report.v1`; it does not introduce a parallel report family. The
normalized request records:

- exact `DocumentRef`, source digest, operation ID, operation, and one-based selected pages;
- explicit character, page, pixel, dimension, artifact-byte, worker-output, and runtime limits; and
- extraction or rendering options that were honored. An unknown option or silently ignored page or
  password argument is an error.

The report records only safe structured evidence:

- inspection facts and selected pages;
- backend name, pinned version, role, and isolation mode;
- per-page artifact kind, MIME type, size, SHA-256, dimensions when applicable, and truncation state;
- extracted character counts and page coverage, never extracted content;
- aggregate budgets consumed, warnings, terminal state, and typed failure; and
- source digest on every derivative descriptor.

The admitted extraction artifact is bounded UTF-8 JSON Lines with one page record per line and stable
one-based page identity. The admitted render artifact is one PNG per selected page. Full text and PNG
bytes are artifacts, not base64 or unbounded fields in worker, tool, trace, task, or chat JSON.

The agent tool may project at most 32 KiB of selected extracted text into the current model call. That
projection is protected from ordinary diagnostic previews and canonical tool-result history; the
durable result retains only artifact refs, digests, counts, pages, states, and safe warnings. Reading
beyond the model-view limit requires another explicit bounded page selection, not automatic prompt
growth.

### Page selection and fixed budgets

Page numbers are one-based. A selection is normalized to a sorted, unique, closed set after PDF0B
inspection proves page count. Open-ended, negative, duplicate, reversed, or out-of-range selections
fail before backend execution. CLI extraction defaults to all pages only when the inspected document
fits the operation page limit; agent operations require an explicit selection or use the skill's
bounded first-pass selection.

Initial hard maxima are frozen for this milestone:

| Limit | Maximum |
| --- | ---: |
| Immutable input | 20 MiB |
| Pages per extract operation | 20 |
| Extracted UTF-8 characters | 256,000 |
| Current model text projection | 32 KiB |
| Pages per render operation | 8 |
| Render dimension per edge | 4,096 pixels |
| Render pixels per page | 16 megapixels |
| Render pixels per operation | 32 megapixels |
| Adopted artifact bytes per operation | 32 MiB |
| Worker control JSON | 64 KiB |
| Worker runtime | 30 seconds |

Implementations may choose smaller safe defaults. Raising a hard maximum requires a focused reviewed
change with resource evidence; a user option can only lower a bound.

### Backend qualification

PDF1A enables exactly one production extract/render backend and exactly one independently implemented
test oracle. The qualification compares only the viable paths left by PDF0B:

- ClawPDF/PDFium WASM, pinned with package, PDFium release, WASM digest, Node.js runtime, notices, and
  reproducible installation evidence; and
- Poppler text/render utilities, pinned with executable identity, fonts, packages, notices, and
  reproducible installation evidence.

`pdfcpu` remains the production inspection authority but is not accepted as a general text-layout or
page-raster renderer. Browser viewer automation, `pypdf`, `pikepdf`, runtime `npx`, runtime package
installation, and an agent-chosen shell pipeline are rejected as production read/render paths.

The implementation PR records executable comparison evidence for text order, Unicode, rotation,
crop boxes, image-only and mixed pages, AcroForm appearance rendering, malformed and encrypted input,
limits, cancellation, crash containment, startup time, memory, packaging, license, update, and
rollback. One candidate becomes production and the other the oracle. There is no automatic production
fallback. If neither passes, `extract` and `render` remain unavailable and implementation stops with a
backend decision instead of weakening the worker boundary.

### One deferred tool and one on-demand skill

MintClaw exposes one hidden first-party tool named `document`. Its compact schema contains an action,
one opaque `media://` source ref, explicit page selection, and only action-specific bounded options.
PDF1A actions are `inspect`, `extract`, and `render`; later action names are not advertised early.

- The tool is registered hidden and discovered/promoted by the existing native tool-search registry.
- Typed current-turn `application/pdf` metadata activates the PDF skill, which directs the model to
  discover this one hidden tool through the existing tool-search path; it does not expose all hidden
  tools or mutate global visibility merely because an attachment arrived.
- A normal non-PDF turn receives neither the `document` schema nor the full PDF skill.
- Existing agent tool allow/deny policy still applies. Typed attachment activation cannot bypass a
  denied tool or a turn profile that suppresses tools or skills.
- `document` accepts only an authority-bound opaque ref for agent calls. It never accepts a model-
  supplied local path or selects by filename.
- Extracted content and page images are treated as protected diagnostic context. Reports and lifecycle
  evidence remain visible; content, paths, and raw bytes remain absent.

The checked-in `pdf` skill is a short workflow policy. Its full body activates when the current turn
has an owner-authorized PDF MIME attachment, the user explicitly invokes the skill, or a continuing
in-turn PDF tool sequence already owns the source. It directs the model to inspect first, select pages,
prefer text extraction when proven, render only needed pages, cite page numbers, and propagate typed
failure. It does not contain parser commands, dependencies, or form-specific instructions.

Existing installations receive the exact checked-in skill during deployment. No document turn may
download or install a skill or backend.

### Authoritative attachment routing

PDF activation and model selection consume metadata resolved from the exact current `media://` refs
after owner binding. They do not infer attachment presence or MIME from `.pdf` prompt text, a caption,
or a filename alone.

- The turn carries a bounded typed attachment projection: ref, detected content type, byte size, and
  original filename for presentation only.
- MIME is confirmed from admitted bytes; a PDF extension with non-PDF bytes is refused.
- A PDF attachment forces the primary/heavy route. A scan may continue only when the selected route's
  provider/model supports the existing image input path.
- If no vision-capable route exists, scan reading returns a typed `vision_unavailable` outcome. It
  never pretends empty extraction is a complete answer.
- Two same-name refs with different digests remain distinct through routing, tool execution, report,
  artifact creation, and answer provenance.

Provider-native PDF bytes are forbidden in PDF1A. Providers receive only bounded extracted text or
selected PNGs through existing text/image request paths.

### Read-only lifecycle and delivery

Read-only one-turn operations do not add a durable document job or write journal. The current turn owns
the operation and artifact lifetime. Cancellation or crash publishes no final artifact.

When retained page renders are requested, the verified PNG refs and a `taskresult.Deliverable` carry
the source digest and page mapping into the existing outbound media coordinator. The existing outbox
owns one logical delivery intent, confirmed acceptance, definite failure, and ambiguous acceptance.
PDF1A adds no second sender and never retries an ambiguous delivery under a new identity.

The first real channel is Telegram because it already provides durable inbound files, typed media
metadata, and outbound files/images in the deployed profile. Core behavior remains channel-neutral;
Telegram-specific code only adapts ingress and presentation.

## Required failure vocabulary

In addition to PDF0A/PDF0B failures, PDF1A distinguishes at least:

- `invalid_page_selection`;
- `extraction_limit` and `render_limit`;
- `text_unavailable` for a selected image-only page when render was not requested;
- `vision_unavailable` when a scan cannot reach an admitted image-capable route;
- `artifact_invalid` for a missing, symlinked, mismatched, malformed, or undeclared worker artifact;
- `artifact_registration_failed` before any ref is returned;
- `backend_unavailable` for a missing or wrong pinned runtime/backend;
- worker protocol, output-limit, timeout, crash, cancellation, and input-mismatch failures; and
- `delivery_ambiguous` when remote acceptance cannot be proved.

Password-required, signed, certified, timestamped, rights-enabled, restricted, AcroForm, and XFA
facts remain visible from inspection. PDF1A may read or render unencrypted ordinary AcroForm pages but
does not interpret field semantics. Password-required input remains unsupported. XFA rendering is not
claimed; an XFA document is refused for PDF1A rendering and remains reserved for the separate PDF4
feasibility gate.

## Fixture and evidence contract

Extend the checked-in manifest with deterministic, synthetic, redistributable fixtures for:

- one-page and multi-page text with stable page markers;
- Unicode, rotation, crop boxes, and deliberately ambiguous reading order;
- image-only scan and mixed text/image pages;
- same filename with distinct bytes and distinct expected answers;
- ordinary AcroForm visible appearance, static/dynamic XFA, signed/certified/restricted input, and
  password-required input;
- malformed, truncated, oversized page count, extreme dimensions, excessive decoded text, excessive
  pixels, excessive output bytes, and unexpected backend artifact cases; and
- cancellation, timeout, worker crash, backend crash, malformed response, concurrent operations,
  registration failure, cleanup, confirmed delivery, definite delivery failure, and ambiguous
  delivery.

Every fixture records digest, generator or provenance, license, exact supported operation, selected
pages, normalized expected text markers or visual-golden identity, allowed tolerance, terminal state,
failure code, and test mapping. No personal, production, government-submission, tax, immigration,
medical, or financial document is admitted.

The independent oracle compares normalized per-page text and rendered pixels. Pixel goldens use fixed
DPI, dimensions, background, fonts, color normalization, and a documented tolerance; raw compressed
PNG byte equality is not treated as visual equality.

## Automated and manual proof

The implementation must provide:

1. unit tests for page normalization, budgets, report validation, artifact adoption, ref authority,
   redaction, routing, activation, and terminal failures;
2. production backend contracts and independent oracle/golden comparisons over the fixture manifest;
3. real-process worker tests for descriptor-only input, scrubbed environment, backend descendants,
   bounded control output, artifacts, cancellation, timeout, crash, process-group death, concurrency,
   and deterministic cleanup;
4. CLI smokes using only documented `document extract` and `document render` commands;
5. agent-loop tests proving exact ref selection, deferred tool/skill activation, text and scan paths,
   page provenance, tool denial, and no non-PDF schema/body cost;
6. Telegram E2E tests for inbound exact identity, answer or typed failure, retained render delivery,
   confirmed/failed/ambiguous delivery, and no blind replay;
7. one checked-in automated deployed harness printing a stable PDF1A marker and one copy-pasteable
   manual Telegram checklist; and
8. content-safe logs and a completed deployed diagnostic trace whose lifecycle, tool name, opaque
   correlations, counts, pages, digests, and states are visible while document text, images, paths,
   and raw filenames are absent.

The same fixture and source digest must pass CLI, service, tool, agent-loop, channel, and deployed
proof. Unit tests alone cannot close PDF1A.

## Pull-request sequence

Use at most two dependent implementation pull requests after this admission PR:

1. **PDF1A core read/render.** Qualify and select the backend and oracle; extend the worker protocol,
   report, artifact transaction, fixtures, service operations, CLI commands, capability registry,
   backend/oracle harness, and operator documentation. This PR must independently deliver the local
   CLI vertical slice.
2. **PDF1A agent/channel.** Add the one hidden `document` tool, the on-demand `pdf` skill, typed
   attachment projection and heavy/vision routing, agent model views and image attachments, MediaStore
   registration, canonical deliverable/outbox composition, Telegram E2E, deployed harness, and manual
   checklist.

Do not begin the dependent second PR until the first is merged. Each implementation PR runs `make
fmt`, `make fmt-check`, affected tests, race tests for changed shared packages, changed-package lint,
docs lint, Linux packaging or real-process tests, required CI, automated review, and owner rocket
approval under `mintclaw-autonomous-pr`.

After both implementation PRs merge, deploy only the exact merged `origin/main` revision through
`mintclaw-deployed-ops`, capture a pre-deploy backup, build and install the qualified backend without
operation-time downloads, restart only affected units, run health and bounded-journal checks, execute
the deployed harness and real Telegram checklist, inspect one new redacted trace, and preserve tested
rollback instructions. Then merge one docs-only PDF1A exit record and stop.

## Complete criteria

PDF1A is complete only when all of the following are true:

1. This admission contract is merged and PDF0B remains green and unchanged in authority.
2. One production backend and one independent oracle are selected by executable evidence, with pinned
   versions, digests, licenses/notices, fonts/runtime, SBOM inputs, packaging, update, and rollback.
3. `mintclaw document extract` and `render` use the shared service and existing child boundary, honor
   exact page and resource limits, and return one stable path-free report plus verified artifacts.
4. Parent-side validation rejects undeclared, symlinked, mismatched, oversized, excessive-pixel,
   wrong-MIME, wrong-page, and partial worker artifacts before publication.
5. The fixture matrix proves text, mixed, scan, Unicode/layout, password, signed/restricted, AcroForm,
   XFA, malformed, resource, cancellation, crash, concurrency, privacy, and cleanup outcomes.
6. Only `linux/amd64` advertises `extract` and `render`; both macOS architectures and every other tuple
   remain unavailable and the matching macOS parity slice is retained in the roadmap.
7. One hidden `document` tool calls the same service with an exact authority-bound ref. The full tool
   schema and `pdf` skill body are absent from an unrelated non-PDF turn and present only when policy
   admits the PDF workflow.
8. Routing uses authoritative attachment MIME and identity. Text and scan paths use bounded local
   artifacts; provider-native PDF bytes are never sent. Scans reach a vision-capable route or fail
   with `vision_unavailable`.
9. Agent and Telegram tests prove source digest and selected-page provenance, same-name/different-byte
   isolation, retained page delivery, registration failure, confirmed/failing/ambiguous delivery,
   cancellation, crash, and no blind replay.
10. All implementation PRs are merged after local, CI, review, and rocket gates. The exact final merge
    is deployed to `server@oc` with a verified backup and rollback, healthy services, clean scratch,
    deployed CLI/agent/channel markers, and one completed content-safe diagnostic trace.
11. A docs-only PDF1A exit record is merged with revisions, commands, evidence, residual limits,
    rollback, macOS backlog, and the next separately admitted boundary.

The goal ends immediately after criterion 11. It does not proceed into PDF1B, PDF1C, PDF2, PDF3,
PDF4, PDF5, PDF6, or macOS implementation.

## Stop and architecture-checkpoint conditions

- If neither bounded candidate passes packaging, malformed-input, cancellation, artifact, license,
  and deployed Linux gates, leave `extract` and `render` unavailable.
- If the backend requires document input by mutable/source path, ambient credentials, runtime package
  download, network access, or in-process parsing, reject it.
- If safe artifact adoption requires a new daemon, broker, persistent worker, custom namespace,
  seccomp, container control plane, or broad sandbox framework, pause instead of expanding PDF1A.
- If agent integration requires more than one permanent model-visible document tool, heuristic
  filename correlation, unbounded content in prompts/traces, or a parallel task/delivery system,
  pause instead of weakening the architecture.
- If a scan cannot be correlated to exact pages or the selected model cannot accept the verified PNG
  path, return a typed failure; do not add OCR or provider-native PDF as a hidden fallback.
- Apply every architecture-checkpoint threshold in `mintclaw-autonomous-pr`. Review-driven growth may
  produce a bounded refactor, prerequisite PR, split, or replacement, but never an unbounded third
  implementation PR without a new admission.
