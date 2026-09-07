# Local Coding Agent P6.5 Decision

Roadmap proposal: [P6.5 — Structured code intelligence](local-coding-agent-roadmap.md#p65--structured-code-intelligence-not-admitted).

Status: **not admitted** on 2026-09-06.

MintClaw will not add an LSP client, language-server discovery, language-server
process management, or LSP-backed coding tools under the current roadmap. P6
closes with the completed P6.4 repository review work, and P7.1 is the next
implementation packet.

## Evidence

The decision was re-audited against current reference implementations:

- OpenAI Codex at `19b62211d9f5999f8d74298f78097e8aeb0e3009` has no LSP
  subsystem or LSP model tool. Its coding loop relies on bounded file access,
  search, shell execution, repository evidence, tests, and review instructions.
- Pi at `9767ba275f3e9a5ee0f5c5342249b629ab1b2282` likewise has no LSP
  subsystem.
- Oh My Pi at `a1b254047d12e143b7c6011536e918c6c35c5906` demonstrates the
  real implementation surface: server discovery and configuration, JSON-RPC
  lifecycle, document synchronization, diagnostics, workspace edits, process
  cleanup, and language-specific behavior.
- OpenCode at `e207624c48159b03dbe17dbc8e51bbcf23e72df5` implements LSP but
  documents it as disabled by default and potentially net-negative because of
  synchronization, memory, version, and latency costs. Its automatic server
  installation behavior is also outside MintClaw's project-trust policy.

The existing MintClaw runtime already provides the primitives used by Codex and
Pi: scoped project instructions, bounded read and search, audited edits and
execution, compiler and test invocation, repository status and diff evidence,
and native review. No concrete MintClaw workload has demonstrated that LSP is
needed to make those workflows successful.

## Rationale

An LSP integration is not one additional query tool. It creates another
long-lived process and state protocol whose failures must be diagnosed across
server versions, workspace roots, document revisions, cancellation, crashes,
stale diagnostics, and multi-file edits. Supporting it responsibly would add a
large testing and maintenance surface before the already implemented coding
features have received broad real-world exercise.

The expected benefit does not justify that complexity today. Language-specific
validation remains available through explicit compiler, type-checker, linter,
test, and repository commands, while navigation remains available through the
existing read and search tools.

## Consequences

- Do not add dormant LSP packages, configuration, server detection, downloads,
  subprocesses, model tools, rename support, or workspace-edit plumbing.
- P6 is complete with P6.4; the rejected P6.5 proposal is not an implementation
  dependency or an exit requirement.
- Continue with P7.1, the stable non-interactive `mintclaw code exec` contract.
- Reconsider structured language intelligence only through a new admission
  decision backed by a reproducible user workload where existing tools fail, a
  measured comparison against the baseline, and an explicit lifecycle and
  maintenance budget.
