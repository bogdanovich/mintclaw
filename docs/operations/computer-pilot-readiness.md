# Computer-use pilot readiness and rollback

The current public tools can discover, observe and hand off an explicitly
granted window. This runbook covers staging that read-only path. The
[roadmap](../architecture/computer-use-roadmap.md) tracks the remaining gates
for an input pilot. Apply production changes only after the operator approves
the specific rollout.

## Select and record the canary

Use one disposable window, one companion, and one exact actor, agent and
runtime. Record the gateway and companion revisions and executable digests,
OS and architecture, driver digest, target alias, profile revision, and
lease and observation limits. On macOS, record the approved app identity
and verify its existing grants through the actual app. On Linux, select the
intended X11 session explicitly. Record any testing on a disposable display
as evidence for that environment.

Start with the computer grant disabled. Use the existing
[companion configuration](../architecture/computer-companion-boundary.md#native-host-configuration)
and [gateway configuration](../architecture/computer-gateway-tools.md#operator-configuration)
to prepare the paired opt-in for the canary. Use the profile revision from
the companion's approved catalog. Keep the existing credential, browser and
node settings in the restore copies. Store both configurations and matching
build receipts privately before changing either side.

## Verify the staged read-only path

Run [the public gateway smoke](../architecture/computer-gateway-tools.md#qualification)
with the pinned driver and companion and an owned test window. Run it
separately for each platform. Use the gateway suite for the protected image
transfer and same-session lifecycle. Treat an unavailable desktop, missing
grant, rejected pin or skipped fixture as an incomplete check.

Check these outcomes in the staged environment before widening the canary:

| Check | Expected outcome |
| --- | --- |
| Disabled grant or unapproved principal | Computer tools are absent for the caller. The disabled configuration creates no capture worker. |
| Discovery and open | Only the configured alias is eligible, and the bound profile revision matches the approved catalog. |
| Handoff and resume | Agent capture is denied during human control. A fresh observation after resume belongs to the original session, worker and window incarnation. |
| Target loss and reconnect | The old binding is rejected. An uncertain open or transition is reconciled against the original invocation. |
| Finish and release | Automation authority ends. Verify application lifetime independently when it matters to the user. |
| Protected results | Retained history contains the protected receipt. Images and live accessibility text stay out of ordinary logs and transcript exports. |

Record the observed result and sanitized invocation or disposition IDs.
For an uncertain transition, record which owner has the unresolved obligation.
Keep browser and other node health checks in the same baseline so a regression
in a shared pathway is visible.

## Stop and roll back

End the canary interaction and release the owned automation session. Verify
the reported disposition. If a release is unconfirmed, reconcile the original
invocation before starting a replacement session. A restart or an expired
lease is insufficient to mark an unresolved disposition confirmed.

Remove the canary's gateway grant, or disable `tools.computer.enabled` if the
canary is the only computer consumer. Restore the matching companion profile
and command policy from the prepared configuration. Keep material needed to
settle outstanding invocations. If the canary introduced a binary change,
restore the previously recorded compatible gateway and companion builds and
use the normal health and rollback procedures for those services.

Verify that the removed actor or target can no longer discover or open the
canary. Check gateway and companion health and repeat the baseline browser
check. Keep release confirmation separate from application-window lifetime.

## Before staging input

Require a reviewed native host and supported target scope for each platform.
Repeat the [roadmap safety matrix](../architecture/computer-use-roadmap.md#acceptance-suite-and-release-gates)
on the specific tuple, including local emergency stop, cancellation and
independent proof of held-input cleanup. Verify effect, no replay and human
ownership through the real gateway and companion route. Use the separate
[evaluation pack](../architecture/computer-evaluation-pack.md) for the 20 model
tasks per platform and record approved run limits. The read-only staging
steps above can be rehearsed while the input gates are open.
