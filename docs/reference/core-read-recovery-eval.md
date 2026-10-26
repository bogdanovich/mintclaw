# Core Read Recovery Eval

`general.tools.recovery` is a pending, explicit General scenario. It performs
one real failed `read_file`, one corrected successful read, then same-session
recall. The [fixed campaign](../operations/manual-agent-evals-core-c3a-20261025.md)
passed qualification, then retained one pass/two judge provider errors in its
batch. All native executions/hard checks passed, but it is not qualified.

## Profile

Use a fresh isolated profile with `conversation`, `bounded_calls` and `read_tool`,
metadata capture and a judge. No model-user, ordinary-question, MCP, PDF or browser
capabilities are admitted in this specialized profile. Tool policy is exactly
default-deny with `read_file` allowed; `request_user_input` is disabled. Existing
outside-workspace read/write, exec, hooks, nodes and background-work restrictions
remain in force. Secrets stay outside the accessible workspace.

The reviewed seed must contain only `eval-fixtures/recovery.txt` in that directory,
mode 0600 under a 0700 directory. Pin it in the preset:

```json
{
  "workspace_files": [{
    "path": "eval-fixtures/recovery.txt",
    "sha256": "8a0c13cee498b7b386c2f2ee27c1deb803d314f9190eb6f3ddd62923f07dcbf8"
  }]
}
```

Its content is the public [synthetic fixture](../../evals/fixtures/general/read-recovery.txt),
not production input. `PrepareProfile` copies only pinned bytes using existing
anchored, bounded and credential-inspecting preparation. Native startup independently
checks the complete one-file census and pinned digest; extra state, links, wrong
modes and a pre-existing `missing.txt` are refused. Never delete a PID or old state
to turn a used profile into a fresh one.

```sh
MINTCLAW_HOME=/private/fresh-profile MINTCLAW_CONFIG=/private/fresh-profile/config.json \
  mintclaw eval run --root evals --case general.tools.recovery --include-pending \
    --preset /private/fresh-profile/reports/preset.json --out /private/fresh-profile/reports \
    --start-target --json
```

This is an on-demand profile, not a replacement for the six-default active-tool
profile. Even if later qualified, recovery should remain non-default unless an
explicit profile-composition decision is made. No global tool authority or new
multi-profile scheduler is introduced here.

## Native Contract

The scripted driver sends three inputs: read the missing path once; supply the
corrected path; recall its value without another tool call. The expected marker
is not in the task or follow-up prompts. The target can learn it through the
actual successful read. The required judge checks honest/coherent recovery.

The hard oracle independently requires:

- Before/after complete canonical fixture censuses and matching pinned SHA-256.
- Clean committed native history with the same agent/client/session identity,
  no skipped history or summary, and exactly the three actual user inputs.
- One emitted missing-path call with a paired native `error` result and one
  continuation before the corrected input. A successful or canceled call cannot
  substitute for the required read error.
- One corrected-path call with a paired native `success` result containing the
  real marker, then a continuation and recall. No orphan, replay, extra call,
  missing continuation, early invented marker (including assistant text attached
  to either tool call) or stale final may pass.
- Clean owned process shutdown, an untruncated log, observed leak-free private
  surfaces and a settled known native call budget. A later unknown call in the
  same run also prevents earlier conversations from being judged/promoted.

The core post-stop owner is shared with steering. It captures all bounded native
observations before allowing any judge call; it does not implement another tool
runner, steering queue or approval authority. Frozen original metadata/history,
hashes and fixture observations support offline `grade`, including after live
history/fixture files are removed. Grading validates every source first, calls
only the tools-disabled judge, and never reopens native history or reruns a read.
The judge sees the eligible visible conversation, not raw proof.

## Bounds And Status

Three inputs, no LLM-user calls, one judge, two-minute turns and a five-minute
conversation. Shared native call/input/output/usage/concurrency ceilings remain
independent. History uses the native 2 MiB/256-record decoder; frozen recovery
proof is capped at 3 MiB and fixture reads at 64 KiB. This is owned-file observation,
not hostile-filesystem attestation or proof of unrelated workspace state.

Model-free controls exercise real native read failure/success, invalid profile
admission, wrong/replayed results, changed fixture observations, removed proof,
and offline regrading. They are not live qualification. The reviewed implementation
and fixed campaign are recorded above; recovery remains pending/non-default.
Promotion requires independently hard-checked/judged 3/3. Preserve failed/unavailable
outcomes; never rerun this campaign until green or resume deferred PDF/browser work.
