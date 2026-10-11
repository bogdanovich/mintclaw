# Local Coding Agent P8: Diagnostic Evidence Inventory

Roadmap packet:
[H1a — Evidence inventory and correlation tests](local-coding-agent-p8-hardening.md#h1a--evidence-inventory-and-correlation-tests).

Status: H1a implementation baseline.

Audit baseline: `origin/main` at
`96d5b9d235886f0e478e6d6444476ca0c98d6acd`, 2026-10-10.

Machine-readable contract:
[`pkg/diagnostictrace/testdata/coding_path_inventory.json`](../../pkg/diagnostictrace/testdata/coding_path_inventory.json).

## Purpose

This inventory maps each supported coding incident path to its authoritative
state, runtime events, passive trace records, stable identifiers, terminal
facts, retention owner, privacy class, and known evidence gaps. A test validates
the schema, event and trace-kind names, identifier coverage, and explicit
disposition of every gap.

The inventory is not a runtime registry and is never read by production code.
It describes how an investigator can join existing evidence and freezes the
minimum H1b work needed to make that route reliable.

## Findings

MintClaw already captures the core single-turn timeline well:

- turn start/end and bounded outcome;
- model request/response, retries, fallback attempts, and token/cache facts;
- tool call/result/skip and loop-protection decisions;
- steering, interrupt, compaction, final context identity, and runtime error;
- channel delivery attempt/outcome; and
- bounded passive-storage policy, truncation, drop, retention, and corruption
  truth.

The durable owners also retain strong identities outside the trace: coding
thread and turn IDs, task and generation IDs, worker generation, interaction
and question revisions, gateway invocation IDs, delivery IDs, worktree IDs,
branches, and repository observations.

The problem is correlation rather than a lack of state. Several identifiers
are available in an event payload or durable owner but are discarded before
the passive trace is written. The current bundled trace-debug instructions
therefore have to correlate some incidents by time windows and external store
inspection.

## H1b priority gaps

The following gaps must close in the existing event/trace pipeline:

1. **Model attempts.** Request, response, retry, and fallback records need one
   runtime-generated attempt identity. A provider request ID is retained when
   safely available but cannot be required from every provider.
2. **Tool-loop decisions.** The decision must identify the relevant tool-call
   generation when one exists, without using argument text.
3. **Async task and delivery.** `AsyncCompletionPayload` already owns task and
   completion IDs, and `ChannelOutboundPayload` already owns a delivery ID;
   passive projection must retain their bounded correlation.
4. **Interactions.** Existing interaction runtime events carry interaction ID,
   revision, event, status, outcome, code, and success, but the turn projector
   currently ignores them. H1b needs bounded lifecycle records and joins, not a
   copy of the interaction registry.
5. **Node and companion invocations.** The runtime event contains the gateway
   invocation ID and safe command/target facts. The current trace hashes the ID
   into a result digest, which cannot join the authoritative invocation store.
6. **Remote coding generations.** Task, task generation, worker generation,
   remote thread, scope/profile, and question identities remain authoritative
   in task/worker/interaction stores. The parent turn trace needs safe
   references for admission and terminal delivery.
7. **Compaction and repository observations.** The coding runtime owns
   compaction attempt IDs and deterministic repository snapshots, while the
   passive trace currently records only the coarser context outcome. H1b must
   retain bounded attempt/snapshot identity and terminal status without paths
   or diff content.

These are additive passive joins. None may make the runtime wait for or depend
on trace persistence.

## Deliberate external-authority boundaries

Some detail should not move into diagnostic traces:

- canonical conversation and complete tool pairing stay in coding JSONL;
- exact task, interaction, invocation, worker, and delivery state stays in its
  owning ledger;
- full remote worker events and transcripts stay on the companion;
- repository paths, diffs, source, commands, output, environment, credentials,
  and process details stay in their existing private owners; and
- exact TUI cells, raw streaming deltas, and viewport history are not retained
  as a production trace.

The trace carries only enough identity and status to select the right owner.
H1c may export bounded metadata summaries from those owners, but collection is
read-only and privacy-safe by default.

## Production qualification finding and H1b resolution

A harmless stateless deployment canary completed successfully and retained
both a `turn.end` record with status `completed` and a completed envelope
outcome. The same trace was nevertheless marked incomplete with
`runtime_closed_before_terminal_outcome`. The content and runtime outcome were
not lost, but the truncation claim conflicts with the retained terminal facts.

The H1b terminal-truth slice resolves that contradiction at both ownership
boundaries. Direct and coding callers own presentation of the returned final
response, so their turn-end event no longer claims that a channel delivery is
pending. A gateway turn that has ended but is still awaiting a channel receipt
remains open, and shutdown labels it
`runtime_closed_before_delivery_settlement` instead of claiming that the
terminal outcome is absent. An active turn with no turn-end event retains
`runtime_closed_before_terminal_outcome`.

Regression tests exercise all three states plus successful channel settlement,
and the machine-readable inventory no longer carries `terminal_consistency` as
an open lifecycle gap. H6 still requires a merged-main production canary; an
older deployed binary's contradictory marker remains authoritative evidence
for that older run.

## Evidence map

| Path | Authoritative evidence | Passive evidence | H1b/H1c action |
| --- | --- | --- | --- |
| Local coding turn | Coding JSONL, metadata, Git/filesystem | Turn/model/tool/context/error records | Add model-attempt and thread/project joins |
| Provider retry/fallback | Runtime provider outcome | Model retry/fallback records | Correlate one runtime attempt across request/response/retry/fallback |
| Tool/process lifecycle | Tool record, process owner | Tool call/result/skip/loop records | Add loop-decision call/generation join; keep PID/process details external |
| Compaction/resume | Coding JSONL, Seahorse, metadata | Context compaction/snapshot | Add attempt/status and bounded repository re-anchor identity |
| Human interaction | Interaction registry/events | No interaction trace records today | Project bounded lifecycle and interaction/revision correlation |
| Channel/async delivery | Task registry, outbox, channel receipt | Delivery decision/attempt/outcome | Retain task, completion, and delivery IDs safely |
| Remote coding task | Task registry, worker ledger/thread | Parent async/delivery evidence | Add task/generation/worker/thread references and terminal uncertainty |
| Companion capability | Gateway invocation store, node ledger | Node observation as tool result | Retain invocation ID and safe policy/target aliases |
| Repository/worktree | Git/filesystem, worktree owner | Context snapshot only | Add commit/worktree/snapshot digests and counts; no paths or diffs |
| TUI/headless projection | Canonical transcript, deterministic projector | Turn/tool/context events | H1c exports bounded current-view/version facts, not cell history |
| Trace pipeline health | Trace envelope, writer counters/logs | Truncation/drop reasons | H1b fixes terminal consistency; H1c summarizes disabled/drop/expiry/corruption truth |

## H1a exit assessment

The supported paths have an authoritative evidence owner and a terminal or
explicit uncertainty source. The machine-readable inventory names every
currently missing passive join and assigns it to H1b or H1c; the first audited
lifecycle gap, terminal consistency, is protected by focused H1b regressions.
H1a therefore does not add a new event system, trace schema, runtime dependency,
or operator command; those changes proceed only from this audited list.
