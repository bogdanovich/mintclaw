# Local Coding Agent P8: Diagnostic Evidence Inventory

Roadmap packet:
[H1a — Evidence inventory and correlation tests](local-coding-agent-p8-hardening.md#h1a--evidence-inventory-and-correlation-tests).

Status: H1a baseline with the H1b terminal-truth, model-attempt, tool-call
generation, async task/delivery, and human-interaction slices implemented and
production-qualified. Companion-invocation correlation is also implemented and
production-qualified. Remote-coding correlation is implemented; its production
qualification is pending. Compaction-attempt and repository-snapshot
correlation are implemented; production qualification is pending.

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

The following gaps drive H1b work in the existing event/trace pipeline:

1. **Model attempts (resolved).** Request, response, retry, and fallback
   records now share one runtime-generated identity for each logical provider
   attempt. Candidate-level fallbacks retain that identity; an outer retry gets
   a new one. A provider request ID remains separate and optional.
2. **Tool-loop decisions (resolved).** Every normalized tool call now receives
   a runtime-owned opaque generation ID. Start, progress, result, skip, and
   loop-decision events retain that generation together with the provider call
   ID and originating model-attempt ID. Reuse of a provider call ID therefore
   cannot join records from separate attempts, and no argument text is needed.
3. **Async task and delivery (implemented and production-qualified).**
   `AsyncCompletionPayload` retains the task ID, runtime-owned task
   generation, and completion ID, while `ChannelOutboundPayload` retains a
   durable delivery ID when an outbound transaction assigns one. Because a
   background task can finish after its parent turn trace is finalized, each
   completion now starts a bounded continuation trace. Its envelope points to
   the original turn through `parent_turn_id`, and its decision record retains
   the originating model attempt and exact tool call/generation. A direct
   user-only continuation waits at most 30 seconds for its correlated channel
   outcome. A completion that requires parent synthesis remains bounded by the
   120-second synthesis deadline plus the 30-second delivery window; any
   earlier direct user message is correlated but only the final synthesized
   outbound settles the trace.
4. **Interactions (implemented and production-qualified).** Existing
   interaction runtime events now produce bounded `interaction.lifecycle`
   records with exact interaction/event ID, revision, task, originating tool
   call, and prompt/final delivery joins. Events observed while the originating
   turn is active remain in that trace. A later answer, resume, final delivery,
   cancellation, timeout, failure, or terminal outcome receives an immediate
   one-record continuation trace linked by `metadata.parent_turn_id`; the
   projector never holds a trace open for the human wait. Question, answer,
   approval, failure-detail, and execution-context content remain only in the
   authoritative interaction registry.
5. **Node and companion invocations (implemented and production-qualified).**
   Each passive observation now retains the exact gateway
   invocation ID, configured target and applicable scope/profile aliases, and
   the execution-policy revision. The trace projects those facts into bounded
   correlation/scope fields, so an investigator can select the gateway store
   and companion ledger record without command, input, output, path, node
   identity, or authority-digest content. Success, failure, cancellation,
   timeout, disconnect/uncertainty, and later status observations remain
   distinguishable through observation, state, and safe error code.
6. **Remote coding generations (implemented; production qualification
   pending).** A `coding_task` result now projects exact task and task-generation
   identity into the parent `tool.result`. Terminal gateway delivery creates a
   bounded completion continuation that retains the same task/generation,
   worker generation, remote thread, safe configured scope/profile aliases,
   terminal task and node states, failure code, completion ID, and final
   delivery receipt. Remote objectives, summaries, transcripts, commands,
   paths, diffs, and artifacts remain only in their authoritative owners. A
   remote monitor may outlive or recover independently of its initiating turn,
   so task plus generation is the exact parent/terminal join; correlation does
   not depend on time matching or an invented parent-turn identity.
7. **Compaction and repository observations (implemented; production
   qualification pending).** Active-turn and standalone compaction lifecycle
   records retain the runtime-owned attempt ID, safe status, revision, counts,
   duration, and terminal outcome. A manual or background compaction outside an
   active turn receives a bounded continuation trace that closes on the end
   event. Repository snapshot records retain the deterministic snapshot hash,
   validated Git commit, and bounded counts/flags. Paths, branch names, changed
   filenames, warnings, diff content, and raw errors remain external.

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
an open lifecycle gap.

Merged-main production qualification completed on 2026-10-11 at
`756ea80a257bbc298117f3f8b8e41cb1fb0e41c1` after PR #1496 merged. The direct
stateless canary retained trace `trace-turn-257fb60524a6220f73eb07c6` with a
completed outcome, six records ending in `turn.end`, no delivery records, and
no truncation. The live gateway canary retained
`trace-turn-1b3488bbdd51e41237b41fc6` with a completed outcome followed by
`delivery.attempt` (`queued`) and `delivery.outcome` (`sent`), again without
truncation. All expected services remained active and the ten-minute
error-journal scan was empty after both canaries.

The checksum-verifiable rollback snapshot is
`/home/server/mintclaw-core-backup-20261011T025349Z`; it records the previous
core revision `9aef97d8956056fc4a059bdd3e029495f16bdde9`, binaries, user units and
drop-ins, service states, and a SHA-256 manifest. Historical traces from the
older binary remain authoritative evidence for their own runs. H6 still owns
the wider final P8 qualification matrix, but terminal and delivery settlement
truth is production-qualified.

### Model-attempt production qualification

The model-attempt slice merged through PR #1501 and was deployed from merged
main `9520fd555d5bef1c19ad736b318681be275c9b1b` on 2026-10-11. The direct
stateless canary retained trace `trace-turn-01809dbcdd7e483554c8161c`; its
`model.request` and `model.response` records shared one non-empty
`model_attempt_id`, the completed trace had no delivery records, and it was not
truncated. The live gateway canary retained
`trace-turn-ab2be4292b937086b3b35ca4`; one ID joined `model.request`, a
successful `model.fallback_attempt`, and `model.response`, followed by
`delivery.attempt` (`queued`) and `delivery.outcome` (`sent`). The trace was
completed and not truncated.

All expected services remained active after both canaries, no failed or legacy
processes were present, and the ten-minute error-journal scan was empty. The
installed core, node, and launcher SHA-256 digests are respectively
`e2f588644a72fde6a1980a3d90705023011da4a5253d95c03e1899dd6dc03616`,
`06a2cc4da959e2495682434569122042cdfc5e61bf947ea7ff6119207c6c544a`, and
`ce292c2405d2b5eed5b7c7902d69ff2b4f16370e78e08eadfc9f5d5b1527e99f`.
The checksum-verifiable rollback snapshot is
`/home/server/mintclaw-core-backup-20261011T043410Z`; it records previous core
revision `756ea80a257bbc298117f3f8b8e41cb1fb0e41c1`, binaries, units and
drop-ins, affected service states, symlinks, and a SHA-256 manifest.

Both new traces still have an absent `source.commit`. This independently
confirms that deployment-commit attribution remains an open H1b gap; the
successful model-attempt qualification does not relabel that missing evidence
as resolved.

### Tool-call generation production qualification

The tool-call generation slice merged through PR #1517 as merge commit
`422300d7c5401651da945ff5fcd11ceffa987b77` and was deployed from merged main
on 2026-10-11. The deployment advanced the production checkout from
`c43e4b7cde9fdde44bf938226ad8de969f103b8e` to that merge commit. Before the
update, the checksum-verifiable rollback snapshot
`/home/server/mintclaw-core-backup-20261011T055004Z` captured 42 files,
including the prior binaries, user units and drop-ins, service states,
symlinks, and source revision; every entry in its SHA-256 manifest verifies.

The installed gateway/CLI, node, and launcher SHA-256 digests are respectively
`d7c73684a5423dd28907331df8f81c047df6aa7a9e9d39e86ff73649381fda2c`,
`3f492408bb9638d757f327d80b30faac52268f1cf97d4c4594c4105f646c4990`, and
`da4c76f789d88a939da41338bbd9589af86abfea01daca84009a382c93c7211d`.
The gateway build and CLI installation have the same digest. All ten expected
services remained active, no failed or legacy processes were present, launcher
and reviewer-webhook probes returned their expected 302 and 404 responses, and
the final ten-minute error-journal scan was empty.

The basic stateless canary retained completed trace
`trace-turn-f506594effe76e7e2c2a9d7e` with six records, redacted-content policy,
and no truncation. A separate harmless read-only repeated-tool canary retained
completed trace `trace-turn-d03727df4cc92724f1960c84` with 22 records,
redacted-content policy, and no truncation. In the latter trace, the second
relevant tool call at sequence 15, its `identical_call_success_warning`
loop-decision record at sequence 16, and its result at sequence 17 share the
exact provider call ID, runtime-owned tool-call generation, and originating
model-attempt ID. The preceding repeated call at sequences 10 and 11 has a
different generation and model-attempt ID, proving that repeated successful
calls cannot be joined merely because they invoke the same tool.

Both qualification traces still have an absent `source.commit`. Deployment
commit attribution therefore remains an independent open H1b gap; the tool
call, generation, model-attempt, and loop-decision joins are qualified without
claiming that the source-commit gap is resolved.

### Async-completion production qualification

The first async-correlation deployment merged through PR #1524 as merge commit
`f753c03dd55c1eb5cdc202e38172d2342ff6e5cc`. A harmless live `spawn` canary
proved the new task ID, task generation, completion ID, tool-call generation,
and model-attempt fields in the runtime journal and task registry. It also
found that the completion arrived about five seconds after the original turn
trace had finalized, so the passive trace could not retain the decision. This
was a trace-lifecycle gap, not an execution or delivery failure.

The follow-up implementation gives every async completion its own bounded
continuation trace instead of retaining the parent trace indefinitely. This
preserves passive one-way capture, bounds user-delivery settlement, and remains
valid when a background task outlives its originating turn.

The continuation fix merged through PR #1532 as merge commit
`2baad0f755c0fc1ac3de48a76b4a25be887cd602` and was deployed from that exact
merged-main revision on 2026-10-11. The deployment advanced production from
`f753c03dd55c1eb5cdc202e38172d2342ff6e5cc`. The checksum-verifiable rollback
snapshot is `/home/server/mintclaw-core-backup-20261011T084555Z`; its manifest
contains 27 files and verifies with SHA-256 digest
`12c2750c9b9a2c3d8be6fced01a338645ed2bdd57ff62de4e718216cec434bd2`.

The installed core/CLI, node, and launcher SHA-256 digests are respectively
`247375a2c54e4b180f53924ad98ee2b941633faa3cb1eae48aba7b863fc9eaa6`,
`c104729b6b470711b7c2637dcf4784b3fb00ae2c3f4b40fd81cd6e8795224a6a`, and
`2af9e3073801955948640eda382335763e1265f93d4bf3fe3ad857b08f14bd6b`.
All five active profile configurations loaded successfully; their doctor exit
code `2` represented existing policy findings rather than a load or schema
failure. All ten expected services were active, no failed or legacy processes
were present, launcher and reviewer-webhook probes returned their expected 302
and 404 responses, and the final ten-minute error-journal scan was empty.

The direct stateless deployment canary retained completed trace
`trace-turn-f304b5c1e684500dcd47ea65` with six records, redacted-content policy,
and no truncation. Two live async canaries then qualified the continuation
lifecycle:

- Parent-only task `subagent-a30fcbe8-649e-472b-9869-acb0dcf3ce05`, generation
  `2d34edd8-6c17-40e9-bdb4-4a8e64bf964e`, completed successfully and queued its
  result for parent synthesis. Continuation trace
  `trace-async-completion-bc67595f3108469414827bb5` links
  `async-completion-2601563b-03c4-4990-bcf5-036ca1e48fae` back to
  `main-turn-6`, retains the exact model attempt, tool call/generation, task
  generation, and completion ID, then records the synthesized final delivery
  as `queued` and `sent`.
- User-and-parent task `subagent-b0c6b93e-7baf-4de1-a6b1-2b1b8743ed2f`,
  generation `b691c92f-c13d-482b-ab02-d5ef3cc2695f`, completed successfully
  with durable delivery status `delivered`. Continuation trace
  `trace-async-completion-eabcb05d80d64acd2482ff85` links
  `async-completion-40f75514-e557-49f7-a885-48e059f7059d` back to
  `main-turn-12` and contains five ordered records: the correlated decision,
  the direct user `queued`/`sent` pair, and the synthesized final
  `queued`/`sent` pair. Persisting both pairs proves that the intermediate user
  delivery did not settle the continuation before final parent synthesis.

The live channel did not assign outbox delivery IDs to these post-turn sends.
Their stable join is the continuation scope plus each outbound event ID, while
the task, generation, and completion IDs select the authoritative task record.
The trace still retains a durable delivery ID when one is supplied by an
outbound transaction; the qualification does not claim that every transport
creates one.

The one-shot live client normally disconnects after the initiating turn's
terminal response. An initial parent-only probe therefore produced a truthful
final `failed` delivery outcome because no client remained connected. The two
qualifying canaries kept a second non-mutating live turn attached to the same
session while the child completed, allowing both final deliveries to reach a
real client. This negative probe is retained as expected transport evidence;
it was not counted as a successful-delivery qualification.

### Human-interaction production qualification

The human-interaction slice merged through PR #1537 as merge commit
`3b9caf108bfa8bb2590dd20d7764eacf275088a9` and was deployed from that exact
merged-main revision on 2026-10-11. The deployment advanced production from
`2baad0f755c0fc1ac3de48a76b4a25be887cd602`. The checksum-verifiable rollback
snapshot is `/home/server/mintclaw-core-backup-20261011T110318Z`; its manifest
contains 32 files and verifies with SHA-256 digest
`b212dcbd95abe434fb33c14b81bf74044bc4d28064e8622950e86d7511367dba`.

The installed core/CLI, node, and launcher SHA-256 digests are respectively
`f3004e04632984ba91eb17c495891ef18080da8fa20f668010c74a049cf4cb5c`,
`4830af8be247aa953033423605d8f0ec2c99768a6cf72054b5bfa217ac358962`, and
`0053e44aa1fed4453bfc4aea86eea9d3a630c9e1334100979f57b1cd449f06e3`.
All five active profile configurations loaded successfully; their doctor exit
code `2` represented existing policy findings rather than a load or schema
failure. All ten expected services were active, no failed or legacy processes
were present, launcher and reviewer-webhook probes returned their expected 302
and 404 responses, and the final ten-minute error-journal scan was empty.

A harmless live canary created interaction
`interaction_df22b7bad4ab3c844ebd420c79dc1fa2` from `request_user_input` in
`main-turn-4`. Parent trace `trace-turn-951d81086943e7ab93ab6d48` retained the
ordered `created`, `prompt_delivery_bound`, and `waiting` lifecycle records and
then truthfully finalized as suspended. Four immediate one-record continuation
traces retained the late lifecycle without holding a trace open during the
human wait:

- `trace-interaction-2d58cc5b6a1cf0f7d55efa14` retained
  `answer_claimed`;
- `trace-interaction-8acb83ca5ce116838e408fb1` retained
  `resume_started`;
- `trace-interaction-07235d65f80848b6495d31a1` retained
  `final_delivery_bound`; and
- `trace-interaction-f55cdc019f937df566c98d45` retained `resolved`.

Every lifecycle record shares the exact interaction and originating tool-call
IDs. Revisions, event IDs, and sequences advance monotonically from one through
seven; each continuation envelope links back to `main-turn-4` through
`metadata.parent_turn_id`. The prompt and final delivery joins resolve to
outbox intents `out_136dc5d93cdd59e6ef0880ca5e02a465` and
`out_1698b0ad1a35b3f89c670431080c677c`; both were delivered in one attempt.
The authoritative registry independently ended at revision and event sequence
seven with status `resolved`, outcome `answered`, one resume attempt, and no
resume or failure detail.

All five traces use `redacted_content`, name the configured redactor, report no
truncation, and contain no question, answer, approval, execution-context, or
failure-detail payload. A direct stateless probe was excluded from
qualification because its standalone SSH process did not inherit the gateway
service's provider-refresh environment; the live gateway canary and passive
evidence above are the qualifying path.

### Companion-invocation production qualification

The first harmless companion canary after PR #1554 proved the exact gateway
invocation and companion-ledger join, but its passive turn trace retained only
the ordinary `nodes` and `nodes_invoke` tool records. The specialized node
observations were published under the durable tool execution identity instead
of the active trace turn identity, so the projector could not attach them to
the turn. This was a passive-correlation defect; the remote command completed
successfully and both authoritative ledgers were correct.

PR #1556 separated those identities. Node event publishers now use the active
turn ID for passive trace scope while preserving the execution ID for durable
authorization and recovery; standalone tool execution retains the execution-ID
fallback. The fix merged as
`dae7fac0324d0354ee79de44d55ad1450c8d31cc` and was deployed from that exact
merged-main revision on 2026-10-11. The checksum-verifiable rollback snapshot
is `/home/server/mintclaw-core-backup-20261011T150238Z`; its SHA-256 manifest
digest is
`1a47a66febff1606e7fb021d3b0fc859db9a3ca583b93b136db5114077996be2`.

The installed core/CLI, node, and launcher SHA-256 digests are respectively
`d2b01e84003d5584a766352cf298a1fe7042cc4358d537051c575445269f724d`,
`1c1bbd1f0208edb95b3e71b3d593f017b481547a9aa67f5d7edf01a38d47e428`, and
`31f0d1125b63d7592caa4c7aec5a0b07e2a2d1e95356c9505f259b1b41d787eb`.
All five active profile doctor results were byte-identical to the previous
binary's existing policy findings. All ten expected services were active, no
failed or legacy processes were present, launcher and reviewer-webhook probes
returned their expected 302 and 404 responses, and the ten-minute service
error-journal scan was empty.

A live gateway deployment canary completed with trace
`trace-turn-4e93e5ee3f001e5477b6b9ab`: schema v1, eight records, completed
outcome, `redacted_content`, and no truncation. A separate one-shot companion
canary then created invocation
`inv_3cc23f93a4eaaab19154a5d1eb1b3a1b25bd106fad92bd750300e2dc51de0586`
for configured target `ab-2`. Trace
`trace-turn-3ec0fbebf110ad2df0e2be14` retained three ordered node observations:
`prepared`, `dispatched`, and terminal `succeeded`. All three carry the exact
invocation ID, originating provider tool-call ID, configured target and scope
aliases, and policy revision
`local-test-automation-v15-owner-shell-v1`.

The invocation ID selects exactly one gateway SQLite row and exactly one
companion-ledger record. The gateway row retains its durable hashed tool-call
identity and `dispatched` handoff state; the companion is the terminal owner
and records `succeeded`, exit code zero, exact expected stdout, empty stderr,
and no truncation. The three correlated trace records contain only bounded
tool/action/status hashes and aliases: they contain no command, input, output,
path, node identity, or authority digest. The trace envelope's `source.commit`
is still absent, so deployment-commit attribution remains an independent open
H1b gap rather than being inferred from this deployment record.

## Evidence map

| Path | Authoritative evidence | Passive evidence | H1b/H1c action |
| --- | --- | --- | --- |
| Local coding turn | Coding JSONL, metadata, Git/filesystem | Turn/model/tool/context/error records | Model-attempt join complete; add thread/project joins |
| Provider retry/fallback | Runtime provider outcome | Model retry/fallback records | Model-attempt join complete; retain optional provider request ID separately |
| Tool/process lifecycle | Tool record, process owner | Tool call/result/skip/loop records | Call/generation join complete; keep PID/process details external |
| Compaction/resume | Coding JSONL, Seahorse, metadata | Context compaction/snapshot | Attempt/status and bounded repository re-anchor identity implemented; production qualification pending |
| Human interaction | Interaction registry/events | Bounded lifecycle records in the parent trace or immediate late-event continuation | Interaction/event ID, revision, task, tool-call, and delivery joins implemented and production-qualified |
| Channel/async delivery | Task registry, outbox, channel receipt | Bounded completion continuation with delivery decision/attempt/outcome | Task/generation, completion, tool-generation, parent-turn, and delivery joins implemented and production-qualified |
| Remote coding task | Task registry, worker ledger/thread | Parent tool result plus bounded terminal completion/delivery continuation | Task/generation/worker/thread, safe scope/profile, terminal uncertainty, completion, and delivery joins implemented; production qualification pending |
| Companion capability | Gateway invocation store, node ledger | Node observation as tool result | Invocation ID and safe policy/target aliases implemented and production-qualified |
| Repository/worktree | Git/filesystem, worktree owner | Bounded repository context snapshot | Commit/snapshot digest and counts implemented without paths or diffs; production qualification pending |
| TUI/headless projection | Canonical transcript, deterministic projector | Turn/tool/context events | H1c exports bounded current-view/version facts, not cell history |
| Trace pipeline health | Trace envelope, writer counters/logs | Truncation/drop reasons | H1b fixes terminal consistency; H1c summarizes disabled/drop/expiry/corruption truth |

## H1a exit assessment

The supported paths have an authoritative evidence owner and a terminal or
explicit uncertainty source. The machine-readable inventory names every
currently missing passive join and assigns it to H1b or H1c; the first audited
lifecycle gap, terminal consistency, is protected by focused H1b regressions.
H1a therefore does not add a new event system, trace schema, runtime dependency,
or operator command; those changes proceed only from this audited list.
