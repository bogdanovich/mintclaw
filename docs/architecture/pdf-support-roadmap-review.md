# Reliable PDF Support Roadmap Review

## Review record

- Date: 2026-09-06
- Repository base: `a8e7267b`
- Scope: architecture completeness of
  [Reliable PDF Support Roadmap](pdf-support-roadmap.md)
- Method: an independent read-only high-reasoning review, direct verification
  against media, routing, provider, interaction, task, outbox, isolation, CLI,
  release, and companion code and architecture documents, and a focused
  independent re-review after the blocker fixes.
- Admission boundary: this review may admit PDF0A only. It does not authorize
  later milestones or select a PDF parser, renderer, filler, or XFA backend.

## Areas checked

The review traced the proposed workflow across:

- inbound attachment identity, ownership, mutation races, and retention;
- typed routing and model-role selection;
- CLI, agent tool, skill, provider-native, and companion boundaries;
- model context and permanent tool-schema cost;
- parser isolation and supported operating systems;
- AcroForm, encryption, signatures, rights restrictions, and XFA limits;
- protected values, human interaction, compaction, restart, and deletion;
- operation journaling, task ownership, artifact registration, and delivery;
- malformed and malicious fixtures, backend conformance, deployed evidence,
  rollback, and manual reproducibility; and
- dependency order, stop gates, vertical-slice size, and optional phases.

## Blocking findings and resolutions

### Attachment ownership and immutable identity

Finding: the first draft overstated current `MediaStore` behavior. Ordinary
inbound refs are durably indexed but are not uniformly owner-bound or backed by
an immutable digest. Reopening a path after hashing would also leave a
replacement race.

Resolution: PDF0A now precedes parsing. It binds inbound refs to route authority,
accepts only regular non-symlink inputs, copies or streams exact bytes into
protected operation scratch while hashing, detects mutation races, and binds
workers, providers, derivatives, and reports to the immutable snapshot digest.

### Mandatory parser isolation and platform truthfulness

Finding: the first draft required sandboxing but allowed an in-process parser
before proving the worker boundary. MintClaw isolation is currently available
on Linux and Windows, not macOS, and the general setting is optional.

Resolution: PDF0A now owns a mandatory fail-closed out-of-process launcher,
protected scratch outside agent-visible workspace `tmp/`, resource and access
limits, and an explicit OS/architecture capability matrix. Initial production
admission is Linux. macOS must report the capability unavailable until an
enforceable sandbox is proven; no in-process parser fallback is allowed.

### Protected values and interaction history

Finding: current durable human interaction stores accepted free text in
canonical model history and supports one question per tool call. That cannot
satisfy the draft's promise that raw form values stay outside ordinary history.

Resolution: PDF3 explicitly depends on protected-value interaction and
encrypted-ledger admission. Raw values live only in protected storage; ordinary
history carries opaque references. The `document` tool is sensitive by default,
generic task field deltas cannot contain raw values, model access is scoped,
and grouped questions require either a defined composite answer or an explicit
protocol extension.

### Runtime ownership and write recovery

Finding: the first draft did not define how document jobs compose with the
existing canonical task deliverable, interactions, media store, and outbox.
Write recovery appeared only in the later conversational phase.

Resolution: the roadmap now has a sole-owner table. `DocumentReport` is a domain
report carried through the canonical `taskresult.Deliverable`, not a parallel
completion payload. A document operation journal starts with the first write in
PDF2 and preserves one operation and artifact generation across restart,
cancellation, failure, and uncertain completion.

### Delivery semantics

Finding: several exact-once phrases exceeded what external channel APIs can
prove. Existing outbox semantics correctly make unknown or partial remote
acceptance ambiguous and forbid blind replay.

Resolution: every phase now creates one durable logical final-delivery intent,
derived from operation identity and artifact-set digest. Confirmed delivery,
definite failure, and ambiguity remain distinct, and the outbox is the sole
delivery-attempt owner.

### Crypto, signatures, providers, and XFA

Finding: password processing, signed-document policy, provider-native limits,
model roles, and XFA success assumptions were underspecified.

Resolution:

- PDF0B classifies encryption, signatures, certification, timestamps, Reader
  Extensions, and DocMDP or FieldMDP restrictions but does not modify them.
- PDF1C separately admits secret lifetime, exact crypto revisions, permissions,
  disclosure, and re-encryption. It may finish with classification-only support.
- PDF1B uses provider-and-model document capabilities, not generic vision, and
  defines privacy, page, byte, MIME, password, and retention behavior.
- document analysis and deliberative audit are separate fail-closed model roles.
- PDF4 is an XFA feasibility gate and may correctly finish with detection and
  refusal only. Mutation requires a sandboxed backend plus an independent
  XFA-capable oracle or authoritative golden evidence.

## Milestone-size findings and resolutions

- The original PDF0 is split into PDF0A acquisition/isolation and PDF0B
  inspection/backend selection.
- The original PDF1 is split into PDF1A local read/render, PDF1B provider-native
  adapters, and optional PDF1C protected crypto input.
- PDF2 now includes the write journal, protected tool boundary, outbox recovery,
  and named end-to-end proof layers.
- PDF3 is blocked on protected-value storage and interaction behavior rather
  than treating them as small implementation details.
- PDF5 is split into independently admitted OCR, table, transformation/redaction,
  and generation slices.
- Companion qualification may follow any stable local operation with a real
  remote use case and is not incorrectly blocked on XFA or OCR.

## Residual unknowns and stop gates

These are intentional decision points, not implied implementation promises:

- PDF0B selects a parser and render/extract path only after executable backend,
  packaging, malformed-input, isolation, licensing, and platform evidence.
- macOS document processing stays unavailable until a mandatory sandbox is
  admitted. Cross-platform buildability does not count as runtime isolation.
- password decryption stays unavailable unless PDF1C proves a protected secret
  path and exact crypto policy.
- signed, certified, timestamped, rights-enabled, and restricted documents are
  inspection-only unless a later focused policy admits a narrower operation.
- XFA mutation may remain unsupported indefinitely without blocking reliable
  ordinary PDF or AcroForm support.
- companion placement is optional and requires an actual remote workload.

## Focused re-review

The focused re-review confirmed that the six original blockers were resolved
but returned `REVISE` for two remaining ambiguities:

1. the top-level objective still implied that external delivery always ends in
   receipt or refusal, omitting ambiguous remote acceptance; and
2. PDF0A did not name the initial runtime tuple or require executable network,
   external-file, and resource-limit escape probes.

The roadmap now defines `uncertain` and `delivery_ambiguous` as valid truthful
terminal reports, without claiming receipt or replaying blindly. PDF0A admits
only `linux/amd64` initially and requires real-process probes for network,
external files, CPU, memory, process count, runtime, output, and scratch
cleanup. Every other tuple advertises the capability as unavailable until it
passes the same boundary and fixture evidence.

A final independent focused pass checked only these two changes and returned
`APPROVE` with both findings resolved.

## Verdict

Approved for PDF0A implementation only, subject to the roadmap remaining the
source of truth for its completion criteria. Later phases require predecessor
evidence and a new focused admission or goal. Architecture review must reopen
if implementation weakens immutable acquisition, mandatory isolation,
protected-value handling, sole-owner composition, ambiguity semantics, or any
fail-closed boundary.
