# Browser Capability BF2 Media, Transfer, Diagnostics, And Snapshot Admission

Status: admitted for implementation

This admission is the implementation authority for Phase 6 of the
[Browser Functional Parity Execution Goal](browser-functional-parity-execution-goal.md).
It completes the shared first-party media, artifact-transfer, bounded
diagnostic, and large-snapshot contract across gateway-hosted and
companion-hosted browser sessions.

The implementation reuses the existing browser broker, private Playwright
adapter, node WebSocket, P2 transfer frames and artifact spool, semantic
references, owner routing, exact capability catalogs, and no-blind-replay
rules. It does not create another browser engine or expose raw Playwright MCP,
CDP, selectors, JavaScript, response bodies, headers, cookies, storage state,
credentials, browser profile paths, node paths, or binary payloads to the
model.

## Current shipped baseline

Phase 6 is a parity and hardening milestone, not a rewrite of gateway B2.

| Capability | Gateway target | Companion target | Phase 6 gap |
| --- | --- | --- | --- |
| Page screenshot | Retained PNG through `browser_observe` | Not advertised | Shared capture and companion output transfer |
| Element screenshot | Not exposed | Not exposed | Fresh semantic element capture on both placements |
| Retained input upload | `upload` and `file_chooser` artifact path | `file_chooser` staging only | Advertise the shared upload contract only after companion parity |
| Browser download | Retained output artifact and optional delivery | Not advertised | Companion capture, reverse transfer, retention, and recovery |
| Diagnostics | Passive target readiness | Passive target readiness | Bounded session diagnostics with privacy-safe summaries |
| Semantic snapshot | Source-bounded; local result | Source-bounded inline node invocation result | Explicit production-WSS chunking, backpressure, and post-action outcome separation |

The existing node transfer protocol already defines authenticated upload and
download directions, fixed maximum frame sizes, sequence numbers, per-chunk
acknowledgements, bounded peer queues, cancellation, status, commit, digest,
and backpressure errors. Phase 6 extends the browser host and gateway worker to
use that contract for browser-owned output. It must not introduce base64 in a
generic node invocation result or a second artifact protocol.

## Model-visible contract

### Screenshot capture

Add a first-party `browser_capture` tool with this typed request:

- `browser_session_id`, `tab_id`, `snapshot_id`, and
  `snapshot_generation` copied from one fresh observation;
- `frame_id`, `context_catalog_id`, and `context_generation` copied together
  when the selected context requires them;
- `target: page|element`; and
- `ref` required only for `target=element` and copied from the same fresh
  observation.

The model cannot provide a selector, rectangle, coordinate, format, quality,
scale, output path, filename, content type, digest, size, or delivery path.
Trusted code captures one PNG with fixed CSS scale and bounded dimensions.
Element capture revalidates the exact semantic ref, selected document, and
private target immediately before the private driver call.

The result is one `browser_screenshot` artifact containing only its opaque
reference, fixed `image/png` content type, safe filename, size, SHA-256,
expiry, browser session, tab, snapshot, generation, target kind, and
`truncated=false`. The PNG bytes and every host path remain outside model
context. Existing `browser_observe(screenshot=true)` remains the compatible
page-capture shorthand and uses the same broker operation and artifact result.

Screenshot capture is read-only and needs no action approval. Capture and
artifact transfer are separate outcomes. If capture succeeds but transfer or
retention becomes uncertain, recovery retries only the immutable captured
bytes identified by their digest; it never silently takes a second screenshot.

### Upload

The existing `browser_act(kind=upload)` shape is the shared upload contract:

- one fresh semantic chooser `ref`;
- one owner-bound retained `artifact_ref`; and
- the standard fresh session, tab, frame, context, snapshot, profile, and
  catalog authority.

`upload` selects the retained input in the browser and may cause page network
activity, so its trusted effect remains `unknown` and it requires exact
approval. The companion implementation reuses the Phase 5 staged-artifact
path. `file_chooser` remains the narrow ordinary-interaction name; both names
must resolve to the same trusted validation, staging, driver primitive, final
evidence, and cleanup semantics rather than diverging implementations.

Multiple files, directories, symlinks, host paths, driver file discovery,
model-provided MIME data, and arbitrary multipart construction remain denied.

### Download

The existing `browser_act(kind=download)` shape is extended to every
supporting placement. It accepts one fresh semantic download target and the
optional existing `deliver` flag. Trusted code fixes the byte limit, captures
exactly one completed response, computes size and SHA-256 while streaming, and
retains one `browser_download` artifact in the gateway P2 spool.

The result contains only bounded metadata and an opaque artifact reference.
Optional chat delivery uses the existing recoverable outbound commit path.
The model cannot provide an output path, filename, content type, request URL,
headers, cookies, body limit, stream ID, or transfer destination.

Download may initiate external page behavior and remains subject to the
existing exact approval and no-replay contract. If the driver action reaches a
terminal state but artifact transport fails, the tool reports the terminal
action state and a separate `artifact_state`. Recovery may resume or repeat
transfer of the immutable node-owned output until expiry; it must never click
or invoke the page download again automatically.

### Session diagnostics

Add a first-party `browser_diagnostics` tool bound to one owned browser
session and, when supplied, one fresh tab, frame, context catalog, and snapshot
generation. The request may select only these fixed categories:

- `console_errors`;
- `failed_requests`; and
- `page_crashes`.

The result is an ephemeral, bounded summary. It contains counts, timestamps,
severity or resource classes, normalized failure codes, safe origins and URL
paths without userinfo, query, or fragment, source line numbers when safe,
and domain-separated message hashes. It never contains raw console text,
request or response headers, request or response bodies, query values,
cookies, storage state, page source, stack traces, driver payloads, or
credentials.

Each category has independent count and byte limits, deterministic ordering,
an `omitted_count`, and `truncated` marker. Diagnostics do not open a session,
change controller state, or mutate the page. Tool results use the same
protected durable-result treatment as page observations so live summaries do
not enter the invocation ledger, conversation persistence, traces, or events.
Only bounded category counts, hashes, and truncation metadata may enter
diagnostic trace records.

`browser_targets.features.diagnostics` means that the selected target and
profile support these session diagnostics. Passive readiness remains
available independently and must not set this feature to true by itself.

## Private companion output-transfer contract

Browser-owned output uses `TransferDownload` frames over the authenticated
node WebSocket generation. The gateway opens the stream after receiving a
small typed command result that binds:

- transfer ID and immutable artifact kind;
- routed owner, agent, actor, workspace, and browser session;
- target, profile and browser-policy revisions;
- originating command or action invocation;
- tab, frame, context, document and snapshot authority where applicable;
- safe filename and content type;
- exact size and SHA-256; and
- capture time, expiry, and cleanup policy.

The companion host keeps captured output in a mode-`0600` private temporary
file or equivalently bounded private buffer. It never returns that path. A
download-direction prepare frame must match the current connection generation
and every bound field. The companion then sends fixed bounded chunks in
sequence and waits for an acknowledgement before the next chunk. The gateway
writes into a pending P2 artifact, verifies size and digest, commits only after
the final frame, and then acknowledges committed state.

The transfer has explicit accept, deny, chunk, acknowledgement, commit,
committed, cancel, status, failure, deadline, and backpressure outcomes.
Disconnect, cancellation, quota failure, digest mismatch, sequence mismatch,
late acknowledgement, or retention expiry removes incomplete gateway and node
staging. A committed immutable output may be transferred again by the same
owner and invocation until expiry, but the browser driver operation is never
replayed.

The existing upload-direction Phase 5 path remains the authority for input
artifacts. Shared helpers may be extracted, but upload and download directions
retain distinct admission and lifecycle checks.

## Large semantic snapshot delivery

Companion observations must no longer depend on fitting a large snapshot into
one generic invocation result. Introduce a typed observation-output envelope
that keeps URL, origin, title, generation, truncation, reference count,
payload size, digest, and transfer binding in the command result while moving
the bounded semantic snapshot projection through the same authenticated
download-direction transfer stream.

The private Playwright adapter bounds and sanitizes the semantic projection at
the source before any node serialization:

- valid UTF-8 only;
- at most `snapshot_bytes` and `snapshot_refs`;
- deterministic truncation at a valid boundary;
- no private selector or raw driver target;
- a fixed serialized envelope budget below `tool_result_bytes`; and
- an explicit `truncated` and omitted-reference count.

The gateway reconstructs the projection only after sequence, size, digest,
timeout, and policy validation, then remints gateway-owned semantic refs. It
never persists the snapshot payload. Chunk size, in-flight frame count,
acknowledgement timeout, total transfer timeout, and reconstructed byte limit
are fixed trusted limits and are reported through `browser_targets` where
useful to the caller.

Snapshot transport failure has outcome-specific behavior:

- a standalone observe is read-only and returns a retryable
  `snapshot_transfer_failed` safe error;
- an already accepted browser action keeps its actual terminal action state
  and reports `observation_state=unavailable` with an instruction to observe
  again; and
- neither case changes a successful or unknown action into a replayable
  action request.

Gateway-local observations use the same source and model-visible bounds even
when they do not require WSS chunking. This keeps truncation and safe-error
semantics placement-equivalent.

## Capability and limit discovery

`browser_targets` computes one immutable runtime-generation view and
advertises only fully usable capabilities for each target and profile:

- page and element screenshot support independently;
- upload and download support;
- diagnostic categories;
- snapshot source, serialized, and transfer byte limits;
- artifact byte, count, retention, and concurrent-transfer limits; and
- chunk size, in-flight frame count, and transfer timeout.

Existing boolean feature fields remain compatible, but they must be derived
from the exact detailed capability intersection. A companion does not
advertise screenshot or download until the approved catalog, host handler,
reverse transfer, gateway retention, and selected driver all support it. A
gateway target does not advertise element screenshot or session diagnostics
until its local adapter supports the same contract. Every use revalidates the
same limits and current catalog generation.

## Artifact ownership, privacy, and retention

Every screenshot, upload, download, diagnostic request, and streamed snapshot
is bound to the existing `browser.Owner` route plus the active browser session
and placement. Cross-agent, actor, route, workspace, session, target, profile,
tab, frame, snapshot, invocation, digest, expiry, or connection-generation use
fails closed without revealing whether another artifact exists.

Artifact content, protected form values, raw diagnostics, and semantic
snapshots do not enter durable browser records, node invocation results,
approval prompts, task state, events, traces, logs, or model-visible errors.
Durable records keep only the minimum identity, hashes, sizes, safe media
metadata, terminal state, and retention timestamps needed for recovery and
audit.

Session close, owner cleanup, quarantine, profile expiry, node disconnect,
gateway restart, transfer cancellation, and retention expiry have explicit
cleanup behavior. Startup recovery removes incomplete staging and preserves
only committed artifacts allowed by the P2 retention contract. Cleanup is
idempotent and must not follow symlinks or delete outside a dedicated staging
root.

## Failure and recovery semantics

Gateway and companion return the same bounded categories:

- `denied_policy`;
- `stale_authority`;
- `unsupported`;
- `quota_exceeded`;
- `capture_failed`;
- `transfer_failed`;
- `digest_mismatch`;
- `timeout`;
- `canceled`;
- `worker_lost`;
- `outcome_unknown`; and
- `cleanup_required`.

Errors contain no path, page content, console text, network headers, artifact
bytes, driver response, or secret. A failure before driver acceptance is safe
to retry only when the returned action says so. A failure after acceptance or
an ambiguous driver outcome is never retried automatically. Artifact and
snapshot transfer recovery is permitted only when it cannot repeat a browser
action or recapture mutable page state.

## Acceptance evidence

Implementation is complete only when focused tool, schema, broker, spool,
node protocol, WebSocket, companion host, private driver, race, real-driver,
and production-WSS tests prove:

- page and semantic element screenshots produce bounded PNG artifacts on both
  placements with matching size, digest, ownership, expiry, and cleanup;
- upload and file chooser use only authorized retained input artifacts and
  have identical gateway and companion behavior;
- one bounded browser download is captured, transferred, retained, optionally
  delivered, and recoverable without replaying the page action;
- wrong owner, session, profile, ref, context, invocation, digest, size,
  content type, expiry, connection generation, or transfer sequence fails
  closed;
- cancellation, timeout, disconnect, partial transfer, backpressure, quota,
  restart, and concurrent close leave no partial committed artifact and no
  orphan node staging;
- console, failed-request, and crash summaries are useful but contain no raw
  console text, query values, headers, bodies, cookies, credentials, storage,
  stack traces, or driver internals;
- large source snapshots truncate deterministically, stream in multiple
  acknowledged chunks over production WSS, respect every byte and time
  budget, and cannot overflow the generic tool result;
- post-action snapshot transfer loss preserves the terminal action state and
  never makes the action replayable;
- capability discovery omits each feature when any mandatory prerequisite is
  absent or stale; and
- repeated close, immediate profile reuse, connection replacement, and
  retention cleanup leave zero phase-owned driver, lock, transfer, or staging
  orphans.

The live completion matrix runs on both gateway and companion targets:

```text
open -> observe large page -> page screenshot -> element screenshot
-> retained upload -> retained download -> diagnostics
-> close -> reopen -> close
```

The canary uses synthetic non-secret content and records only artifact
metadata, digests, bounded counts, truncation state, terminal outcomes, and
cleanup evidence.

## Implementation sequence

1. Add the private outbound artifact and streamed-observation transfer
   foundation, with no new model-visible capability advertised.
2. Add shared `browser_capture` page and element screenshot behavior and P2
   retention on both placements.
3. Complete companion `upload` and `download`, including partial-transfer,
   resume-without-action-replay, delivery, and cleanup semantics.
4. Add bounded session diagnostics with protected durable results and exact
   capability discovery.
5. Add large-page, multi-chunk, backpressure, timeout, race, real-driver, and
   production-WSS matrices; then enable the exact supported features.
6. Deploy the exact merged binaries, renew companion catalog authority, run
   equivalent live canaries, audit persistence and process cleanup, and record
   Phase 6 plus global completion evidence.

Each implementation slice is a focused autonomous pull request from the
latest merged `main`. A feature remains unadvertised until its complete
gateway and companion acceptance path is merged and deployable.

## Mandatory stop conditions

Stop the affected slice and keep its capability unadvertised if:

- implementation requires model-visible binary data, base64, host paths,
  selectors, raw Playwright tools, CDP, page code, headers, bodies, cookies,
  credentials, storage state, console text, or driver payloads;
- browser output cannot use the authenticated P2 transfer and spool boundary;
- an immutable captured output cannot be distinguished from rerunning the
  browser operation that created it;
- an accepted action can become replayable because observation or artifact
  delivery failed;
- large snapshots cannot be source-bounded before WSS serialization;
- transfer backpressure, timeout, cancellation, disconnect, restart, or
  partial commit can leak staging or publish an unverified artifact;
- diagnostics cannot provide useful bounded summaries without exposing
  sensitive raw content; or
- gateway and companion cannot expose the same model-visible semantics for an
  advertised capability.

PDF, HAR, Playwright trace, video, device emulation, locale, timezone,
geolocation, clipboard, browser permissions, headed handoff parity, and
privileged arbitrary Playwright execution remain separately admitted later
work. They are not required for the six-phase execution goal.
