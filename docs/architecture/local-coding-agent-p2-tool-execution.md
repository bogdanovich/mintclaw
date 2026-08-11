# Local Coding Agent P2.4 Tool Execution

Roadmap packet: P2.4 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

The coding runtime exposes one fixed trusted-local execution catalogue:

- `read_file`
- `list_dir`
- `search_files`
- `write_file`
- `append_file`
- `apply_patch`
- `exec`
- `update_plan`

The first seven tools operate from the coding thread's admitted execution root.
Relative filesystem paths and command working directories resolve from that
root. Absolute paths retain full-host access because local coding sessions are
an explicitly trusted-local mode. This is an execution policy, not an approval
bypass added to the personal gateway: personal agents keep their configured
tool enablement, allow paths, workspace restriction, command guard, and
approval behavior.

The coding instruction gate canonicalizes model-supplied paths against the
invocation cwd before execution. The tools also root direct relative paths so
the execution contract does not depend on the MintClaw process cwd. Repository
instructions can narrow behavior but cannot add tools or alter this trust
boundary.

## Process and environment contract

`exec` starts in the admitted invocation cwd unless a tool call supplies a
different cwd. A relative supplied cwd resolves beneath the execution root; an
absolute cwd is allowed in trusted-local coding mode. Commands inherit the host
process environment and receive MintClaw's tool-routing variables plus the
owner-scoped `MINTCLAW_WORKSPACE_TMP` scratch path. Coding construction clears
configured remote execution, approval, hook, and deny-pattern additions without
mutating the persisted personal configuration.

Synchronous commands run in a process group where the platform supports it.
Timeout or caller cancellation terminates the process tree, waits for the
leader, preserves bounded partial output, and always returns an error result.
An interrupted command is never reported as successful even if process exit
races with cancellation. Background sessions retain their existing owner-local
session manager and shutdown/reaping contract.

## Audit and durability contract

Filesystem mutations continue to emit the existing bounded write audits.
`write_file`, `append_file`, and `apply_patch` therefore drive repository
snapshot refresh without injecting file bodies or full diffs into the prompt.

The native pipeline journals each assistant tool call before invoking it and
then journals the correlated tool result with the same tool-call ID. Closing
and reopening a coding thread from its state root restores those completed
pairs, including bounded command output, to the canonical journal for the
selected context manager to assemble.
P2.4 does not add a pre-side-effect in-flight marker or infer outcomes after an
abnormal exit; crash-safe start/result lifecycle and dangling-call repair are
owned by P2.5.

## Verification contract

A deterministic native-pipeline scenario lists, searches, reads, patches,
writes, appends, and tests a small Go fixture. It closes and reopens the same
coding thread and verifies every correlated result and the test output survive
in the canonical journal. Focused filesystem and exec tests cover relative
execution-root resolution, absolute full-host access, inherited environment,
and cancellation truthfulness. The exact coding catalogue test includes
`append_file`, while the paired personal-profile tests continue to prove that
gateway restrictions and persisted configuration are unchanged.
