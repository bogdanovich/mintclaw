# Architecture

Internal architecture notes for major runtime mechanisms and subsystem design.

- [Steering](steering.md): injecting messages into a running agent loop between tool calls.
- [Codex-Style Steering Roadmap](codex-style-steering-roadmap.md): incompatible replacement of pending-tool classification with same-turn post-batch user-input steering.
- [Architecture Simplification Roadmap](architecture-simplification-roadmap.md):
  canonical internal contracts, bounded additive wire compatibility, removal
  of historical fallback paths, browser/task/delivery ownership consolidation,
  AgentLoop decomposition, and an in-process coding frontend.
- [AgentLoop Runtime Host](agentloop-runtime.md): AgentLoop/Pipeline split, inbound scheduling, session claims, recovery, and intentional coupling.
- [Local Coding Agent Roadmap](local-coding-agent-roadmap.md): ordered runtime-boundary, project-thread, coding-profile, terminal UI, compaction, resume, automation, and release work for a native local coding agent.
- [Local Coding Agent P2.2 Project Instructions](local-coding-agent-p2-project-instructions.md): one-file AGENTS/CLAUDE fallback selection, root-to-cwd scope precedence, bounded late-discovery barriers, cache invalidation, and symlink safety.
- [Local Coding Agent P2.3 Workspace Snapshots](local-coding-agent-p2-workspace-snapshots.md): bounded deterministic
  Git observations, prompt freshness, post-write refresh, and frontend repository-state updates.
- [Local Coding Agent P2.5 Durable Lifecycle](local-coding-agent-p2-durable-lifecycle.md): accepted-turn durability,
  redacted side-effect start markers, conservative startup repair, and no-replay crash semantics.
- [Local Coding Agent P2.6 Native Turn](local-coding-agent-p2-native-turn.md): native CLI-to-AgentLoop execution,
  provider selection, restart continuation, inspectable failures, and deterministic command-level evidence.
- [Local Coding Agent P2 Exit Record](local-coding-agent-p2-exit.md): packet-by-packet evidence for the completed
  native coding runtime profile and the explicit P3/P4 boundary.
- [Local Coding Agent P3 Exit Record](local-coding-agent-p3-exit.md): merged evidence for the bounded terminal
  event/control plane and the explicit interactive P4 boundary.
- [Local Coding Agent P4.1 Terminal Shell](local-coding-agent-p4-terminal-shell.md): interactive TTY admission,
  alternate-screen lifecycle, bounded final scrollback, revision watches, and restoration evidence.
- [Async Task Delivery](async-task-delivery.md): durable task/completion/delivery model, deliverables, and current source-of-truth boundaries.
- [SubTurn Mechanism](subturn.md): sub-agent coordination, concurrency control, and lifecycle handling.
- [Subagent Model Policy](subagent-model-policy.md): child-run model selection, inherited session override modes, and precedence.
- [Session System](session-system.md): session scope allocation, JSONL persistence, alias compatibility, and migration.
- [Seahorse Reconciliation](seahorse-reconciliation.md): canonical JSONL history, derived Seahorse state, revision watermarks, and recovery invariants.
- [Memory System](memory-system.md): memory layers, source-of-truth boundaries, prompt budgets, mutation semantics, privacy policy, and evaluation contract.
- [Session Goals](session-goals.md): durable per-conversation objectives, command and tool interfaces, prompt injection, and reset semantics.
- [Routing System](routing-system.md): agent dispatch, session policy selection, and light/heavy model routing.
- [Durable Ingress](durable-ingress.md): normalized inbound message spool and restart replay semantics.
- [Durable Outbound Delivery](durable-outbound-delivery.md): canonical outbound ownership, typed channel outcomes, retry-safe restart reconciliation, and explicit ambiguity.
- [Safe Restart And Deploy](safe-restart-and-deploy.md): bounded restart/deploy handoff, shared binary targets, and durability boundaries.
- [Node Companion](node-companion.md): outbound paired capability hosts, transport and identity boundaries, remote execution policy, and the Linux/macOS MVP.
- [Gateway Invocation SQLite Contract](gateway-invocation-sqlite-admission.md): transactional high-volume invocation retention, current-format admission, no-replay, and fail-closed schema boundaries.
- [Node Companion Post-MVP Roadmap](node-companion-roadmap.md): ordered future milestones for explicit owner/root shell and PTY access, administrator file transfer, service management, fleet operations, executors, transports, and additional capabilities.
- [Android Companion Roadmap](android-companion-roadmap.md): native Android
  operator chat and device-node architecture, reliability, permissions,
  distribution profiles, SMS safety, and phased delivery plan.
- [Android Companion A0-N Admission](android-companion-a0n-admission.md):
  admitted P-256 identity, short-lived enrollment, native node foundation,
  durable read-only invocation, validation, and mandatory A4 stop gates.
- [Reliable Browser Capability](browser-capability.md): deployed-state
  investigation, comparative analysis, and target broker/worker architecture
  for safe local, companion, and cloud browser automation.
- [Reliable Browser Capability Roadmap](browser-capability-roadmap.md): ordered
  browser milestones for current-specialist hardening, first-party tools,
  artifacts, human handoff, companion placement, profiles, providers, and
  computer fallback.
- [Browser Capability B0 Admission](browser-capability-b0-admission.md):
  admitted no-blind-replay, exclusive stdio lease, MCP artifact ownership,
  specialist allowlist, diagnostics, deployment evidence, and exact stop
  conditions for stabilizing the current browser specialist.
- [Browser Capability B1 Admission](browser-capability-b1-admission.md):
  admitted local browser broker, typed session and action contracts, runtime
  approval and recovery boundaries, delivery sequence, and completion gates.
- [Browser Capability N1 Admission](browser-capability-n1-admission.md):
  admitted public-web network mode, worker-owned enforcing proxy, redirect and
  DNS-rebinding boundaries, lifecycle rules, and deployed completion gates.
- [Browser Capability N2 Admission](browser-capability-n2-admission.md):
  admitted explicit high-risk any-HTTP mode for public and private network
  destinations without broadening browser actions or other authority.
- [Browser Capability B2 Admission](browser-capability-b2-admission.md):
  admitted retained screenshot and file artifacts, passive readiness
  diagnostics, exclusive human takeover, safe resume, and the prerequisite B1
  consecutive-session repair.
- [Browser Capability B2 Deployment Evidence](../operations/browser-capability-b2-deployment-evidence.md):
  merged revisions, live artifact round-trip, passive diagnostics, human
  handoff and resume, privacy checks, cleanup, health, and residual limits.
- [Browser Capability BF1 Scroll Parity Admission](browser-capability-bf1-scroll-admission.md):
  admitted shared scroll semantics, exact per-target action discovery,
  companion wire authority, deployment order, and completion gates for the
  first post-B3 parity slice.
- [Browser Capability BF1 Click Parity Admission](browser-capability-bf1-click-admission.md):
  admitted shared semantic click authority, explicit approved-action mode,
  companion approval attestation, target revalidation, recovery, rollout, and
  completion gates.
- [Browser Capability BF1 Press And Select Parity Admission](browser-capability-bf1-press-select-admission.md):
  admitted document-scoped keyboard input, bounded semantic option selection,
  companion attestation, no-replay behavior, and cross-placement completion
  gates.
- [Browser Capability BF1 Tabs, Frames, And Popups Admission](browser-capability-bf1-contexts-admission.md):
  admitted bounded document-context discovery and selection, opaque tab and
  frame authority, correlated popup outcomes, lifecycle recovery, and
  cross-placement completion gates.
- [Browser Capability BF1 Protected Fill Parity Admission](browser-capability-bf1-protected-fill-admission.md):
  admitted durable tool-call redaction, transient companion value delivery,
  sensitive-field denial, no-replay behavior, and cross-placement completion
  gates for typed semantic fill.
- [Browser Functional Parity Execution Goal](browser-functional-parity-execution-goal.md):
  selected six-phase BF1/BF2 execution program for shared interaction,
  document-context, protected form, artifact, diagnostic, and large-snapshot
  parity across gateway and companion placements.
- [Browser Functional Parity Phase 5 Deployment Evidence](../operations/browser-functional-parity-phase5-evidence.md):
  merged remaining-interaction slices, live cross-placement file chooser,
  specialist-boundary restoration, cleanup, health, and rollback evidence.
- [Browser Capability BF2 Media, Transfer, Diagnostics, And Snapshot Admission](browser-capability-bf2-media-transfer-diagnostics-admission.md):
  admitted shared page and element screenshots, retained upload and download,
  privacy-safe session diagnostics, reverse companion artifact transfer, and
  large-snapshot production-WSS delivery.
- [Node Companion P0 Capability Contracts](node-companion-p0-contracts.md): admitted scope, bounded discovery schema, effective-policy projection, freshness, redaction, and completion gates for model-visible node capabilities.
- [Node Companion P1 Owner-Control Admission](node-companion-p1-admission.md): admitted owner shell, cancellation, Linux root broker, and interactive terminal contracts with disabled production defaults and exact completion gates.
- [Node Companion P2 File Transfer Admission](node-companion-p2-admission.md): admitted regular-file transfer, gateway spool, path safety, Linux administrator helper, approval, replay, deployment, and mandatory completion gates.
- [Node Companion P3 Typed Service Administration Admission](node-companion-p3-admission.md): completed Linux systemd status, bounded logs, exact approved actions, root-helper isolation, no-replay recovery, deployment, and mandatory stop gates.
- [Inbound Message Relations](inbound-message-relations.md): explicit relation typing for replies, adjacent follow-ups, media-only turns, and platform-native grouping.
- [Runtime Events](runtime-events.md): runtime event envelope, centralized event logging, filters, and examples.
- [Channel Lifecycle](channel-lifecycle.md): conservative channel reload policy, delivery ownership invariants, and the roadmap for any future hot-replacement work.
- [Workspace Temp Directory](workspace-temp.md): standard scratch path, `MINTCLAW_WORKSPACE_TMP`, and where temporary files should go.
- [Media Store Durability](media-store.md): workspace-local media reference recovery, retention semantics, and migration limits.
- [Shellguard](shellguard.md): reusable shell command validation, command classification, permission modes, and path-scope limits.
- [Tool-Loop Stagnation Protection](tool-loop-stagnation.md): warning-first repeated failure and read-only no-progress detection with hash-safe state and events.
- [Passive Diagnostics](passive-diagnostics.md): bounded redacted execution traces for direct human and Codex debugging without runtime coupling.
- [Test Suite Performance Roadmap](test-suite-performance-roadmap.md): measured test-runtime bottlenecks, coverage-preserving remediation phases, and explicit completion criteria.
- [Hook System Guide](hooks/README.md): current hook architecture and protocol details.

## Archived Plans

- [Current Refactoring Audit](current-refactoring-audit.md): superseded July 2026 static audit retained as implementation
  history.
- [Reliability and Refactoring Roadmap](archive/reliability-refactoring-roadmap.md): completed durability, security,
  ownership, provider-contract, and cross-platform verification program.
- [Reliability and Refactoring Roadmap V2](archive/reliability-refactoring-roadmap-v2.md): completed turn-critical
  session mutation, transactional gateway generation, and versioned configuration writer program.
