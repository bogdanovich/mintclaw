# Local Coding Agent P5 Exit Record

Roadmap gate: [P5 — Coding compaction and resume continuity](local-coding-agent-roadmap.md#p5--coding-compaction-and-resume-continuity).

The merge containing this record completes the six ordered P5 packets. The
local coding agent now meets the roadmap's minimum beta gate for multi-hour and
multi-day thread continuity. Canonical per-thread JSONL remains authoritative;
Seahorse summaries, indexes, watermarks, and checkpoints remain disposable
derived state.

## Completion evidence

| Packet | Merged change | Contract evidence |
| --- | --- | --- |
| P5.1 | [#903](https://github.com/bogdanovich/mintclaw/pull/903), merge `8fb6597a` | Coding runtimes select the versioned `coding-v1` summary policy without changing personal-agent behavior. The policy preserves objectives, decisions, paths, validation truth, blockers, next action, tool batches, failure evidence, media/artifact references, and bounded historical tool output. Policy-generation changes rebuild derived state from canonical history. |
| P5.2 | [#905](https://github.com/bogdanovich/mintclaw/pull/905), merge `45069a14` | Every compacted or resumed coding prompt places a fresh bounded workspace observation after model-generated summary state. Synthetic git fixtures prove external branch/file changes, provider-retry mutations, capture failure, and tool-driven changes; current repository state supersedes stale summary claims. |
| P5.3 | [#906](https://github.com/bogdanovich/mintclaw/pull/906), merge `55a656df` | Proactive, post-turn, emergency, and manual triggers share one typed lifecycle and per-thread ownership boundary. Background work deduplicates; manual/emergency work serializes against transcript mutation; terminal events precede the next same-thread attempt; cancellation, no-progress, partial failure, and panic release ownership without touching canonical history. |
| P5.4 | [#908](https://github.com/bogdanovich/mintclaw/pull/908), merge `33bd7e14`; recovery follow-up [#925](https://github.com/bogdanovich/mintclaw/pull/925), merge `beceb6f2` | Resume reconciles one stable canonical revision before returning a controller. Missing, corrupt, outdated, interrupted, or incompletely checkpointed derived state is rebuilt from JSONL. Incomplete tool calls remain inspectable as unknown/interrupted and are never replayed as side effects. Construction and checkpoint ownership were subsequently narrowed in [#924](https://github.com/bogdanovich/mintclaw/pull/924) and [#926](https://github.com/bogdanovich/mintclaw/pull/926). |
| P5.5 | [#922](https://github.com/bogdanovich/mintclaw/pull/922), merge `ff7ee278` | Seahorse metrics flow through one correlated frontend lifecycle. The TUI distinguishes blocking from background work, remains usable during safe background compaction, reports trigger/tokens/summaries/duration/outcome, gives continuation guidance, and converges on one terminal result for manual compaction. |
| P5.6 | [#928](https://github.com/bogdanovich/mintclaw/pull/928), merge `8844980f` | A deterministic 32-turn/64-message harness compacts more than 107,000 tokens through depth 2, reopens, deletes derived DB/WAL/SHM, rebuilds and re-compacts from JSONL, and retains summary-only semantic markers plus an ordered tool pair exactly once. It also proves strict independent budgets, thread isolation, real deadline cancellation, exact lifecycle delivery, and timing-independent baselines. The complete clause-to-test matrix is in the [P5.6 evaluation record](local-coding-agent-p5-long-session-evaluation.md). |

Every implementation packet passed the repository's final nine-check matrix:
linter, security, tests, race, Darwin and Windows compilation, macOS
portability, integration tests, and browser tests. Automated review findings
were fixed with focused regressions, re-reviewed on the final packet head, and
resolved before merge.

## Authority and continuation architecture

P5 exits with one continuity model rather than a second coding transcript:

- Canonical JSONL stores the complete per-thread message and tool history. A
  successful derived write never authorizes dropping or rewriting canonical
  records.
- Seahorse is opened with the coding runtime's construction context, reconciles
  against a versioned canonical generation, and may be deleted and rebuilt.
- Coding summary policy carries historical intent and explicit epistemic state:
  passed, failed, not run, and unknown remain distinct across condensation.
- The protected complete-turn tail and tool-aware projection keep provider
  history valid. Tool calls and matching results remain ordered and paired.
- A fresh deterministic workspace snapshot is appended after model summary
  context, so live git/filesystem observations win every conflict.
- Agent lifecycle events are the sole source for frontend compaction state.
  The TUI does not own a summary, checkpoint, retry loop, or compaction worker.

## Exit-gate decision

The P5 beta statement is satisfied:

- a long coding thread remains within independent history, summary, and total
  context budgets while preserving its objective, done criteria, changed
  paths, decisions, constraints, validation truth, failure, and next action;
- proactive and post-turn compaction may run in the background without blocking
  the composer, while emergency and manual compaction have an explicit blocking
  lifecycle and bounded terminal outcome;
- at least two summary-condensation levels preserve the coding continuation
  contract under deterministic large-history pressure;
- process reopen and complete loss of derived state reconstruct continuity from
  files without duplicating a tool side effect or promoting an incomplete tool
  result to success;
- current repository state supersedes historical summary text after external or
  tool-driven mutation; and
- users can see whether compaction is running, blocking, completed, made no
  progress, was interrupted, or failed, with truthful continuation guidance.

This is the minimum beta gate for the local coding agent. It is not a claim that
the remaining roadmap is complete.

## Deliberate P6 and later boundary

The following remain explicitly outside P5:

- P6 owns thread rename/archive/delete/fork completion, richer diffs, and
  additional coding UX rather than continuity correctness.
- P7 owns stable non-interactive `code exec` and machine-readable event/output
  contracts.
- P8 owns coding-task dispatch and explicit paired-node execution. P5 does not
  allow a gateway chat agent to infer a laptop workspace or turn the local TUI
  into a remote worker.
- Broader sandbox and approval modes are not required for the trusted-local
  default. Any future untrusted execution profile requires its own admission.

P6 may proceed from this beta continuity baseline without changing canonical
history authority, live-workspace precedence, or frontend ownership.
