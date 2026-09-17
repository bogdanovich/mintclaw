# Local Coding Agent P7.2 Exit Record

Roadmap packet: [P7.2 — Optional local coding daemon investigation](local-coding-agent-roadmap.md#p72--optional-local-coding-daemon-investigation).

The merge containing this record closes P7.2. MintClaw does not add a
machine-wide coding daemon. It now has one private, supervised worker process
for one coding task, native thread, immutable authority binding, and execution
root. The worker speaks bounded protocol-v1 JSON Lines only through inherited
stdin/stdout pipes and exits after its task, explicit shutdown, or bounded idle
window.

Local `mintclaw code`, `mintclaw resume`, and `mintclaw code exec` remain
in-process frontends over the native controller. They do not discover, attach
to, or depend on the worker process. Node Companion and Telegram integration
remain P7.4 work after P7.3 establishes isolated worktree ownership.

## Merged implementation

| Boundary | Evidence | Result |
| --- | --- | --- |
| Placement | [#1094](https://github.com/bogdanovich/mintclaw/pull/1094), merge `f1d029d9` | Rejected a resident daemon after startup/resource measurements; admitted one task-scoped child behind a trusted supervisor. |
| Controller steering | [#1119](https://github.com/bogdanovich/mintclaw/pull/1119), merge `46a300b8` | Added an optional typed steering capability, actor serialization, bounded per-turn idempotency, and an explicit steerable interval. |
| Native steering | [#1122](https://github.com/bogdanovich/mintclaw/pull/1122), merge `97bdf395` | Bound steering to the active workspace/session/generation and made admission, durable insertion, terminal sealing, suspension, and cancellation linearizable. |
| Worker protocol | [#1135](https://github.com/bogdanovich/mintclaw/pull/1135), merge `27cd9dcf` | Added closed, bounded protocol-v1 request/response records, version negotiation, immutable initialization identity, stable errors, and required idempotency keys. |
| Semantic events | [#1139](https://github.com/bogdanovich/mintclaw/pull/1139), merge `92f030cb` | Added bounded renderer-neutral snapshots, item revisions, status, questions, context usage, terminal turns, and worker-stop events. |
| Command projection compatibility | [#1143](https://github.com/bogdanovich/mintclaw/pull/1143), merge `e803a973` | Preserved bounded command lifecycle, transcript, duration, ownership, and orphan state in worker snapshots. |
| Server and pipe client | [#1145](https://github.com/bogdanovich/mintclaw/pull/1145), merge `b69eb62f` | Added the single-task server/client control loop, exact question correlation, bounded event retention, idempotent commands, and uncertain-response handling without retry. |
| Runtime authority | [#1147](https://github.com/bogdanovich/mintclaw/pull/1147), merge `661d0621` | Made investigate mode immutable read-only authority and removed command execution, mutation tools, and out-of-root reads from that runtime. |
| Native child composition | [#1148](https://github.com/bogdanovich/mintclaw/pull/1148), merge `5b25d192` | Added the hidden `code _worker` entrypoint, executable pinning, exact new/resume thread admission, writer-lease ownership, and native controller construction. |
| Parent process ownership | [#1155](https://github.com/bogdanovich/mintclaw/pull/1155), merge `8fe0025f` | Added the production launcher/client, build verification, immutable control methods, bounded diagnostics, Windows Job Object containment, and Unix process-group ownership. |
| Recovery and platform proof | [#1158](https://github.com/bogdanovich/mintclaw/pull/1158), merge `03fa77c9` | Added typed terminal classification and real MintClaw binary tests for start, resume, status, steer, interrupt, cancel, crash, disconnect, lease recovery, no replay, and shutdown on Linux and macOS CI. |

## Closed worker contract

One initialized generation is immutably bound to:

- task ID and task-generation ID;
- worker-generation ID;
- native coding thread ID and exact new/resume mode;
- canonical project and execution-root identities;
- investigate or mutate task mode;
- provider profile, model alias, and provider; and
- expected worker executable content hash and protocol revision.

The worker, not its parent, acquires the thread writer lease and owns the
controller, agent loop, provider, transcript, compaction state, and repository
runtime. The parent owns the exact child process, bounded event journal, and
control pipes. It cannot supply another task or generation identity to an
already initialized `Process`; start, snapshot, steer, interrupt, hard cancel,
and shutdown derive their identity from the accepted binding.

There is no listener, socket discovery, HTTP service, PID reattachment, shared
daemon state, or anonymous client. A future discoverable endpoint still
requires the separate reconsideration gate and threat review in the placement
decision.

## Terminal and recovery semantics

`workerprocess.Result.Outcome()` gives a future supervisor one stable
classification:

| Outcome | Required evidence | Supervisor meaning |
| --- | --- | --- |
| `completed` | authenticated `worker.stopped: completed` | The task turn and worker finalization completed. |
| `interrupted` | authenticated `worker.stopped: canceled` | Cancellation is known; retained thread/workspace state remains inspectable. |
| `shutdown` | authenticated `worker.stopped: shutdown` | The idle initialized worker closed on request. |
| `idle_timeout` | authenticated `worker.stopped: idle_timeout` | No turn started before the bounded idle deadline. |
| `failed` | authenticated failed stop with a non-uncertain protocol error | The worker reported a known internal task/finalization failure. |
| `uncertain` | no authenticated stop, or a failed stop carrying `uncertain` | The parent must reconcile retained state and must not replay the accepted operation. |

OS exit status, stderr, or a closed pipe alone never proves task success. A
mutating command whose response may have been lost returns an uncertain error;
the client sends it once and does not retry it. After worker or parent loss,
only an explicit successor operation may launch a new generation in resume
mode. That successor must reacquire the existing thread lease and perform the
native interrupted-lifecycle recovery before accepting a new turn. Opening it
does not call the provider or replay the earlier accepted prompt.

Hard cancellation preserves thread and project state; it does not claim
filesystem rollback. Windows uses a kill-on-close Job Object and waits for the
owned job to drain. Linux and macOS own the worker process group. A deliberately
daemonized Unix process that creates a new session is outside that portable
boundary, matching the current Codex process model; diagnostic descriptors are
force-closed after a bounded deadline and report
`ErrDiagnosticsDrainTimeout`. Stronger arbitrary-descendant containment would
require a separately admitted cgroup, container, or platform supervisor.

## Done-criteria evidence

| P7.2 criterion | Evidence |
| --- | --- |
| Decision-record worker-control criteria pass on Linux and macOS | #1158 builds the real `mintclaw` binary and runs the same integration-tagged lifecycle matrix in Linux Integration Tests and macOS Portability. Both passed on the merge head. |
| Parent start, resume, status, steer, interrupt, and cancel without TUI state | #1145 exposes the complete typed pipe client; #1148 composes the native child; #1155 owns the process; #1158 drives every operation through that production path. |
| Bounded disconnect, crash, malformed input, version mismatch, and cancellation races without blind replay | #1135 and #1145 cover closed schemas, bounds, malformed records, negotiation, request idempotency, and lost responses. #1158 kills the real worker, closes the parent stream, races hard cancel with process loss, proves `uncertain` classification, excludes a live successor, reacquires the released lease, and observes no provider request before explicit resume input. |
| No machine-wide daemon without the reconsideration gate | #1094 records measurements and the rejection. The implementation has inherited pipes and one hidden child command, but no listener or resident service. |
| Foreground and one-shot paths remain supported | #1148 and #1155 leave the frontend composition untouched. Full repository CI continued to exercise the interactive/native packages and P7.1 `code exec` path while the worker matrix ran independently. |

The worker protocol additionally rejects unknown, duplicate, case-aliased,
unsafe-control, malformed UTF-8, and oversized input. Event history and stderr
are independently bounded. Repository diff and MCP projections redact or omit
secret-shaped and oversized content before it reaches the wire.

## Validation and review gates

The P7.2c merge head passed:

- repository-wide formatting and changed-package lint;
- integration-tagged lint for the worker-process package;
- 20 consecutive worker-process test runs and three race runs;
- the complete `pkg/coding/...` suite;
- three consecutive local macOS real-binary lifecycle runs;
- Linux integration-test compilation; and
- GitHub frontend, lint, security, full tests, race, Darwin and Windows
  compilation, macOS portability, Linux integration, and Browser Windows jobs.

Review of the parent process boundary found and closed Windows descendant,
stderr-drain, and Unix process-boundary issues. After four substantive cycles,
the required architecture checkpoint compared current Codex behavior and
rejected timing-based process-table polling as a false containment guarantee.
The final portable contract is the process boundary described above. The P7.2c
review then reported no actionable findings, and both implementation PRs
merged only after clean CI and owner rocket approval.

## What is available now

P7.2 is an internal execution boundary, not a new user command. Operators
should continue to use:

```text
mintclaw code "Inspect this repository"
mintclaw resume
mintclaw code exec "Inspect this repository"
mintclaw code exec resume <thread-id> "Continue the investigation"
```

Developers can run the real worker matrix through
`scripts/run-go-integration-tests.sh`; CI runs it on Linux and macOS. The
hidden `_worker` command is private, accepts only inherited protocol input, and
is not a supported shell interface.

## Next boundary

P7.3 is next: isolated worktree allocation, exclusive ownership, explicit
branch/conflict handoff, and conservative cleanup for multiple coding agents.
P7.4 then composes this completed worker boundary with an admitted project
catalogue, paired-node commands, gateway `CodingTask`, interaction/delivery
projection, and a Telegram canary.

P7.2 does not add Node or gateway task commands, Telegram dispatch, worktree
allocation, automatic Git publication or merge, remote filesystem authority,
or removal of the ACPX/Codex compatibility path. Those remain separately
admitted roadmap work.
