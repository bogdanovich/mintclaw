# Runtime Capability C6 Exit Record

Status: complete

Date: 2026-09-29

Roadmap: [Runtime Capability And Turn-Engine Convergence](runtime-capability-convergence-roadmap.md)

## Result

C6 completes the cross-runtime capability program without making the gateway
depend on the coding controller or giving both hosts identical authority.
Coding now uses the shared document implementation for local field discovery,
fill, and verification, and it projects the existing authenticated browser
broker for the admitted browser workflow. Advanced capabilities whose owner is
not yet complete remain absent rather than being approximated.

The first C6 packet landed in [PR #1444](https://github.com/bogdanovich/mintclaw/pull/1444),
merge `aa32ce649af1563881f1f4940f22295fbba672e3`. The closing packet adds the
protected-answer fail-closed invariant and records the owner decisions below.

## Ownership Matrix

| Capability | Admission and authority | Durable state owner | Decision |
| --- | --- | --- | --- |
| PDF `fields` | Trusted coding document switch, final tool admission, exact runtime principal, and exact workspace path or thread attachment | No mutation state; bounded source acquisition remains in the shared document engine | Admitted |
| PDF `fill` and `verify` | Same coding principal plus a complete explicit stable-ID assignment map; repository instructions and skills grant no authority | Canonical coding thread `document-writes` journal and `document-artifacts` index | Admitted |
| Protected PDF form | Gateway protected-answer transport, registered namespace sink, form-job store, and audit owner | Gateway form store and protected audit path | Not admitted in coding |
| Browser observe/act/capture/download | Trusted coding browser switch plus an authenticated exact broker capability alias | Existing gateway browser session/profile/invocation owners; verified imported artifacts belong to the coding thread | Admitted where broker capability exists |
| Browser handoff/resume | Existing broker live-resource handoff plus an admitted human-interaction host | Existing gateway browser ledger and interaction lifecycle | Deferred in coding |
| Attached-user browser | Explicit user consent, visible tab selection, connector lifecycle, and refreshed owner admission | Existing browser owner contract; never the coding thread | Deferred globally |
| Companion browser placement | Operator-selected broker target/profile alias; no raw node command or profile selector reaches the model | Existing gateway/companion broker and browser ledger | Admitted only through the broker |
| Channel delivery | Authenticated route, channel adapter, outbox transaction, and delivery settlement | Gateway outbox and channel owner | Optional gateway adapter; absent in coding |

## PDF Write And Recovery Contract

The coding document schema contains only `inspect`, `extract`, `render`,
`fields`, `fill`, and `verify`. It omits `form`, retained render, and every
channel-delivery field or callback.

One direct fill has these owners:

1. The runtime principal owns the exact source and output authority.
2. The shared document write coordinator owns write and verification
   progression.
3. The coding thread write journal owns path-free and value-free operation
   evidence.
4. The coding thread artifact index owns the verified PDF bytes and enforces
   exact owner access.

Assignments exist only in the active call and one-shot worker request. Durable
tool arguments, reports, transcripts, diagnostics, the write journal, and the
artifact index retain hashes and state but not submitted field values.

The existing journal distinguishes accepted/writing/written/verifying states,
successful registration, cancellation, failure, and uncertain recovery. An
exact idempotent retry recovers the same owner-bound artifact reference. A
different thread cannot use that operation or artifact identity. Coding adds
no delivery-pending or delivery-settlement path; gateway delivery continues to
use the existing document transaction and outbox owners.

## Protected Form Decision

Ordinary coding questions are runtime-neutral and can resume through the
shared interaction registry. Protected answers are not: the existing coding
answer API would otherwise construct an ordinary plaintext
`interactions.Answer` and call `ClaimAnswer` without the protected namespace
sink.

The closing invariant therefore rejects a record with a protected-answer
binding at both coding boundaries:

- it is not projected to the terminal frontend; and
- a direct or cached-identity answer attempt is rejected before a durable
  claim.

Regression coverage proves that the record stays waiting, has no answer, and
does not retain a plaintext canary in the interaction snapshot.

Coding protected forms may be reconsidered only when one coherent packet owns
all of the following:

- a protected-value ingress distinct from ordinary composer text;
- a coding-scoped form-job store and protected-answer namespace sink;
- a coding-scoped audit owner that retains receipts but not raw values;
- restart, cancellation, expiry, correction, and uncertain-outcome recovery;
- negative transcript, diagnostic, event, and filesystem leak tests; and
- adaptive skill admission derived from the final effective capabilities.

Until then `document.form` remains `runtime_unsupported` in coding. This is a
deliberate privacy boundary, not an implementation fallback.

## Advanced Browser Decision

Coding already uses one control plane: its browser facade invokes exact
operations through the authenticated gateway broker. The broker remains the
sole owner of profiles, sessions, action policy, invocation receipts, and
companion placement. Coding owns only its runtime principal and imported
thread artifacts.

The restricted broker source reports handoff unavailable, excludes
handoff/resume from the coding session schema, and rejects attached-user
profiles. Existing invariant tests cover both boundaries. Capture and download
reuse the broker's owner-bound artifact transfer instead of introducing an
inline or second file-transfer protocol.

Future coding handoff must project the existing broker handoff into a suitable
coding interaction host while preserving one live controller, prompt
ownership, expiry, cancellation, restart, and safe resume. It must not create
a coding browser ledger. Attached-user work additionally requires the new
owner decision and refreshed admission required by
[Browser B4](browser-b4-execution-goal.md); dormant gateway code is not
authority to enable it. Richer companion placement must continue to use exact
broker-selected target/profile aliases and must never become generic remote
command execution.

## Completion Evidence

The C6 completion gate is satisfied:

- every admitted action has an exact runtime principal and an explicit state
  owner;
- protected values are rejected from ordinary coding interaction persistence,
  PDF assignments remain value-free durably, and browser credentials never
  cross the broker facade;
- PDF recovery distinguishes success, failure, cancellation, and uncertainty,
  while browser mutations retain broker no-replay receipts; and
- coding adds no second PDF transaction engine, protected form store, browser
  session/profile ledger, interaction transport, outbox, or delivery control
  plane.

The bundled PDF and browser skills describe these effective branches but do
not grant them. Trusted runtime configuration and final admitted capabilities
remain authoritative.

## Deferred Turn-Engine Work

R0-R3 remain deferred. Gateway and coding already share the native
`AgentLoop`/pipeline turn engine while retaining distinct host responsibilities
for routing, delivery, thread leases, TUI presentation, and storage roots. A
new harness abstraction or cutover should begin only when a concrete second
engine exists or measurements identify a duplicated responsibility that the
current shared engine does not already own.
