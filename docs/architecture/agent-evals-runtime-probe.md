# Eval Runtime Admission Probe

## Scope

Prerequisite for E1B2 of the [manual eval roadmap](agent-evals-roadmap.md): local
preset contents and a healthy PID are not evidence of the gateway's active config.
No scenario runner, provisioning, task, approval, or production deployment is
introduced here.

`GET /runtime` requires the existing gateway service bearer token, not the
MintClaw chat-channel token. An empty service token never enables anonymous
access. It is read-only, ready-gated, uncached, and unavailable during reload.
Public `/health` and `/ready` behavior is unchanged.

Version 1 returns PID, a service-instance fingerprint, active public-config
fingerprint, physical home/config-path fingerprints, actual log level, build
metadata, and OS/architecture. It returns no configuration graph, path, secret,
credential, or underlying error. Missing build metadata remains explicitly empty.

The config fingerprint uses the native public projection under the agent's
configuration lock. Binary build information is excluded from this fingerprint
and reported separately. An on-disk config edit or failed reload is not confused
with an accepted active configuration.

Snapshot collection holds the runtime's reload exclusion through the final log
level read. Reload transitions take the exclusive side of that same lock. Probe
requests refuse immediately while the lock is busy, rather than waiting behind
a potentially slow reload. A complete reload between two boolean flag reads
therefore cannot produce a mixed-generation successful observation.

## Admission Ownership

E1B2 compares this observation with the inspected private preset/profile before
billable work; the actual turn must still match native agent/session/workspace
identity. A mismatch, unavailable probe, or unknown required observation refuses
the run. The probe itself cannot certify isolation: filesystem permissions,
allowlisted tools/side effects, browser placement, immutable fixtures, credentials,
and owned resource cleanup remain separate preflight/runner responsibilities.

Typed secret values are excluded from the public fingerprint. It does not prove
which credentials are available, provider reachability, skill/file contents,
external resource state, or byte identity of the running executable. Build fields
are the binary's native build metadata, not the checkout's current revision.

CI tests use synthetic configs and stub providers, never live model calls.

## Recovery Drain Prerequisite

Native interaction recovery runs independently of inbound turn workers, and startup unanswered-session recovery
can also hold runtime-owned providers and stores. `CloseContext` must seal recovery enrollment under the same
lifecycle lock used for Run, cancel admitted recovery contexts, and wait for both kinds before closing those
resources. The interaction coordinator still owns its existing single-flight gate and durable state transitions.

A close deadline leaves shared resources open and recovery enrollment sealed, permitting a later close attempt
to finish teardown after the original work retires. Retries share one drain completion signal; they do not start
new recovery, replay input, or create another document/browser lifecycle. A late recovery after sealing returns
before loading or mutating native registries or session stores.

This fixes a native shutdown prerequisite, not a dynamic idle extension of `/runtime`. An eval still needs owned
target identity, complete native settlement and admitted private-surface/resource witnesses before it can grant
positive no-write, delivery, privacy or cleanup checks. A final reply, zero active-turn count or disappeared PID
alone cannot substitute for those observations.
