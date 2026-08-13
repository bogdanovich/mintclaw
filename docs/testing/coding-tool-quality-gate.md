# Coding tool quality gate

MintClaw evaluates its native coding tools with deterministic repository
fixtures before freezing the P3 terminal presentation. The gate calls the
production `read_file`, `search_files`, `apply_patch`, `write_file`, and coding
`exec` implementations; it does not substitute stub tools or invoke a live
model.

## Coverage

The same scenario runs against two materially different provider-facing
tool-call forms:

- object arguments, matching providers that expose a decoded input object;
- function JSON, matching OpenAI-compatible function calls whose arguments
  cross a JSON string boundary.

Each form runs over both an 11-file small fixture and a 411-file large fixture.
The assertions and emitted `mintclaw.coding-quality.v1` report cover:

- first-attempt patch correctness and verified write audit behavior;
- rejection of a stale patch after an external concurrent edit;
- exact search-result set equality while ignored generated files and binary
  content contain the same needle;
- bounded byte and estimated-token volume for search and command results;
- Unicode write/read behavior, a path containing spaces and Unicode, and a
  120 KB single-line read;
- renamed and deleted paths;
- command cancellation, process settlement, and a successful next command;
- preservation of full oversized coding command output as a `0600` artifact
  under the thread-owned runtime scratch directory, with deterministic
  seven-day, 32-file, and 64 MiB pruning of older artifacts.

Run the deterministic gate with:

```sh
go test ./pkg/testharness/codingquality -run TestCodingQualityGate -count=1 -v
```

The current baseline for both tool-call families is two expected search files,
zero unexpected files, 512-514 bounded search bytes (128-129 estimated tokens),
and about 10.4 KB of bounded command context (about 2.6K estimated tokens). The
complete command output remains available through the emitted local artifact
reference. Tests enforce upper bounds rather than exact whitespace-sensitive
counts, while logging the exact report so volume changes are visible.

Artifact pruning never removes the artifact returned by the current command.
Consequently, one current artifact may temporarily exceed the byte budget by
itself; older artifacts are still removed, and the next retained output makes
that prior artifact eligible for pruning.

## Live smoke

An optional live-provider smoke asks a configured model to create and read back
one exact file through the native coding runtime:

```sh
MINTCLAW_CODING_LIVE_SMOKE=1 \
  go test ./cmd/mintclaw/internal/coding \
  -run TestCodingQualityLiveSmoke -count=1 -v
```

Set `MINTCLAW_CODING_LIVE_MODEL` to a configured model alias to override the
default. This smoke is representative evidence only: network availability and
model behavior are nondeterministic, so it is not a merge gate.

## Decision

The existing read, search, patch/edit, write, and cancellation contracts are
sufficient for P3. The measured gap was oversized coding command output: head
and tail truncation was actionable but discarded the full result. Coding exec
now retains the full output in owner-scoped runtime scratch and exposes a
bounded artifact reference. Personal-agent and gateway exec construction keep
their previous behavior. No broader edit or search protocol rewrite is
warranted by the fixture evidence.
