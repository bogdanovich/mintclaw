# Browser Capability Continuation Execution Goal

## Status And Objective

Status: active. Phases 0 through 2 are complete; Phase 3 is the next
dependency-ordered phase.

Continue the deployed first-party browser program with the smallest practical
set of capabilities that improve owner-operated automation:

1. a stable driver/provider seam and a reusable real-process smoke runner;
2. a direct Playwright-library driver;
3. opt-in privileged Playwright execution;
4. one self-service cloud provider, Steel;
5. one versioned repeatable workflow recipe;
6. on-demand HAR, trace, and video artifacts;
7. browser environment controls and browser-scoped clipboard access;
8. screenshot-bound browser-viewport coordinate input; and
9. explicit remote-workspace routing plus global completion evidence.

The program follows the MintClaw autonomous pull-request workflow. Each phase
is one focused pull request or the smallest coherent dependent sequence. A
phase starts from the latest `origin/main` only after its prerequisite is
merged, deployed where applicable, and supported by sufficient production
evidence.

This goal deliberately does not complete every speculative browser feature.
Attached-user browser control, credential injection, managed Playwright
runtime distribution, a second cloud provider, and arbitrary desktop control
remain deferred under the boundaries below.

## Product Decisions

- The model continues to see MintClaw first-party browser tools, never raw MCP,
  CDP, provider, or driver administration.
- Driver and provider are separate concepts. A driver performs browser
  operations; a provider creates, connects, persists, and releases a browser
  runtime.
- The initial direct driver uses the official Playwright Node.js library. It
  does not rewrite browser automation in Go.
- Steel is the only cloud provider selected for this goal. Cloudflare Browser
  Run remains the next conformance candidate, not a second implementation
  requirement.
- Privileged execution grants broad browser authority, not host authority. It
  receives a scoped Playwright browser object and cannot access arbitrary
  processes, files, environment values, MintClaw credentials, or node
  credentials.
- Confirmation remains configuration-driven. Privileged execution does not
  hard-code approval for every call; it reuses the effective profile approval
  mode, including `none`, `model_requested`, `always_commit`, and `policy`.
- Persistent local and cloud profiles use visible human login and retain
  browser state. The goal does not implement password, OTP, cookie, or secret
  injection.
- Each phase proves one concrete vertical slice and adds only the abstraction
  needed by that slice. No generic plugin framework, provider marketplace,
  recipe language, or desktop-control framework is admitted preemptively.

## Execution Progress

| Phase | Status | Required outcome |
| --- | --- | --- |
| 0. B4 closeout and smoke baseline | [Complete](../operations/browser-continuation-phase0-evidence.md) | Disable the unused attached profile, close the superseded B4 plan, and add the real-process smoke runner against existing managed and ephemeral profiles |
| 1. Driver/provider seam and conformance | [Complete](../operations/browser-continuation-phase1-evidence.md) | Separate runtime provisioning from browser control without changing the current Playwright MCP behavior |
| 2. Direct Playwright-library driver | [Complete](../operations/browser-continuation-phase2-evidence.md) | Run the existing first-party contract through a small MintClaw-owned Playwright sidecar on gateway and companion |
| 3. Privileged browser execution | Next | Expose an opt-in `browser_execute` escape hatch with bounded browser authority and configurable approval |
| 4. Steel cloud provider | Pending | Provision, drive, view, persist, and release a billable Steel browser through the same first-party contract |
| 5. Repeatable workflow recipe | Pending | Ship one versioned listing workflow that validates current page state and falls back safely when stale |
| 6. HAR, trace, and video artifacts | Pending | Capture bounded on-demand diagnostic artifacts with retention, redaction, and cross-session isolation |
| 7. Environment and clipboard controls | Pending | Add typed geolocation, locale, timezone, viewport/device, permission, and browser-scoped clipboard behavior |
| 8. Browser coordinate fallback | Pending | Bind one viewport action to an exact fresh screenshot frame without granting desktop control |
| 9. Workspace routing and closeout | Pending | Keep browser, artifacts, and compatible workspace operations on one explicit target and record global production evidence |

## Shared Rules For Every Phase

- Preserve session ownership, target/profile authority, document freshness,
  policy revision, approval binding, durable receipts, and no-blind-replay
  behavior.
- A gateway, companion, or cloud failure never causes silent fallback to a
  different placement, provider, profile, or driver.
- New safe capability facts appear in `browser_targets` before the model can
  invoke the feature. Unsupported features are omitted rather than failing
  only after invocation.
- Provider keys, connection URLs, native session IDs, profile IDs, live-view
  credentials, local paths, and driver arguments remain private runtime data.
- Browser results return bounded structured values or retained artifact
  references. Large or binary results never enter model JSON directly.
- Every runtime with persistent identity has one writer and explicit cleanup,
  quarantine, restart, and revocation behavior.
- New billable behavior is disabled by default and has operator-owned session,
  concurrency, and lifecycle limits.
- Tests include success, denial, timeout, cancellation, stale authority,
  disconnect, restart, cleanup failure, privacy, and unsupported-capability
  paths in proportion to the feature.
- Production evidence records exact merged revisions, safe configuration
  facts, commands, outcomes, cleanup, and rollback without storing personal
  page content or secrets.

## Canonical Smoke Runner

Phase 0 adds `scripts/browser-capability-smoke.sh` as the operator-facing entry
point for deterministic browser validation. Later phases extend the same
runner; they do not create unrelated one-off scripts as their only evidence.

The stable command shape is:

```text
scripts/browser-capability-smoke.sh \
  --target <gateway|companion|cloud> \
  --profile <safe-alias> \
  --suite <suite-name> \
  --json-output <path>
```

The runner may use a Go helper and dedicated fixtures internally. Its public
contract must:

- invoke the same first-party broker and worker boundaries used by the browser
  specialist instead of calling a private driver API directly;
- use deterministic local or companion-reachable fixtures by default;
- never perform a real external commit unless an operator supplies a separate
  explicit production-canary opt-in;
- require `--allow-billable` for a cloud session and use configured provider
  secrets rather than accepting them as command-line arguments;
- emit one bounded JSON document containing suite, target, safe profile alias,
  effective capabilities, ordered checks, cleanup state, process audit,
  artifact metadata, duration, and safe error;
- omit page text, form values, credentials, cookies, storage state, private
  paths, provider endpoints, native IDs, and secret-bearing URLs;
- close or release every browser, driver, fixture, artifact lease, and cloud
  session on success, failure, signal, and timeout; and
- exit non-zero unless every required check and cleanup assertion passes.

The suite registry grows in this order:

| Phase | Suite | Required placements |
| --- | --- | --- |
| 0 | `core`, `managed-reuse`, `ephemeral-cleanup` | Gateway and companion |
| 1 | `driver-conformance`, `provider-lifecycle` | Gateway and companion |
| 2 | `playwright-library` | Gateway and companion |
| 3 | `privileged-execute` | Gateway and companion |
| 4 | `steel-cloud`, `steel-profile-reuse`, `steel-handoff` | Cloud |
| 5 | `recipe-listing` | Gateway and cloud; companion when advertised |
| 6 | `har`, `trace`, `video` | Every placement advertising each artifact |
| 7 | `environment`, `clipboard` | Gateway and companion; cloud when advertised |
| 8 | `viewport-coordinate`, `stale-frame` | Gateway and companion; cloud when advertised |
| 9 | `workspace-routing`, `global` | Gateway and the admitted companion workspace |

CI runs non-billable fixture suites. Billable cloud smoke runs only after a
merged deployment with explicit operator configuration. A feature is not
complete merely because its driver-level test passes.

## Phase 0: B4 Closeout And Smoke Baseline

Record the owner decision that managed and ephemeral profiles are the active
identity modes for this deployment. Disable the experimental attached profile
in production discovery without deleting the dormant attached implementation.
The official Playwright browser extension is not installed as part of this
goal.

Add the canonical smoke runner with existing first-party flows:

- `core`: target discovery, open, observe, one reversible action, fresh
  observe, and close;
- `managed-reuse`: retain a harmless fixture marker across consecutive
  sessions owned by the same managed profile; and
- `ephemeral-cleanup`: prove that cookies, local storage, cache, service
  workers, and runtime files do not survive a new ephemeral session.

Acceptance criteria:

- `browser_targets` no longer advertises the unused attached profile in the
  current gateway deployment;
- existing managed and ephemeral profiles remain unchanged and ready on
  gateway and companion;
- every baseline suite uses first-party runtime boundaries and emits the
  documented JSON contract;
- a forced failure and signal still close sessions and leave no driver,
  browser, fixture, or profile lock orphan; and
- B4 documents describe Phases 1 through 3 as completed and attached-user work
  as deferred rather than active or falsely completed.

## Phase 1: Driver/Provider Seam And Conformance

Extract only the interfaces required to separate two existing responsibilities:

- a provider resolves trusted profile configuration and creates or connects to
  a browser runtime; and
- a driver maps MintClaw worker operations to that runtime.

The current local Playwright MCP path remains the production implementation.
No model-facing schema or behavior changes in this phase.

Acceptance criteria:

- local gateway and companion startup use an explicit local provider plus the
  existing Playwright MCP driver;
- provider lifecycle and driver command behavior have independent fakes and
  conformance suites without duplicating the browser session state machine;
- opaque runtime handles cannot be serialized into model, audit, invocation,
  node, or ordinary durable payloads;
- provider creation failure, driver attach failure, partial startup, close
  failure, crash, restart, and configuration replacement have one defined
  ownership and cleanup path; and
- production managed and ephemeral smoke results match the Phase 0 baseline.

Stop and redesign this phase if the seam requires a generic plugin registry or
duplicates target, profile, session, receipt, or recovery authority.

## Phase 2: Direct Playwright-Library Driver

Status: complete. The merged implementation and gateway and companion evidence
are recorded in
[Browser Continuation Phase 2 Evidence](../operations/browser-continuation-phase2-evidence.md).

Add a small MintClaw-owned Node.js sidecar using the pinned official Playwright
library. The sidecar implements the private typed worker protocol; it does not
become an MCP server and is never exposed to the model.

The Playwright MCP driver remains available for rollback until the direct
driver passes conformance and production canaries. Managed runtime distribution
is not part of this phase: ordinary install/update tooling may install the
pinned Node dependency.

Acceptance criteria:

- the sidecar supports every currently advertised first-party session,
  observe, context, action, artifact, diagnostic, and handoff operation;
- gateway and companion pass the same conformance suite and existing BF1/BF2
  regression tests through both drivers;
- cancellation, action acceptance, terminal/unknown outcomes, output bounds,
  process death, and cleanup preserve the existing contracts;
- switching a configured profile between drivers requires a profile revision
  change and never opens one persistent profile concurrently; and
- real managed and ephemeral canaries pass on both placements before the
  direct driver becomes the selected default.

## Phase 3: Privileged Browser Execution

Add a separate first-party `browser_execute` tool for owner-enabled profiles.
It runs bounded JavaScript or TypeScript against a scoped Playwright `page` and
approved browser-context facade in the existing browser session. It may use
loops, conditions, locators, DOM evaluation, and supported browser APIs that
do not yet have typed first-party actions.

It is a deliberate escape hatch. One invocation may contain multiple browser
steps, so the complete source digest, requested effect, effective approval
decision, action budget, and terminal or unknown outcome bind one durable
invocation. An accepted invocation is never automatically replayed.

Acceptance criteria:

- the feature is disabled unless the exact actor, agent, target, profile, and
  profile revision enable it;
- `approval_mode` is inherited from the profile rather than hard-coded;
- `model_requested` can pause one exact source digest when requested, while
  `none` can run unattended under explicit operator configuration;
- the execution receives no `process`, `fs`, unrestricted imports, ambient
  environment, provider key, node credential, browser endpoint, profile path,
  or raw artifact path;
- runtime, output, action count, memory, network, artifact, and concurrent
  execution limits are enforced outside the submitted code;
- stale tab or document authority, cancellation, timeout, driver loss, and
  disconnect return durable terminal or unknown outcomes without replay; and
- gateway and companion smoke prove structured extraction, an unusual but
  reversible DOM interaction, artifact return, denial, timeout, and cleanup.

A repeated privileged script should become a typed action or recipe when doing
so materially narrows authority or improves reliability.

## Phase 4: Steel Cloud Provider

Add one provider implementation for Steel's self-service cloud browser. The
provider creates and releases Steel sessions, keeps the provider API key and
CDP URL private, supplies a scoped runtime handle to the selected driver, and
maps one configured opaque MintClaw cloud profile alias to one Steel profile.

The initial authentication workflow is visible human login through a bounded
interactive live view followed by provider-managed profile persistence. Site
passwords, OTP values, cookies, and storage state never transit the model and
are not exported into MintClaw.

Acceptance criteria:

- Steel is disabled by default and requires an exact provider configuration,
  secret reference, actor/agent grants, profile revision, concurrency limit,
  session timeout, and explicit maximum billable lifetime;
- discovery reports safe cloud capabilities and limits but no provider name
  unless the operator-selected target alias intentionally reveals it, and no
  provider resource identity or URL;
- create, connect, live-view handoff, resume, observe, action, artifact,
  disconnect, release, timeout, quota failure, and provider outage map to the
  existing first-party lifecycle and safe errors;
- a human can log in once through live view, close cleanly, and reuse the same
  cloud profile in a later session without credential injection;
- provider failure never falls back to gateway or companion;
- abandoned or crashed work is released within the configured bound and a
  process audit finds no locally orphaned driver; and
- billable smoke records duration and cleanup but no API key, CDP URL,
  live-view credential, provider session/profile ID, or personal page content.

Steel Launch is selected because it is self-service and has no fixed monthly
charge. Its session limits, profile retention, API-key scope, pricing, and
interactive view must be rechecked from official documentation immediately
before implementation and again before deployment.

Current primary references are [Steel pricing](https://docs.steel.dev/overview/pricinglimits),
[profiles](https://docs.steel.dev/overview/profiles-api/overview),
[Playwright integration](https://docs.steel.dev/integrations/playwright), and
[live sessions](https://docs.steel.dev/overview/sessions-api/embed-sessions/live-sessions).

Cloudflare Browser Run remains the second provider candidate. Its CDP and live
view behavior should be exercised by the Phase 1 conformance harness only in a
separate future admission. It is not implemented in this goal.

The future comparison must recheck [Cloudflare Browser Run pricing](https://developers.cloudflare.com/browser-run/pricing/),
[CDP and Playwright support](https://developers.cloudflare.com/browser-run/playwright/),
[Live View](https://developers.cloudflare.com/browser-run/features/live-view/),
automatic request identity, persistent-profile behavior, and Workers plan
requirements.

## Phase 5: One Repeatable Workflow Recipe

Add one versioned listing workflow recipe using the same first-party tools and
the deployed driver/provider seam. Keep the recipe outside browser broker
authority. It can cache an observed semantic plan, but it must validate origin,
page identity, fresh element meaning, and post-action invariants before relying
on cached work.

The initial recipe covers listing inventory extraction and a price/status edit
flow because those are demonstrated owner workflows. Fixture validation is
mandatory; a production read-only canary is required. A production edit is
performed only when the owner explicitly selects a disposable or already
intended listing mutation.

Acceptance criteria:

- one recipe version produces a typed listing summary and prepares one exact
  edit through ordinary first-party authority;
- stale layout, missing listing, ambiguous match, changed origin, changed
  account state, and failed postcondition fall back to fresh observation or a
  safe explicit failure;
- the recipe cannot bypass network, profile, approval, no-replay, or artifact
  policy and cannot embed credentials, cookies, provider IDs, or selectors as
  unquestioned truth;
- the same recipe works through local and Steel providers when their target
  advertises the required capabilities; and
- measurements compare completion, latency, tool calls, tokens, recovery, and
  maintenance cost with the ordinary uncached first-party workflow.

Do not add a general recipe language or a second site recipe in this phase.

## Phase 6: HAR, Trace, And Video Artifacts

Extend browser capture with on-demand diagnostic artifacts:

- HAR records bounded HTTP request metadata and timing; bodies and sensitive
  headers are excluded by default;
- Playwright trace records actions, DOM snapshots, screenshots, console, and
  network evidence supported by the selected driver; and
- video or provider session recording supplies a human-readable replay when
  supported by the placement.

Acceptance criteria:

- discovery reports each artifact independently per driver and provider;
- capture must be enabled before the relevant operation when the underlying
  runtime requires it and finalization waits for graceful context/session
  close;
- results contain retained artifact references with digest, size, media type,
  owner, session, expiry, and truncation facts rather than local paths or bytes;
- credential-bearing URLs, authorization headers, cookies, form values,
  request bodies, DOM content, console content, and screenshots follow an
  explicit redaction and operator-access policy;
- retention and total-byte quotas prevent unbounded recording; and
- fixture smoke opens every artifact with its intended viewer or decoder and
  proves expiry, cleanup, cross-session denial, crash behavior, and unsupported
  capability reporting.

## Phase 7: Environment And Clipboard Controls

Add typed, policy-bounded browser environment controls for geolocation, locale,
timezone, viewport/device characteristics, selected browser permissions, and
browser-scoped clipboard read/write.

Profile configuration defines which controls and ranges are available. The
model may select only admitted values. Geolocation emulation does not claim to
change public IP location; a provider proxy is a separate configured property.
Clipboard authority covers the browser context, not the host desktop or other
applications.

Acceptance criteria:

- target discovery describes exact supported controls and bounded choices;
- environment values are bound before context creation when required and a
  later change rotates document/snapshot authority;
- invalid coordinates, timezone, locale, viewport, device, permission, or
  clipboard operations fail before driver dispatch;
- clipboard values follow the same transient-value and durable-redaction rules
  as protected fill;
- an origin cannot gain a permission outside its configured profile scope; and
- fixture pages verify every effective value, permission denial, clipboard
  isolation, profile reuse behavior, and placement capability difference.

## Phase 8: Browser-Viewport Coordinate Fallback

Add one browser action that targets coordinates inside the selected page
viewport. It is authorized by an exact retained or transient screenshot frame,
not by ambient screen position.

Acceptance criteria:

- the request binds target, profile revision, session, tab, viewport geometry,
  device scale, screenshot frame ID, snapshot generation, coordinates, button
  or key, effect, and approval decision;
- stale, resized, scrolled, navigated, replaced, cross-tab, and out-of-bounds
  frames fail before input;
- the action cannot address browser chrome, native dialogs, another window, or
  the host desktop;
- one call performs one bounded coordinate action and returns a fresh frame;
- driver loss after acceptance produces a durable unknown result and no blind
  replay; and
- canvas and custom-widget fixtures pass success, stale-frame, scaling,
  timeout, cleanup, and gateway/companion parity tests.

Native desktop control remains deferred and requires a separate threat model.

## Phase 9: Workspace Routing And Global Closeout

Bind browser placement to an explicit admitted remote workspace context so a
task can keep compatible browser, artifact, file, and terminal operations on
the same companion without exposing node identities or silently falling back
to the gateway.

Acceptance criteria:

- an explicit workspace target resolves to compatible browser targets and
  profiles before session open;
- unsupported browser capability fails on the selected workspace rather than
  running elsewhere;
- changing workspace selection cannot migrate an active browser session;
- model, memory, interaction approval, channel delivery, and durable
  orchestration remain gateway-owned;
- a real companion workflow opens a browser, produces an artifact, moves or
  consumes it through admitted workspace/file capability, and closes with all
  operations proven on the same target; and
- the `global` smoke suite runs every non-billable selected feature on gateway
  and companion, then runs the explicitly enabled Steel suites with bounded
  billing and records exact merged/deployed evidence and rollback.

## Explicit Deferrals

### Attached-user browser

Gateway attached-user code may remain dormant, but no attached profile is
advertised or supported in the current deployment. Gateway live validation,
Darwin companion attachment, extension installation, and attached-profile
closeout are deferred until an operator needs to control an already open daily
browser rather than a MintClaw-managed profile.

### Credential injection and secret managers

Credential injection requires a separately admitted secret-broker foundation:
scoped secret aliases, origin binding, provider access, rotation, revocation,
audit redaction, transient delivery, and proof that neither model nor page can
recover the secret outside its intended input. 1Password, Google Secret
Manager, Steel Credentials, and similar products are candidates, not current
dependencies.

Until then, local and Steel profiles authenticate through visible human login
and retain browser state. Provider infrastructure keys still use existing
operator-owned MintClaw secret configuration; that is not site credential
injection.

### Managed Playwright runtime distribution

Continue installing the pinned Node package through ordinary deployment. Do
not vendor `node_modules` or build an updater until cold-start, offline,
rollback, latency, or version-drift evidence triggers BF4 admission.

### Additional providers and full desktop control

Cloudflare Browser Run is the next provider candidate after Steel but is not
part of this goal. Browserbase, Hyperbrowser, Browserless, WebDriver BiDi, and
other providers or drivers require a concrete need and the conformance suite.
Native dialogs, browser chrome, arbitrary windows, and desktop input remain a
separate computer-capability program.

## Global Completion And Stop Rule

Mark this goal complete only after Phases 0 through 9 are merged, deployed on
every affected configured placement, exercised by their named smoke suites,
recorded in production evidence, and free of unresolved in-scope review or
production defects.

Stop the affected phase and require a new architecture decision if:

- first-party behavior requires exposing raw MCP, CDP, provider, driver,
  profile, credential, path, process, or desktop authority to the model;
- gateway, companion, and cloud require incompatible model-facing contracts;
- privileged execution cannot be separated from arbitrary host execution;
- a provider cannot guarantee bounded cleanup or honest billing lifetime;
- a diagnostic artifact cannot be bounded and protected as potentially
  sensitive data;
- coordinate input cannot be confined to a fresh browser viewport; or
- the implementation grows into a generic framework without a second concrete
  use case.
