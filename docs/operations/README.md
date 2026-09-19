# Operations

Operational docs for debugging, diagnosis, and production troubleshooting.

- [Document acquisition and inspection](document-acquisition.md): PDF0A immutable identity,
  PDF0B structural inspection, PDF1A bounded agent/channel reads, typed failures, retained renders,
  supported tuple, cleanup, and CLI, integration, deployed, and Telegram smokes.
- [PDF3 conversational form test](pdf3-conversational-form-test.md): automated and real-Telegram qualification of
  protected answers, restart/compaction continuity, correction, approval, verified exactly-once delivery, cancel,
  privacy, and cleanup on the supported Linux deployment.
- [Troubleshooting](troubleshooting.md): common failures, symptoms, and recovery steps.
- [Debugging MintClaw](debug.md): live logs, passive diagnostic traces, and
  root-cause workflow.
- [Node Companion P0 deployment evidence](node-companion-p0-deployment.md):
  same-SHA rollout, bounded smoke verification, stale-revision drill, evidence,
  and rollback.
- [Linux node privileged runtime lifecycle](node-linux-privileged-runtime.md):
  root-owned volatile socket directories, acyclic systemd dependencies,
  effective-process readiness, and mandatory reboot verification.
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
- [Remote coding tasks](remote-coding-tasks.md): paired-machine architecture,
  project and requester grants, safe descriptor discovery, activation,
  recovery, rollback, and Linux/macOS real-process proof.
- [Node terminal client and lifecycle smoke test](node-terminal-smoke.md):
  interactive use and automated verification of authenticated PTY open,
  attach, resize, input/output, and confirmed close.
- [Live gateway agent smoke test](live-agent-smoke.md): authenticated,
  bounded testing of the running gateway agent and its live node sessions
  without Telegram or a second agent runtime.
- [Node JSON canonicalization v2 cutover](node-json-canonicalization-v2-cutover.md):
  gateway-first rollout, completed v2 fleet inventory, verification,
  checksummed rollback, and the retained-record zero-v1 gate.
- [Interaction record strict-reader cutover](interaction-record-strict-reader-cutover.md):
  approval-authority inventory, binary-only rollout, live verification, and
  rollback evidence.
- [Browser Capability B2 deployment evidence](browser-capability-b2-deployment-evidence.md):
  merged revisions, live screenshot/upload/download proof, passive diagnostics,
  human handoff and resume, privacy checks, cleanup, health, and rollback.
- [Browser B4 Phase 1 deployment evidence](browser-b4-phase1-evidence.md):
  canonical profile authority, lossless managed-identity cutover, exact gateway
  and Darwin deployment, first-party canaries, trace settlement, cleanup, and
  rollback.
- [Browser B4 Phase 2 deployment evidence](browser-b4-phase2-evidence.md):
  managed-alias isolation, lease and capacity conformance, revision-bound
  revocation, quarantine recovery, exact restoration, and rollback.
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
- [Browser continuation Phase 0 evidence](browser-continuation-phase0-evidence.md):
  canonical gateway and companion smoke baseline, strict structured evidence,
  cleanup fault coverage, profile discovery, ledger settlement, and rollback.
- [Browser continuation Phase 1 evidence](browser-continuation-phase1-evidence.md):
  provider and driver seams, conformance and lifecycle coverage, gateway and
  companion live matrix, state-capacity correction, cleanup, and rollback.
- [Browser continuation Phase 2 evidence](browser-continuation-phase2-evidence.md):
  direct official Playwright-library driver, gateway and companion production
  matrices, lifecycle corrections, selected-default proof, cleanup, and
  rollback.
- [Gateway invocation SQLite operations](gateway-invocation-sqlite.md):
  retention, health and size inspection, backup/restore, capacity exhaustion,
  and matching-state rollback.
- [Architecture simplification Z1 session cutover](architecture-simplification-z1-session-cutover.md):
  stopped-state session conversion, atomic installation, matched rollback,
  reapply canaries, observation, retained recovery evidence, and cleanup.
- [Architecture simplification O7/O8 cutover](architecture-simplification-o7-o8-cutover.md):
  strict browser-policy and coding-baseline cutover, matched rollback and
  reapply, passive canaries, zero-legacy audit, recovery, and cleanup.
