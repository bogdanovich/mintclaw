# Browser Functional Parity Execution Goal

## Status And Goal

This document is the governing execution goal for completing the next six
browser-capability phases after shared scroll parity. It is intentionally
larger than one pull request. Each phase must be admitted, implemented,
reviewed, merged, deployed, and live-validated before dependent work begins.

The goal is complete only when all six phases and the global acceptance
criteria in this document are satisfied on both gateway-hosted and
companion-hosted browser sessions. Completing an individual phase or merging
an intermediate pull request does not complete the goal.

The implementation must extend the shared first-party browser contract. Raw
Playwright MCP tools, CSS selectors, CDP endpoints, arbitrary page code, and
placement-specific model-visible APIs remain outside the ordinary browser
tool surface.

## Execution Rules

1. Deliver the phases in order unless a separately merged foundation is
   required by more than one phase.
2. Give each phase its own focused admission and implementation pull requests;
   split a phase further when that produces safer review units.
3. Start each dependent pull request from the latest merged `main` and carry
   it through the autonomous review, CI, approval, merge, deployment, and live
   validation workflow.
4. Keep gateway and companion semantics in one typed contract. Placement may
   change private transport and capability availability, but not the meaning
   of an advertised action or result.
5. Make `browser_targets` advertise only the exact features supported by the
   selected target and profile in one consistent runtime generation.
6. Preserve fresh session, document, tab, frame, snapshot, policy, profile,
   and catalog authority wherever each action depends on them.
7. Derive effects and approval requirements in trusted code. Never accept an
   effect classification, approval bypass, raw selector, profile path, or
   driver endpoint from model arguments.
8. Never blindly replay an accepted action after an ambiguous outcome.
9. Keep secrets, form values, cookies, storage state, credentials, and raw
   driver payloads out of durable ledgers, logs, traces, and model-visible
   errors.
10. After every deployed phase, run equivalent real-browser canaries on
    gateway and companion targets and verify session cleanup, immediate
    profile reuse, and zero phase-owned orphan processes.

## Phase 1: Shared Click Parity

Add a typed semantic `click` action to the shared gateway and companion
contract.

### Acceptance Criteria

- `click` accepts only a fresh broker-issued semantic element reference bound
  to the current session, tab, frame, snapshot generation, origin, policy,
  profile, and catalog authority.
- The model cannot provide a CSS/XPath selector, coordinate, JavaScript
  expression, driver command, or effect classification.
- Trusted code distinguishes read-like interaction from a potential external
  commit and requires exact approval when policy requires it.
- Success returns a fresh bounded observation and an explicit outcome for
  same-document change, navigation, popup, dialog, page close, or no change.
- Stale, detached, hidden, covered, disabled, ambiguous, disconnected, and
  unsupported targets return equivalent bounded safe errors on both
  placements.
- Unit, contract, race, real-driver, production-WSS, and no-blind-replay tests
  cover success and failure paths.
- A controlled live canary completes `open -> observe -> click -> observe ->
  close -> reopen -> close` on gateway and companion targets.

## Phase 2: Shared Press And Select Parity

Add typed keyboard and option-selection interactions without broadening the
driver boundary.

### Acceptance Criteria

- `press` uses a bounded normalized key/chord contract and a fresh semantic
  target or an explicitly admitted document target.
- `select` addresses a fresh semantic select control and bounded option
  identities; it does not accept arbitrary selectors or page code.
- The broker derives effects, approvals, navigation expectations, and
  document invalidation rules for both actions.
- Keyboard input cannot invoke privileged OS shortcuts or escape the browser
  document boundary.
- Success, stale reference, invalid option, disabled control, navigation,
  dialog, popup, timeout, disconnect, and ambiguous-result behavior is
  equivalent on gateway and companion targets.
- Focused tests and production-WSS tests cover both actions, including
  concurrency with close, expiry, and reconnect.
- Controlled live canaries execute `press` and `select`, observe fresh state,
  close, and immediately reuse the profile on both placements.

## Phase 3: Tabs, Frames, And Popups

Add bounded discovery and selection of browser document contexts so later
actions can target the correct page and frame without raw driver handles.
The implementation authority and stop conditions are fixed in
[Browser Capability BF1 Tabs, Frames, And Popups Admission](browser-capability-bf1-contexts-admission.md).

### Acceptance Criteria

- The first-party contract exposes opaque broker-owned tab, popup, and frame
  identities with bounded metadata and deterministic ordering.
- Every identity is scoped to one owner, session, profile, runtime generation,
  and current document generation and cannot cross sessions or placements.
- Open, close, navigation, popup creation, frame attach/detach, and selection
  have explicit state transitions and safe terminal outcomes.
- Popup correlation is bound to the initiating action; an unrelated page
  cannot be claimed as its result.
- Stale or detached contexts fail closed, and reconnect never revives stale
  authority or silently falls back to another target.
- Contract, lifecycle, race, isolation, real-driver, and production-WSS tests
  cover multiple tabs, nested frames, popups, close, disconnect, and cleanup.
- A live multi-document canary discovers and selects a tab or popup and an
  iframe, performs a read-only observation, and closes all owned contexts on
  gateway and companion targets.

## Phase 4: Protected Fill Parity

Add typed form filling only after form values can cross the companion boundary
without entering durable invocation records or diagnostic output.
The implementation authority and stop conditions are fixed in
[Browser Capability BF1 Protected Fill Parity Admission](browser-capability-bf1-protected-fill-admission.md).

### Acceptance Criteria

- `fill` accepts a fresh semantic editable-control reference and a bounded
  value through an ephemeral or equivalently protected delivery path.
- Plaintext values do not appear in the companion invocation ledger, gateway
  recovery state, events, traces, logs, errors, approval digests, or retained
  model-visible artifacts.
- The durable record retains only the minimum redacted metadata needed for
  ownership, recovery, audit, and no-replay behavior.
- The broker derives effect and approval policy, invalidates stale document
  state, and never retries an accepted fill blindly after uncertainty.
- Password, payment, one-time-code, and other operator-designated sensitive
  fields fail closed unless a separately admitted credential policy permits
  them.
- Redaction, crash, disconnect, timeout, stale reference, maximum-size,
  cancellation, and concurrency tests run on both local and production-WSS
  paths.
- A synthetic non-secret form is filled and observed successfully on gateway
  and companion targets, followed by durable-store and trace inspection that
  proves the value was not retained.

## Phase 5: Remaining Ordinary Interaction Parity

Add typed `dialog`, `check`, `uncheck`, `hover`, `drag-and-drop`, and
`file-chooser` behavior to the shared contract.

### Acceptance Criteria

- Every action uses fresh semantic references and the same advertised input,
  result, effect, approval, freshness, and safe-error semantics on every
  supporting placement.
- Dialog handling has explicit accept, dismiss, and bounded prompt-value
  behavior; prompt values use the protected value path from Phase 4.
- Check and uncheck verify the target control type and requested final state
  instead of blindly toggling it.
- Hover is bounded to a semantic target and reports any resulting document,
  popup, or visibility change without coordinate input.
- Drag-and-drop binds fresh source and destination references, has explicit
  completion evidence, and cannot inject an arbitrary data-transfer payload.
- File chooser accepts only broker-owned retained input artifacts authorized
  for the current actor, session, profile, target, and destination control;
  raw host paths never enter model arguments.
- Capability discovery omits an action when its protected-value or artifact
  prerequisite is unavailable on the selected target.
- Focused security, artifact-ownership, lifecycle, race, real-driver, and
  production-WSS tests cover every action and terminal failure mode.
- A controlled live test matrix exercises all six actions on gateway and
  companion targets and proves cleanup and immediate profile reuse.

## Phase 6: BF2 Media, Transfer, Diagnostics, And Snapshot Delivery

Complete the selected BF2 parity slice for screenshots, browser upload and
download flows, bounded diagnostics, and large semantic snapshots.

### Acceptance Criteria

- Page and element screenshots use retained artifact references with bounded
  size, media type, digest, ownership, expiry, cleanup, and redaction rules.
- Uploads use authorized broker-owned input artifacts and downloads produce
  broker-owned output artifacts; neither operation exposes raw host paths or
  model-visible binary payloads.
- Gateway and companion targets provide equivalent explicit success,
  rejection, cancellation, timeout, disconnect, partial-transfer, digest,
  quota, and cleanup outcomes.
- Bounded redacted console errors, failed-request summaries, and page-crash
  diagnostics are available only when advertised and do not expose secrets,
  cookies, headers, response bodies, storage state, or driver internals.
- Semantic snapshots are bounded at the source. Production WSS delivery has
  explicit truncation, chunking, backpressure, and timeout budgets so a large
  page cannot turn a successfully completed action into an unknown outcome
  solely because its observation is large.
- Artifact and diagnostic capability limits are reported through
  `browser_targets` before session start and revalidated at use time.
- Unit, contract, quota, retention, cleanup, race, real-driver, and
  production-WSS tests cover large pages and large allowed artifacts without
  unbounded memory or ledger growth.
- Live gateway and companion canaries capture a screenshot, upload a
  synthetic artifact, download a synthetic artifact, retrieve bounded
  diagnostics, and complete an action on a large page without outcome loss.

## Global Completion Criteria

This execution goal may be marked complete only when all of the following are
true:

- every acceptance criterion in Phases 1 through 6 is satisfied;
- all admission, foundation, implementation, regression-fix, and deployment-
  evidence pull requests required by the six phases are merged;
- the exact merged binaries are deployed to the production gateway and the
  configured companion, and both report the expected version and approved
  capability catalog;
- gateway and companion return the same contract, effect, approval, freshness,
  recovery, artifact, and safe-error semantics for every commonly advertised
  feature;
- a real first-party-only workflow exercises multiple pages or frames, form
  interaction, screenshot evidence, and upload or download on each placement;
- production traces, ledgers, logs, artifact stores, locks, and process tables
  pass the privacy, boundedness, cleanup, and zero-orphan audit;
- accepted ambiguous operations remain no-replay, terminal worker loss is
  reconciled immediately, and completed, blocked, and failed turns release
  their browser sessions and profiles; and
- the browser roadmap, architecture index, operator guidance, and deployment
  evidence describe the final shipped behavior and any explicitly deferred
  work.

Do not mark the goal complete after a partial phase, a green test suite, a
merged but undeployed pull request, or validation on only one placement. If a
phase exposes a prerequisite defect, fix and validate that defect through a
focused pull request before resuming the phase. External blockers are handled
by the autonomous workflow, but they do not reduce these completion criteria.
