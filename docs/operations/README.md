# Operations

Operational docs for debugging, diagnosis, and production troubleshooting.

- [Troubleshooting](troubleshooting.md): common failures, symptoms, and recovery steps.
- [Debugging MintClaw](debug.md): live logs, passive diagnostic traces, and
  root-cause workflow.
- [Node Companion P0 deployment evidence](node-companion-p0-deployment.md):
  same-SHA rollout, bounded smoke verification, stale-revision drill, evidence,
  and rollback.
- [Node Companion P2 file-transfer deployment](node-companion-p2-deployment.md):
  deny-by-default rollout, reversible transfer fixtures, redaction checks, and
  rollback evidence.
- [Node Companion P2 deployment evidence](node-companion-p2-deployment-evidence.md):
  merged revisions, focused validation, live canaries, completion gates,
  enabled authority, backups, and the mandatory stop before P3.
- [Node Companion P3 service-administration deployment](node-companion-p3-deployment.md):
  deny-by-default Linux systemd profile setup, helper isolation, canary,
  redaction checks, and rollback.
- [Node Companion P3 deployment evidence](node-companion-p3-deployment-evidence.md):
  merged revisions, requirement matrix, live approved restart, no replay,
  checksums, rollback rehearsal, enabled authority, and mandatory stop.
- [Node Companion P5a durable-jobs proof](node-companion-p5a-proof.md):
  implementation matrix, real-process restart/log/artifact/cancellation proof,
  deployment evidence, rollback, and mandatory stop before P8.
- [Node terminal client and lifecycle smoke test](node-terminal-smoke.md):
  interactive use and automated verification of authenticated PTY open,
  attach, resize, input/output, and confirmed close.
- [Live gateway agent smoke test](live-agent-smoke.md): authenticated,
  bounded testing of the running gateway agent and its live node sessions
  without Telegram or a second agent runtime.
- [Browser Capability B2 deployment evidence](browser-capability-b2-deployment-evidence.md):
  merged revisions, live screenshot/upload/download proof, passive diagnostics,
  human handoff and resume, privacy checks, cleanup, health, and rollback.
- [Browser Functional Parity Phase 5 deployment evidence](browser-functional-parity-phase5-evidence.md):
  merged ordinary-interaction slices, live gateway and companion file-chooser
  proof, specialist-boundary restoration, cleanup, health, and rollback.
- [Browser Functional Parity Phase 6 and global completion evidence](browser-functional-parity-phase6-evidence.md):
  merged BF2 slices, exact gateway and companion deployment, screenshot,
  upload, download, diagnostics, large-snapshot, receipt, privacy, cleanup,
  health, and rollback proof closing the six-phase goal.
- [Browser current-contract cutover](browser-current-contract-cutover.md):
  pre-deployment state audit, current browser authority evidence, opaque
  no-replay tombstone boundary, rollout, and rollback.
- [Gateway invocation SQLite operations](gateway-invocation-sqlite.md):
  retention, health and size inspection, backup/restore, capacity exhaustion,
  and matching-state rollback.
- [Architecture simplification Z1 session cutover](architecture-simplification-z1-session-cutover.md):
  stopped-state session conversion, atomic installation, matched rollback,
  reapply canaries, observation, retained recovery evidence, and cleanup.
