# Local coding agent P5.1: coding compaction policy

Status: implementation contract for roadmap item P5.1.

## Decision

Seahorse owns one compaction mechanism with a runtime-selected summary policy.
The zero-value policy remains the personal-agent contract. Coding runtimes
select the versioned `coding-v1` contract at the agent composition root; they
do not fork the context manager or alter user configuration.

Canonical per-thread JSONL remains authoritative and complete. Policy-specific
prompts, output projection, and summaries are derived data in the thread's
Seahorse database. The existing protected recent-turn boundary still decides
which messages remain raw.

## Coding-v1 summary contract

Leaf, aggressive, and condensed prompts require explicit continuation state:

- current objective and repository/worktree/branch state;
- decisions, constraints, blockers, and unresolved questions;
- exact mutated paths and the observed state of each mutation;
- validation status (`passed`, `failed`, `not run`, or `unknown`) with concrete
  failed-check evidence when available;
- artifact references needed to inspect retained output; and
- one next safe action, or `none` when work is complete.

The deterministic fallback uses the same fields and reports validation as
unknown. It directs a resumed agent back to canonical history rather than
inventing state.

## Historical tool-output projection

Before a coding leaf summary is generated, Seahorse clones the already
retention-projected messages and annotates matched tool results with tool name
and persisted outcome. Large successful non-mutation output is reduced to a
deterministic UTF-8-safe head and tail plus its original byte count and any
artifact-reference lines that would otherwise fall in the omitted middle.

The generic byte projection does not elide:

- error, unresolved, or unknown results;
- successful `append_file`, `apply_patch`, `edit_file`, or `write_file`
  mutation evidence; or
- unmatched and structurally ambiguous tool results.

This projection is used only for summary input. It does not run during ordinary
context assembly, modify the source messages, or write back to canonical JSONL
or Seahorse message rows.

## Version and reconciliation

The reconciliation watermark combines the existing Seahorse schema generation
with the selected summary-policy generation. Personal runtimes continue using
the existing generation. `coding-v1` uses a distinct generation, so introducing
or later replacing a coding policy causes only coding derived state to rebuild
from canonical JSONL.

## Verification contract

Tests must establish that:

- personal prompts and tool output retain their prior behavior;
- every coding summary level requests validation status and a next action;
- large successful read and command output is bounded with its outcome, tail
  evidence, and artifact references intact;
- mutation and failure evidence is not generically elided;
- projection never mutates its input; and
- coding runtimes select a generation distinct from personal runtimes.
