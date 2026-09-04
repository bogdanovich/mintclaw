# Browser Capability B4 Admission

Status: admitted for phased implementation

Browser milestone B4, **Browser Identity and Attached-User Profiles**, is
admitted as the dependency-ordered sequence in this document. It extends the
deployed first-party browser contract on gateway and companion placements. It
does not expose raw Playwright MCP tools, browser endpoints, profile paths,
cookies, or storage state to the model.

The implementation is governed by
[Browser B4 Execution Goal](browser-b4-execution-goal.md). Each phase is a
focused pull request or the smallest coherent dependent pull-request sequence.
A later phase starts only after its prerequisites are merged, deployed, and
live-validated on every placement it changes.

## Operator Outcome

An operator can configure multiple named browser identities and deliberately
choose among:

- a persistent MintClaw-managed profile;
- a fresh ephemeral profile whose browser state is destroyed after use; and
- an existing signed-in Chrome profile attached with visible, bounded operator
  consent.

The same first-party tools continue to perform browser work. Profiles change
where identity state lives and how a session is activated; they do not create
a second model-facing automation API.

Cloud is reserved as a profile identity class, but B4 does not enable a cloud
provider. Provider selection, credentials, billing, live-view URLs, and remote
resource cleanup remain B5 work.

## Existing Contracts Reused

B4 reuses these deployed contracts without weakening them:

- `browser_targets`, `browser_session`, `browser_observe`, `browser_contexts`,
  and `browser_act` remain the complete model-facing surface;
- gateway and companion use the same typed broker, action, artifact, approval,
  freshness, and no-blind-replay contracts;
- profile and target names are model-safe operator aliases rather than paths,
  node IDs, endpoints, or driver arguments;
- one non-terminal session owns a target/profile lease;
- configuration reload closes the previous gateway browser generation before
  the new generation becomes authoritative;
- a companion catalog and profile revision bind every routed action;
- human handoff transfers the controller of an already open managed session
  and requires a fresh observation after resume;
- protected fill keeps model-provided values out of durable state;
- network, capability, approval, artifact, and resource policy continue to
  intersect rather than replace one another; and
- `full_access` removes semantic field and action restrictions but does not
  bypass session ownership, stale-reference rejection, protocol validation,
  configured network reachability, or resource limits.

The current `managed` alias and its existing profile directory are production
data. Migration must preserve that directory byte-for-byte and must not log,
copy, export, or reinterpret its browser state.

## Admitted Profile Classes

### Managed

A `managed` profile is a persistent, dedicated MintClaw browser identity. Each
enabled profile has an arbitrary valid operator alias, a revision, exact actor
and agent grants, one execution target, a private profile directory, a separate
exclusive lock, lifecycle policy, browser policy, and bounded limits.

The gateway driver template remains a base executable definition. Reserved
identity flags such as `--user-data-dir`, `--isolated`, `--storage-state`,
`--extension`, endpoints, and profile lock paths are derived from the selected
profile by trusted code; they are not accepted as model arguments or ambiguous
duplicate operator arguments.

Managed profile storage is persistent until the operator removes it offline.
MintClaw does not add online export, import, backup, or migration commands in
B4. The supported migration rule is stop, verify ownership and permissions,
move or restore the directory using operator tooling, update the opaque profile
mapping and revision, then restart or reload. MintClaw never moves a live
profile and never follows an unvalidated replacement or symlink.

### Ephemeral

An `ephemeral` profile creates one owner-only runtime directory and isolated
browser context for one browser session. It starts without cookies, local
storage, cache, service-worker state, or a copied managed profile. Any login
state created during the session remains ephemeral.

Close, expiry, cancellation, open failure, driver loss, gateway reload, node
disconnect recovery, and process restart must terminate the worker and remove
the session directory. Cleanup is idempotent. A failed deletion quarantines the
profile root, reports a safe `cleanup_required` state, and prevents reuse until
operator repair; it is never reported as successful cleanup.

No browser-generated file outside the admitted artifact store may be retained
as an implicit way to preserve ephemeral identity.

### Attached User

An `attached_user` profile connects the private pinned Playwright driver to an
existing Chrome or Edge tab through the official Playwright browser-extension
flow. The extension and its connection approval remain browser UI owned by the
operator. MintClaw does not expose the extension token, CDP endpoint, tab
endpoint, browser profile directory, cookie store, or storage state.

The first B4 attached implementation uses per-session consent. Opening the
profile creates a one-use, expiring attach request routed to the authenticated
owner. After the owner allows it, the local browser displays the Playwright
connection and tab-selection UI. The session cannot become `ready` until the
operator visibly approves the connection and selects a tab. Denial, timeout,
restart, cancellation, or connector loss revokes the request and closes the
private driver.

The attach consent is bound to owner, actor, agent, target, profile and profile
revision, one browser session ID, one connector generation, and an expiry. It
cannot approve a later session. B4 does not use a permanent extension token to
bypass the connection dialog. A future bounded-arm convenience mode requires a
separate admission if real use shows that per-session consent is too costly.

Attached Chrome remains a user-owned process. MintClaw can guarantee one active
MintClaw controller and reject stale document authority, but it cannot prevent
the human or another extension from changing the selected tab concurrently.
Every first-party action therefore keeps the existing fresh snapshot and
private document-identity checks. Ambient human changes produce stale or
unknown outcomes, never a blind replay.

Attached profiles require an explicit action-origin scope:

- `exact_origins` permits first-party actions only while the selected top-level
  document and any declared navigation destination match a configured exact
  HTTP or HTTPS origin; or
- `any_http` explicitly permits actions on any syntactically valid HTTP or
  HTTPS top-level origin.

This is an action boundary, not a claim that MintClaw proxies all traffic from
an existing user browser. Background requests, extensions, and user navigation
are outside the managed network proxy. Discovery and documentation must state
that difference. An attached profile cannot claim the managed
`public_web` request-level network guarantee. Non-HTTP browser pages are never
model-actionable.

Human handoff and attached activation remain distinct. Attached activation
selects an existing user tab and grants MintClaw a bounded session. Handoff
temporarily releases agent control of an already active MintClaw session.
Neither operation approves a later external commit.

### Cloud

`cloud` may be reserved in shared profile enums and safe descriptors, but an
enabled cloud profile is rejected until a B5 provider admission supplies a
conforming implementation. B4 completion does not depend on a cloud provider.

## Authority And Revocation

Every profile operation is authorized by the intersection of:

1. the authenticated route owner and actor;
2. workspace and selected browser agent;
3. the global browser-tool grant;
4. target placement and exact profile alias;
5. profile mode, revision, actor and agent grants;
6. network or attached action-origin authority;
7. capability and approval modes, restricted rules, and optional policy hook;
8. session, controller, tab, frame, snapshot, document, and prepared-action
   authority;
9. the execution host's private runtime mapping and profile lease; and
10. for a companion, current pairing, catalog, command, profile, and invocation
    authority.

Possessing or guessing an alias is not authority. Unknown, disabled,
ungranted, and unavailable profiles return the same bounded denial class so
the tool cannot be used to enumerate another actor's identities.

A profile revision change, disable, actor or agent grant removal, or
attached-consent revocation prevents new opens and actions.
Gateway configuration reload must close or quarantine every session owned by
the retired generation before the replacement becomes active. A companion
profile change requires a new catalog/profile revision; the gateway closes or
quarantines sessions bound to the old revision and never falls back to another
profile or placement.

Revocation is fail-closed even when cleanup reports an error. A session whose
worker outcome cannot be proven is `lost` or `unknown`, and its profile remains
unavailable until the lease and process state are reconciled.

## Configuration Contract

The final names may be represented by ordinary Go configuration types, but the
following shape and semantics are normative:

```json
{
  "tools": {
    "browser": {
      "enabled": true,
      "agents": ["browser"],
      "default_target": "gateway",
      "targets": {
        "gateway": {
          "enabled": true,
          "placement": "gateway",
          "driver": "playwright_mcp",
          "driver_server": "playwright",
          "profiles": {
            "personal": {
              "enabled": true,
              "revision": "personal-v1",
              "mode": "managed",
              "allowed_agents": ["browser"],
              "allowed_actors": ["telegram:123456"],
              "network_mode": "any_http",
              "capability_mode": "full_access",
              "approval_mode": "model_requested",
              "dry_run": false,
              "allow_approved_actions": true,
              "runtime": {
                "profile_directory": "/var/lib/mintclaw/browser/personal",
                "lock_file": "/run/mintclaw/browser-personal.lock",
                "headed": true
              }
            },
            "scratch": {
              "enabled": true,
              "revision": "scratch-v1",
              "mode": "ephemeral",
              "allowed_agents": ["browser"],
              "allowed_actors": ["telegram:123456"],
              "network_mode": "public_web",
              "capability_mode": "full_access",
              "approval_mode": "model_requested",
              "dry_run": false,
              "allow_approved_actions": true,
              "runtime": {
                "ephemeral_root": "/var/lib/mintclaw/browser/ephemeral",
                "lock_file": "/run/mintclaw/browser-scratch.lock",
                "headed": false
              }
            },
            "chrome": {
              "enabled": true,
              "revision": "chrome-v1",
              "mode": "attached_user",
              "allowed_agents": ["browser"],
              "allowed_actors": ["telegram:123456"],
              "network_mode": "any_http",
              "capability_mode": "full_access",
              "approval_mode": "model_requested",
              "dry_run": false,
              "allow_approved_actions": true,
              "attached": {
                "connector": "playwright_extension",
                "consent_mode": "per_session",
                "consent_seconds": 300,
                "action_origin_mode": "exact_origins",
                "allowed_origins": ["https://www.facebook.com"]
              }
            }
          }
        }
      }
    }
  }
}
```

The operator configuration may choose any valid profile alias. MintClaw
contains no site or account-name allowlist.

## Model-Facing Contract

### Discovery

`browser_targets` continues to list only profiles granted to the current agent
and actor. Each safe profile descriptor adds:

- `mode`: `managed`, `ephemeral`, or `attached_user`;
- persistence: `retained`, `session_only`, or `user_owned`;
- whether headed view, handoff, and per-session attach consent are available;
- the existing capability, approval, network, action, context, diagnostic, and
  artifact flags and bounded limits; and
- for attached profiles, the safe action-origin mode and whether the profile is
  ready, awaiting operator presence, busy, degraded, or unavailable.

Discovery never returns actor lists, paths, browser endpoints, extension
tokens, installed extensions, cookies, storage state, or the titles and URLs
of unattached user tabs.

### Session lifecycle

The existing `browser_session open` arguments remain target and profile aliases.
Managed and ephemeral opens preserve the current ready lifecycle. An attached
open may return a durable human interaction while it is `attach_pending`.
Only the authenticated resolution of that interaction may create the one-use
consent and continue the same open request. Successful selection rotates
context authority and returns a fresh observation. The model cannot select a
native browser profile or tab by host identifier.

`status`, `close`, `handoff`, and `resume` preserve their current meanings.
Close detaches an attached tab without closing the user's Chrome process or
other tabs. Driver or gateway loss never kills the user-owned browser.

## Delivery Sequence

1. Generalize profile authority and migrate the existing managed profile.
2. Complete managed profile aliasing, revision-bound revocation, and lifecycle
   conformance on gateway and companion.
3. Add ephemeral profiles and prove cleanup on gateway and companion.
4. Add per-session attached Chrome on the gateway through the Playwright
   extension flow.
5. Add the same attached-user contract on the Darwin companion and record
   global B4 production evidence.

The exact acceptance gates and stop conditions for each phase are in the
execution goal. Deployment configuration changes are part of the phase that
introduces them. No implementation phase is complete only because unit tests
or CI are green.

## Global Acceptance Evidence

B4 is complete only when all of the following are proven:

- two differently named managed profiles cannot share a directory, lock, or
  live worker, and the existing production profile retains its login state
  through the schema cutover;
- actor, agent, target, profile, and revision mismatches fail before worker
  open or action dispatch;
- disabling or revising a profile prevents new work and closes or quarantines
  every active session bound to the retired authority;
- gateway and companion ephemeral sessions start clean and leave no retained
  browser identity after success, failure, cancellation, reload, disconnect,
  restart, and forced cleanup error paths;
- attached Chrome requires visible, expiring, one-use owner consent, exposes
  only the selected tab, and detaches without closing the user browser;
- denial, expiry, disconnect, revocation, reload, and restart cannot leave a
  reusable attach authorization or two MintClaw controllers;
- attached origin checks accurately describe their top-level action boundary
  and never claim managed request-proxy enforcement;
- attached activation and human handoff remain different state transitions,
  and neither bypasses external-commit approval policy;
- `browser_targets` advertises only features actually available on that target,
  profile, placement, and runtime generation; and
- real owner-routed smoke workflows complete on gateway and companion for
  managed reuse, ephemeral cleanup, attached consent, fresh observe/action,
  detach, immediate profile reuse, and process/lock audit.

Attached smoke tests use a non-sensitive tab and make no irreversible external
commit.

## Mandatory Stop Conditions

Stop the affected phase and require a new architecture decision if:

- a profile path, lock path, native tab identifier, raw endpoint, extension
  token, cookie, storage state, or secret value must enter a model-visible or
  ordinary durable payload;
- gateway and companion require different model-facing profile or action
  contracts;
- an existing persistent profile must be copied, exported, deleted, or opened
  concurrently to complete migration;
- an ephemeral profile can retain identity state without a detectable cleanup
  failure;
- attached Chrome requires exposing generic CDP, raw MCP, arbitrary extension
  control, or a permanent unbounded authorization;
- attached mode is presented as enforcing browser-wide request policy that the
  connector cannot actually enforce;
- revocation cannot prevent new actions or cannot quarantine an active session
  whose worker outcome is uncertain;
- closing an attached session can terminate or mutate unrelated user tabs;
- the model can enumerate ungranted profiles through distinguishable errors;
  or
- live validation requires an irreversible external commit.

## Non-Goals

B4 does not add cloud providers, provider billing, remote live-view services,
profile export/import, cookie or storage-state tools, credential injection,
password-manager APIs, TOTP generation, CAPTCHA bypass, arbitrary headers,
client certificates, raw Playwright execution, generic JavaScript, generic MCP
forwarding, CDP access, desktop control, coordinate input, site-specific
recipes, browser migration between hosts, or workspace routing. Those remain
B5, B6, BF3, or separately admitted work.

## Deferred Credential Provider Direction

Persistent managed profiles are the current authentication mechanism: the
operator signs in through the existing visible handoff and the browser retains
the resulting session state. Direct credential injection is deferred until a
real workflow demonstrates that profile-based login is insufficient.

If admitted later, credential resolution must remain host-local and behind a
provider interface. For 1Password Individual or Personal subscriptions, the
1Password CLI desktop-app integration can serve only as an attended provider
because it requires a locally running, unlocked app and interactive operator
authentication. Unattended 1Password access requires a Teams or Business
service account and should be scoped read-only to a dedicated MintClaw vault.
Google Secret Manager is another possible adapter, but no external secret
manager is currently planned or required.
