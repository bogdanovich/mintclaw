# Protected form task-routing foundation

## Delivery boundary

This is a native prerequisite for natural task switching, not a Telegram UX
completion claim. Runtime ingress, private classification and the question UI
are not connected by this change. Existing deployments do not start routing
messages through this API until that adapter is delivered and qualified.

The separate interpretation-diagnostics prerequisite is
[PR #1782](https://github.com/bogdanovich/mintclaw/pull/1782). Its goal document
defines the combined operator outcome; merging a prerequisite is not completion.

## Ownership and storage

Routing requests live inside the existing encrypted form source, not a second
queue, service or database. They bind the exact authenticated owner, form/mode,
ingress identity, message body and ordered media references. Public snapshots
contain opaque identities and bounded counters, never text or media references.

Native reservation precedes each private interpretation and survives restart.
A model proposal is advisory: it cannot assign fields, approve output or erase
the old form. Routing has four reserved passes, one pending routing request,
at most ten media references and 16 KiB retained routing text. These are separate
from the unchanged four field-input receipts, 64 KiB retention and 32 extraction
passes. Capacity failure does not evict accepted or unresolved form data.

## State and release

An unambiguous same-form decision selects ordinary protected field admission.
That admission binds its receipt and clears the routing copy atomically. The
ordinary field ledger, literal validation and extraction budget remain in force.
An attachment or disagreement selects a protected question instead of guessing
the attachment's purpose. This prerequisite retains that question state; its
protected interaction adapter follows separately.

Automatic switching needs two separately reserved agreeing decisions at the
same current native revision, each quoting the whole new request. Granting the
handoff atomically suspends the old intake and releases only the new request.
The old source, accepted field events and unresolved protected inputs remain.
Suspension differs from legacy pause: a suspended draft no longer captures
ordinary new input. Exact owner-authorized lookup can find it for resumption;
resumption refuses to create two capturing forms on the same route.

The grant is **at most once**, not exactly-once task execution. A replay returns
no text or media, including after restart. A crash after grant but before the
runtime uses it is an uncertain handoff; it must not authorize silent replay or
duplicate external effects. The later runtime adapter must expose that state
truthfully. Grant failure publishes neither suspension nor an ephemeral payload.

## Qualification

Native tests cover owner/topic mismatch, changed attachment replay, stale and
partial evidence, private-call exhaustion across restart, conflicting routing
decisions, same-form phone/city clarification, atomic field linkage, unchanged
accepted and unresolved values, exclusive resume, safe JSON/encrypted storage,
atomic-write failure and no-payload grant replay.

Still required before claiming the operator bug fixed: trusted ingress wiring,
the bounded private classifier, protected buttons plus free text, retained-media
handoff through transient runtime context, runtime/restart/privacy regressions,
reviewed merge, exact-source deployment and a fresh Telegram test. No form,
agency, person name or phone-number heuristic belongs in production routing.
