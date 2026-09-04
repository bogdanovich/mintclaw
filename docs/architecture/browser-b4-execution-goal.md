# Browser B4 Execution Goal

## Status And Objective

Status: admitted, not started.

Implement Browser Identity and Attached-User Profiles end to end under
[Browser Capability B4 Admission](browser-capability-b4-admission.md). The goal
ends only after all six phases are merged, deployed, and live-validated on the
placements named by each phase, and global completion evidence is committed.

Implementation uses the MintClaw autonomous pull-request workflow. Each phase
is a focused pull request or the smallest coherent dependent sequence. Do not
start a dependent phase before its prerequisite is merged and production
evidence is sufficient to continue safely.

## Execution Progress

| Phase | Status | Required outcome |
| --- | --- | --- |
| 1. Profile authority and production cutover | Pending | Canonical multi-profile schema, private runtime mapping, safe discovery, and lossless migration of the deployed `managed` identity |
| 2. Managed alias and revocation parity | Pending | Arbitrary managed aliases, exact grants, revision-bound revocation, lease isolation, and gateway/companion conformance |
| 3. Ephemeral profiles | Pending | Fresh session-only identity with verified cleanup and quarantine semantics on gateway and companion |
| 4. Origin-bound credential injection | Pending | Host-local opaque credential grants using protected fill with zero plaintext persistence or cross-host transfer |
| 5. Gateway attached Chrome | Pending | Visible per-session Playwright-extension consent, selected-tab authority, detach, and gateway lifecycle evidence |
| 6. Companion attached Chrome and closeout | Pending | The same attached contract on the Darwin companion plus global B4 production evidence and roadmap closeout |

## Rules For Every Phase

- Begin from the latest `origin/main` in the dedicated autonomous worktree.
- Keep production, tests, documentation, deployment changes, and evidence
  limited to the current phase.
- Preserve the existing model-facing first-party browser tools. Never expose
  raw MCP tools, CDP, profile paths, cookies, storage state, or secrets.
- Keep gateway and companion schema and behavior shared. Placement-specific
  host configuration may differ only behind the common contract.
- Treat profile mode, revision, owner, actor, agent, target, catalog, session,
  document, policy, and lease as authority rather than metadata.
- Preserve explicit effect and approval modes, stale-reference rejection,
  no-blind-replay, artifact bounds, network policy, and safe errors.
- Add focused unit, integration, failure-injection, real-driver, persistence,
  privacy, and process-cleanup tests in proportion to each phase.
- Run targeted tests first, then the repository lint and test gates required by
  the touched shared packages.
- Deploy each merged phase to the current gateway and affected companion before
  relying on it as the next phase's baseline.
- Record exact deployed commit, binary versions, configuration revision, safe
  canary procedure, outcomes, and rollback location without recording secrets,
  private paths, cookies, or personal page data.
- Stop on any admission stop condition. Do not patch around a broken OS,
  browser, display, extension, or network dependency by weakening the contract.

## Phase 1: Profile Authority And Production Cutover

Replace the single hard-coded gateway `managed` profile assumption with the
canonical B4 profile schema and a private per-profile runtime mapping. Add safe
profile descriptors and validation for mode, revision, exact actor/agent grant,
storage ownership, lock ownership, lifecycle policy, and mutually exclusive
mode-specific settings.

The migration must preserve the current production profile directory and login
state. Use an additive reader or staged deployment only as long as required for
one safe cutover; remove the legacy ambiguity before the phase closes. The
driver server remains a base template, while trusted profile configuration owns
reserved identity and connector arguments.

Acceptance criteria:

- valid canonical managed configuration starts on gateway and companion;
- missing, duplicate, conflicting, ungranted, or path-unsafe configuration
  fails before the browser runtime is published;
- discovery shows only safe aliases and mode/capability facts granted to the
  current agent and actor;
- the deployed existing identity remains signed in after cutover without
  copying, deleting, or recreating its directory;
- rollback restores the previous binary and configuration against the same
  untouched profile data; and
- no transitional single-profile or driver-argument fallback remains after the
  production cutover is proven.

## Phase 2: Managed Alias And Revocation Parity

Allow multiple arbitrary managed aliases per target within configured limits.
Bind each worker and durable session to one exact alias and revision. Ensure
different aliases cannot share a resolved directory or lock and cannot inherit
worker, tab, snapshot, approval, or artifact authority.

Implement revocation through gateway generation replacement and companion
catalog/profile revision changes. Disable, revision change, actor removal,
agent removal, or runtime mapping change must prevent new work and close or
quarantine existing sessions before replacement authority is used.

Acceptance criteria:

- two managed aliases independently open, retain state, close, and immediately
  reuse their own storage on gateway and companion;
- cross-alias session IDs, refs, approvals, artifacts, and credentials fail
  before dispatch;
- concurrent opens for one profile are busy without poisoning another
  profile's readiness; global session-capacity exhaustion remains a distinct
  bounded result, and the other profile opens after capacity is released;
- every revocation cause has success, cleanup-failure, restart, and stale
  catalog tests; and
- real smoke evidence proves alias isolation and revocation on both placements.

## Phase 3: Ephemeral Profiles

Add `ephemeral` as a shared profile mode. Create a unique private runtime root
for one session, launch an isolated context without importing managed state,
and bind cleanup to every terminal and recovery path. Retained downloads and
screenshots continue to use the existing artifact store and are the only
allowed outputs surviving browser cleanup.

Acceptance criteria:

- consecutive ephemeral sessions cannot observe one another's cookies, local
  storage, service-worker state, cache markers, or files;
- success, open failure, timeout, cancellation, driver crash, session expiry,
  config reload, companion disconnect, gateway restart, and node restart all
  remove the session root;
- injected cleanup failures produce `cleanup_required`, quarantine reuse, and
  retain bounded operator recovery guidance;
- symlink swaps, ownership changes, permissive permissions, path overlap, and
  cleanup outside the configured root fail closed; and
- gateway and companion real-process smoke tests prove clean start, cleanup,
  immediate reuse, and no orphan browser or driver process.

## Phase 4: Origin-Bound Credential Injection

Add opaque credential grants and typed `credential_fill`. Use the existing
secure-string resolver with owner-only `file://` and `enc://` values; reject
inline plaintext. Resolve secrets only on the execution host after current
origin, profile grant, field, writable element, document identity, and policy
checks. Reuse protected-fill ephemeral delivery and durable redaction.

Acceptance criteria:

- exact granted origin and field fill successfully on gateway and companion;
- wrong origin, redirect, missing or ungranted alias, wrong field, stale ref,
  non-writable element, revoked profile, resolver failure, and policy denial
  dispatch no secret bytes;
- gateway credentials never cross the node transport and companion credentials
  never cross back to the gateway;
- high-entropy canaries are absent from config JSON, browser store, agent
  history, traces, logs, approvals, events, node plans, node ledgers, artifacts,
  safe errors, and post-cleanup process arguments and environments;
- crash, disconnect, cancellation, timeout, and restart cannot replay or
  reconstruct a credential fill; and
- live synthetic-origin smoke tests prove success and origin denial on each
  placement without using personal credentials.

## Phase 5: Gateway Attached Chrome

Add `attached_user` on the gateway with the official Playwright extension
connector. Opening requires one expiring owner-routed consent and visible
browser connection/tab selection. The selected tab becomes the sole initial
context authority. Close detaches without closing Chrome or unrelated tabs.

Attached action-origin policy must be honest: it checks the selected top-level
document and declared navigation destination but does not advertise managed
request-proxy enforcement over the user-owned browser.

Acceptance criteria:

- no attached session becomes ready before authenticated consent and visible
  tab selection;
- deny, expiry, cancellation, reload, restart, extension disconnect, and user
  tab close revoke the one-use request and release every MintClaw resource;
- the model cannot enumerate unattached tabs, browser profiles, endpoints,
  cookies, storage state, installed extensions, or connection credentials;
- exact-origin and explicit `any_http` action modes behave as configured;
- concurrent human page mutation produces stale or unknown authority and never
  a blind replay;
- detach preserves the Chrome process and unrelated tabs; and
- a non-sensitive real tab completes consent, observe, one reversible action,
  close, immediate reattach, and process/lock audit.

## Phase 6: Companion Attached Chrome And Closeout

Extend the same attached-user descriptor, consent, controller, origin,
freshness, detach, and safe-error contract to the admitted Darwin companion.
Connector executable, extension state, native browser identity, locks, and any
secret material remain companion-local. Gateway routing carries only typed
session and consent authority.

Acceptance criteria:

- the gateway cannot select a native companion browser profile, tab ID,
  endpoint, extension, or process argument;
- stale node catalogs, profile revisions, disconnects, reconnects, driver loss,
  companion restart, and gateway restart revoke or quarantine the attach
  session without gateway fallback;
- consent is routed to the authenticated owner and cannot be replayed for
  another node, target, profile, actor, agent, or session;
- first-party discovery, observe, contexts, actions, artifacts, diagnostics,
  close, and safe errors match gateway behavior for advertised features;
- a non-sensitive companion Chrome tab completes visible consent, selection,
  observe, reversible action, detach, immediate reuse, and lock/process audit;
  and
- global evidence proves every requirement in the B4 admission, updates the
  roadmap status to complete, and records any separately deferred follow-up.

## Global Completion And Stop Rule

Do not mark the goal complete after a partial profile mode, a green test suite,
or a successful gateway-only demonstration. Completion requires all six phases,
the B4 global acceptance evidence, merged closeout documentation, deployed
gateway and companion versions, and no unresolved review or production defect
within the admitted scope.

If any mandatory stop condition in the B4 admission is reached, stop the
affected implementation before broadening authority. Record the exact evidence
and request a new architecture decision rather than silently weakening
identity, credential, consent, revocation, or cleanup guarantees.
