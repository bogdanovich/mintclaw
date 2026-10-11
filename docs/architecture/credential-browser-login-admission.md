# Credential Browser Login Admission

## Scope and status

Implementation contract for CA4 in the [credential roadmap](credential-access-roadmap.md).
The first consumer performs one configured login in a fresh, private Chromium
context. It returns a fixed login or human-handoff outcome. This document does
not enable a worker, qualify a deployment, or permit real-token startup.

The private implementation adds a canonical `LoginFlow` to account rules,
captures a value copy in the signed login lease, and supplies a credential-only
Playwright session component. Legacy rules without a flow deny browser login.
The Linux launch helper requires inherited privilege restrictions, a protected
executable, an explicit child environment, and a namespace/seccomp witness;
it does not prove caller role, complete network/filesystem isolation, or service
readiness. No command, listener, provider retrieval or model tool activates it.

Ordinary browser tools retain their existing contract. A credential context
has no ordinary browser handle. General browsing, page observations, human
view transport, and subsequent account operations require their own admitted
interfaces and cannot be enabled by handing its context to an ordinary worker.

## Trusted login flow

Protected operator configuration selects the exact HTTPS navigation URL,
credential-entry origin, top-frame form, username/password input identities
and expected types, submission destination, and exact success/handoff URLs with
unique visible state markers. A URL alone cannot report authenticated state.
Every effective flow constraint contributes to the account policy revision.
No model request supplies a URL, selector, JavaScript, provider item ID, browser
path, proxy, launch flag, or credential value. Accounts without a complete
qualified flow deny login. The initial flow excludes embedded frames and
multi-step credential entry; admit them separately after concrete fixtures.

Before retrieval, the worker proves that the configured form and inputs are
current, unique, connected, enabled, and in the live top-frame document at the
exact entry origin. Capture document/element authority in the private driver.

The initial component requires visible inputs and a same-origin POST form with
URL-encoded submission, an empty target, and query/fragment-free unescaped paths.
Each configured ID is bounded ASCII. These deliberately narrow constraints
need individual site qualification; sample test flows are synthetic.
Immediately before injection, recheck that authority and the full current
authenticated owner, actor, agent, host, account, and policy revision. A URL
requested earlier is not evidence of the current origin. Navigation, document
replacement, detached inputs, changed form action, or field-type changes deny
use and retire the attempt. Recheck inside the injection operation before
assigning either value; do not implement credential fill as ordinary tool args.

Retrieve only the immutable vault/item binding held by the current login
lease. Recheck the lease after provider access and before private injection.
Destroy an unconsumed login on denial. Submit only the configured form after
the independent browser action policy permits login submission. The credential
grant does not approve purchases, posts, messages, or account changes.

## Browser and process boundary

The browser worker, credential service, and agent run under separate OS
identities on a host outside the agent's root, sudo, debugger, and service-control
authority. Root-controlled startup proves their process roles and live caller
identities. A PID supplied by a request or writable PID file is insufficient.
Token files, owner keys, broker memory, private IPC, and browser profiles must
be inaccessible to agent tools; the browser cannot read broker token sources.

Require a real Chromium launch with `chromiumSandbox: true`, a fresh private
profile, and a verified sandbox witness under the intended OS restrictions.
Never fall back to an unsandboxed browser. Use a bounded environment allowlist,
an operator-pinned executable, private control transport, and bounded process
resources. Exclude inherited secrets, runtime loader options, attached/cloud
profiles, downloads, extensions, recordings, and generic execution/CDP APIs.

Use the first-party enforcing proxy and exact admitted HTTPS origins. Validate
and pin public DNS answers when making connections, enforce redirects, and
prevent direct-network/proxy bypass. Browser routing or a DNS preflight alone
does not prove this connection boundary. Qualify non-HTTP channels separately
or disable them for this consumer.

## Lease retirement and output

Each credential context belongs to one immutable authenticated scope and one
live grant generation. Borrowing it later does not renew the approval. Check
the lease inside the trusted consumer before every credential or cookie-backed
operation, including status/read operations. Watch retirement independently
of incoming calls. Expiry, revocation, policy replacement, owner-store failure,
caller death, service shutdown, or lost control transport stops admission,
cancels work, and closes the context. A fixed failure is not proof of cleanup:
acknowledge destruction or quarantine/terminate the worker before admitting
another operation. Do not replay an uncertain login after restart or failure.

Return only service-owned opaque references and validated fixed outcomes.
Never forward driver exceptions, page text, accessibility/DOM values,
screenshots, console/network diagnostics, artifacts, cookies, storage state,
MFA seeds, or provider output. Password reveal controls, page echoes, and
encoded credential content exercise this boundary. Approved sites receive
their own passwords; no substring-redaction claim substitutes for the fixed
output interface. MFA/CAPTCHA requires a separately protected human handoff.

## Qualification and observed host

Use a fake vault and synthetic canaries with a real local browser to prove
successful login, stale document/element denial, redirects and network denial,
fixed output on password reveal/echo, cancellation, uncertain injection,
grant expiry/revocation, context destruction, and denial through ordinary
browser APIs. Inspect model-facing payloads and durable stores without
printing canaries. Real secrets remain gated on protected service startup,
actual host isolation, phone qualification, and the CA6 personal pilot.

On 2026-10-11 at 12:46 UTC, a disposable public checkout of
`ba6ab0981315bb744ebe0a3f066771a2e9aa1276` on `server@oc` used the repository's
pinned Playwright 1.63.0 and newly downloaded Chromium. A headless launch with
`chromiumSandbox: true` failed before the sandbox witness could be checked.
There was no fallback, real token, inherited browser profile, root action, or
production-service change. The temporary checkout/profile was removed.
This is failed qualification, not evidence of the exact launch failure cause.

The host reported `kernel.unprivileged_userns_clone=1` and
`kernel.apparmor_restrict_unprivileged_userns=1`. Chromium documents that Ubuntu
can require a binary-specific AppArmor allowance for downloaded builds. A
future deployment must qualify its exact immutable browser path and sandbox;
do not disable the host-wide restriction as a rollout shortcut. The existing
agent's admin authority is a separate unresolved isolation gate.

Sources: [Chromium AppArmor restrictions](https://chromium.googlesource.com/chromium/src/+/main/docs/security/apparmor-userns-restrictions.md)
and [Playwright Chromium sandbox option](https://playwright.dev/docs/api/class-browsertype#browser-type-launch-option-chromium-sandbox).
