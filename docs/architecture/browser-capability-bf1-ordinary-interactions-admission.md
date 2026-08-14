# Browser Capability BF1 Remaining Ordinary Interactions Admission

Status: admitted for implementation

This slice completes the ordinary first-party browser action surface shared by
gateway-hosted and companion-hosted sessions. It adds typed dialog handling,
explicit check and uncheck operations, semantic hover, bounded drag-and-drop,
and broker-authorized file chooser selection. It reuses the existing browser
broker, context catalog, fresh semantic references, private Playwright adapter,
protected-value transport, artifact stores, companion command boundary, and
terminal no-replay rules.

The slice does not expose raw Playwright tools, selectors, coordinates,
JavaScript, arbitrary data-transfer payloads, browser endpoints, host paths,
cookies, storage state, or credentials. Coordinate input and privileged raw
browser execution remain separate capabilities.

## Admitted action contract

`browser_act` may accept exactly one of these additional action shapes:

| Kind | Model-visible arguments | Trusted effect |
|---|---|---|
| `dialog` | `dialog_id`, `decision: accept|dismiss`, optional protected `value` | `external_commit` for accept, `read` for dismiss |
| `check` | fresh semantic `ref` | `local_edit` |
| `uncheck` | fresh semantic `ref` | `local_edit` |
| `hover` | fresh semantic `ref` | `read` |
| `drag` | fresh `source_ref` and `destination_ref` from one snapshot | `unknown` |
| `file_chooser` | fresh semantic `ref` and broker-owned `artifact_ref` | `unknown` |

The model cannot provide an effect, approval decision, current state, element
role, selector, coordinate, file path, MIME override, digest, size, origin,
profile, driver command, or data-transfer payload. Trusted code derives those
facts from current broker, artifact, profile, and private driver state.

Actions with `external_commit` or `unknown` effect require the existing exact
one-time approval and remain unavailable in dry-run sessions. `check`,
`uncheck`, and `hover` do not inherit an approval from a later submit, click,
drop, or navigation. Each later action retains its own independently derived
effect and approval boundary.

Every successful action returns the existing invocation identity, trusted
effect, terminal state, and a fresh bounded observation. The result also
contains only the bounded action-specific evidence needed to distinguish
`no_change`, `same_document_change`, `navigation`, `popup_opened`,
`dialog_pending`, `page_closed`, or `completed`. It never returns private DOM
attributes, selectors, host paths, driver payloads, or protected values.

## Dialog authority and protected prompt values

An observation containing a pending JavaScript alert, confirm, prompt, or
before-unload dialog mints an opaque `dialog_id`. The identifier is bound to
the owner, actor, session, target, profile, tab, frame, document and snapshot
generation, dialog type, bounded message digest, policy revision, catalog
revision, and current controller generation. A dialog action must use that
exact identifier while the same dialog remains pending.

`dismiss` never accepts a value. `accept` accepts a value only for a prompt;
alert, confirm, and before-unload acceptance reject one. Prompt text is
non-empty or empty according to the page contract, valid UTF-8, and bounded by
`text_input_bytes`. It uses the Phase 4 protected-value lifecycle:

- durable tool arguments contain no plaintext;
- the prepared action and companion plan retain only a domain-separated
  digest and byte length;
- the initial companion dispatch carries the value once in `EphemeralInput`;
- ledgers retain only a terminal receipt without the prompt value or the
  resulting page observation; and
- restart, reconnect, or recovery without the live value fails closed and
  never replays an accepted dialog action.

The private host revalidates the exact pending dialog immediately before
dispatch. A replaced or already handled dialog fails stale. Dialog acceptance
is conservatively an external commit because page code or browser navigation
may continue immediately. Dismissal is read-like but still invalidates the
snapshot and returns a fresh observation.

## Check and uncheck final-state semantics

`check` and `uncheck` accept one fresh semantic reference. Trusted code
revalidates a checkbox, radio button, switch, or equivalent native checkable
control and reads its current checked state privately.

- `check` requests a final checked state.
- `uncheck` requests a final unchecked state.
- Unchecking a radio control is denied because the browser cannot establish
  that state through a direct semantic uncheck operation.
- An already satisfied final state succeeds as `no_change` without issuing a
  toggle input.
- Otherwise the adapter uses the fixed typed check or uncheck primitive and
  verifies the requested final state in the post-action observation.

The implementation never models these operations as blind clicks or toggles.
Hidden, disabled, read-only, detached, covered, ambiguous, non-checkable, or
state-changing controls fail closed with placement-equivalent safe errors.

## Hover semantics

`hover` accepts one fresh semantic reference and uses Playwright actionability
checks without a coordinate or force option. It is a read-like interaction,
but it invalidates snapshot authority because page script may reveal content,
open a popup, create a dialog, navigate, detach the target, or close the page.

Success returns a fresh observation and bounded outcome classification. A
navigation or popup produced by hover is admitted only when it passes the same
origin, network, context-correlation, and context-limit checks as every other
browser action. The adapter never falls back to mouse coordinates when
semantic hover fails.

## Drag-and-drop authority

`drag` accepts exactly two distinct fresh semantic references minted by the
same observation and selected document context. The broker binds source and
destination roles and accessible names into one prepared action. The private
host re-observes both elements, requires each private target to occur exactly
once, and performs the final navigation-identity check immediately before the
fixed typed drag-and-drop operation.

The model cannot provide drag coordinates, steps, duration, buttons,
modifiers, MIME types, files, or a custom `DataTransfer`. Trusted code supplies
no arbitrary payload. Because a drop commonly commits application state,
`drag` has unknown effect and requires exact approval. A successful result
requires post-action evidence that the operation completed without driver
ambiguity; navigation, popup, dialog, page close, source or destination
detachment, timeout, or transport loss receives the normal explicit terminal
outcome. An accepted ambiguous drag quarantines the session and is never
replayed.

## File chooser and artifact authority

`file_chooser` selects one retained input artifact on one fresh semantic file
input or chooser control. The model supplies only an opaque `artifact_ref`.
Before preparation, the gateway resolves the artifact through the existing
artifact boundary and binds:

- owner, actor, routed session, workspace, and tool-call identity;
- browser session, target, profile, tab, frame, snapshot, and control;
- immutable size, SHA-256 digest, filename, content type, expiry, and cleanup
  policy; and
- the exact gateway or companion destination placement.

Raw host paths never enter model arguments, browser records, companion plans,
approval prompts, traces, or model-visible results. The private local adapter
may receive a short-lived broker path only after authorization. A companion
artifact is transferred through the existing bounded spool before browser
action acceptance, verified against the bound digest and size on the node,
and exposed to the private driver only for the accepted chooser operation.

The control is revalidated as a file chooser immediately before dispatch.
The fixed operation selects exactly one artifact; directory selection,
multiple-file selection, path injection, symlink escape, and driver-managed
file discovery are not admitted in this slice. File selection has unknown
effect because a page may start a network upload from its input event, so it
requires exact approval and is denied in dry-run mode.

Transfer completion alone does not prove browser action completion. A failed
or stale chooser releases the staged node artifact. Successful selection
retains only bounded artifact metadata and a terminal receipt. Session close,
expiry, cancellation, quarantine, node disconnect, and retention expiry clean
both gateway and node staging. Recovery never replays an accepted chooser.

Phase 5 owns semantic chooser selection. Phase 6 owns the broader browser
upload/download delivery experience, artifact reporting, quotas, diagnostics,
and transfer parity beyond this single retained-input action.

## Shared freshness and no-replay boundary

Immediately before any admitted action reaches the driver, the broker or
companion host revalidates:

- routed owner, actor, and agent;
- session lease, controller generation, target, and profile;
- selected tab and frame plus context-catalog generation;
- driver-owned document identity and navigation generation;
- fresh snapshot and every semantic element or dialog reference;
- current origin and network policy;
- browser policy, profile, approved catalog, and capability revisions; and
- action-specific control state, dialog state, or artifact authority.

Any mismatch before dispatch fails stale or denied with zero action input.
The final private navigation check is the authority linearization point.
Browser events beginning after it are concurrent outcomes, not conditions the
contract claims to reject before input. Once an action is durably accepted,
timeout, cancellation, process loss, WSS loss, or an ambiguous driver result
produces a terminal unknown outcome and worker quarantine rather than replay.

## Capability discovery

`browser_targets` advertises only actions supported by the selected target and
profile in one runtime generation:

- `dialog` requires typed dialog handling and the complete protected prompt
  path;
- `check` and `uncheck` require typed final-state verification;
- `hover` requires semantic hover without coordinate fallback;
- `drag` requires typed source/destination drag-and-drop and exact approval;
  and
- `file_chooser` requires artifact resolution, bounded placement transfer,
  private chooser support, cleanup, and exact approval.

The gateway intersects local policy with the approved companion catalog. A
missing protected-value, artifact, transfer, driver, platform, profile, or
approval prerequisite omits the affected action instead of advertising it and
failing after arguments are supplied. Gateway and companion expose the same
model-visible action shapes, trusted effects, freshness, terminal outcomes,
and safe errors whenever an action is advertised.

## Acceptance evidence

Implementation is complete only when focused tool, schema, broker, artifact,
transport, runtime, host, real-driver, race, and production-WSS tests prove:

- dialog accept and dismiss consume only the exact current dialog authority;
- prompt values use the protected path and are absent from every durable
  gateway, companion, agent, trace, log, error, approval, and artifact record;
- check and uncheck reach the requested final state, return `no_change` when
  already satisfied, and never dispatch a blind toggle;
- hover reports bounded same-document, navigation, popup, dialog, page-close,
  and no-change outcomes without coordinate fallback;
- drag binds two fresh references, requires approval, supplies no arbitrary
  payload, and fails closed on either stale or ambiguous endpoint;
- file chooser accepts only an authorized retained artifact, rejects raw or
  cross-owner paths and references, verifies digest and size on the destination,
  and cleans staging on every terminal path;
- action preparation races with navigation, context mutation, close, expiry,
  policy reload, catalog change, disconnect, and cancellation remain
  placement-equivalent and no-replay;
- capability discovery omits each action when any mandatory prerequisite is
  absent; and
- a controlled live matrix exercises all six actions on gateway and companion
  targets, observes fresh state, closes every session, immediately reuses each
  profile, and finds no orphan driver or staged artifact.

Tests use synthetic non-secret prompt text and artifacts with unique canaries.
Persistence scans inspect raw state files and bounded logs, not only decoded
views. Production evidence records only fixed redaction assertions and
artifact digests, never protected prompt values or raw artifact content.

## Implementation sequence

1. Extend shared action types, schemas, results, discovery, and private
   Playwright primitives while keeping new actions unadvertised.
2. Add dialog protected-value transport and companion host parity, then prove
   no-retention and no-replay behavior.
3. Add check, uncheck, and hover final-state and outcome parity.
4. Add two-reference drag authority, approval, and completion evidence.
5. Bind file chooser to the existing artifact and node transfer boundaries,
   including destination verification and cleanup.
6. Add real-driver and production-WSS matrices, enable the exact profile
   actions, renew companion catalog approval, deploy, and run equivalent live
   canaries on both placements.

The implementation may use multiple focused dependent pull requests, but
Phase 5 is not complete until all six actions are merged, deployed, advertised,
and live-validated together.

## Mandatory stop conditions

Stop implementation and keep the affected action unadvertised if:

- a prompt value, raw path, artifact content, selector, driver payload, or
  private DOM metadata must enter durable or diagnostic state;
- check or uncheck can only be implemented as a blind toggle;
- hover or drag requires model-provided coordinates or arbitrary payloads;
- file chooser cannot prove artifact ownership, immutable digest and size,
  destination placement, or bounded cleanup before action acceptance;
- companion recovery requires retaining or replaying protected or staged
  input after acceptance;
- a driver error can echo protected input or a host path; or
- gateway and companion cannot expose equivalent semantics for an advertised
  action.

Screenshots, download delivery, bounded diagnostics, and large semantic
snapshot transport remain Phase 6 work under the
[Browser Functional Parity Execution Goal](browser-functional-parity-execution-goal.md).
