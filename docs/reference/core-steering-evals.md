# Core Steering Evals

`general.steering.boundary` and `general.steering.order` are pending manual
General scenarios. They do not join the four qualified defaults until the fixed
live qualification protocol succeeds. PDF/browser remain pending and untouched.

## Execution

Use a fresh prepared [active-tool profile](eval-active-tool-fixture.md) with
`conversation`, `active_tool`, `bounded_calls`, metadata capture, a configured
judge and `--start-target`. The boundary case pins `one-at-a-time`; order accepts
separate explicit `one-at-a-time` and `all` profile variants. A mismatched or
unbounded profile is refused before target work. For example, using an operator's
private prepared preset:

```sh
mintclaw eval run --root evals --case general.steering.boundary \
  --preset /private/prepared/reports/preset.json --out /private/prepared/reports \
  --start-target --include-pending --json
```

The initial task calls one synthetic read with a unique trial/session identity.
The synchronous in-flight driver waits for the real MCP handler to remain active,
sends one correction (boundary) or B then C (order), and waits for each native
enqueue hash before releasing the read. The agent then completes and answers a
same-session follow-up. No sleeps, fabricated acceptance, shell tool or copied
steering queue substitutes for native execution.

Native sender/authority selection intentionally coalesces messages from the same
sender in both configured modes (`dequeueForTurnLocked` in `pkg/agent/steering.go`).
These scenarios therefore require one ordered injection batch for that sender,
not an invented one-message-per-LLM-call rule. The two order variants remain
separate runs with separately recorded configuration and qualification.

## Hard Evidence

- A held, unreleased fixture and one matching native enqueue per actual wire
  input, with correct agent/session/turn identity and native trace event IDs.
- Canonical native history: one emitted read, its successful paired result,
  corrections after that result in FIFO order, and retained session/client scope.
- A complete metadata-only native trace from the candidate source, with the
  same tool call/result, interrupts during that call, then injection before the
  first post-tool model request. An intervening request cannot be hidden by a
  later request. Truncated/missing/mismatched evidence cannot pass.
- A post-stop outbox census binding one delivered logical final per request to
  its observed platform message ID. Streaming revisions are not extra finals.
- A clean owned gateway, observed private surfaces, an untruncated log, a known
  settled call budget and a closed fixture host. Missing host metadata cannot
  disappear during grading/comparison or be overridden by a passing judge.

History and outbox are re-read after native stop, before any judge call. All
bounded native observations finish before judging begins. Original native JSON,
JSONL and hashes are retained privately for offline hard-check replay; grading
does not reopen live history/outbox/traces or restart a target. Judge input is
only the eligible visible conversation, never those raw native observations.

## Scope And Bounds

These live contracts deliberately hold one read. Multi-call emitted-batch and
precise finalization-edge races remain deterministic native agent tests; this
is not a claim of live coverage for an artificially paused finalization edge,
anonymous coding steering, multiple senders or physical Telegram rendering.
Distinct-session isolation is the next C2 scenario, not covered by these cases.

Each case permits one judge call, no model-user calls, at most three/four user
inputs, a two-minute turn and five-minute conversation budget. The existing
run-wide provider budget and suite deadline remain independent hard bounds;
post-stop observation has a separate bounded cleanup allowance. Durations for
the conversation exclude deferred judging; role calls retain their own timings.
Native trace limits are unchanged. Frozen steering evidence is capped at 3 MiB;
outbox census is at most 128 entries/2 MiB, with bounded original native files.

The real CLI/local HTTP controls exercise both scenarios and both order modes,
then remove owned live history/outbox/traces and regrade the frozen evidence.
Negative controls include cancellation, wrong scope, orphaned tool results,
native trace errors, late injection, stale finals, duplicate native-valid deliveries, missing
host/cleanup evidence and truncated logs. These are model-free CI controls,
not live model qualification or a new green baseline.

The model-free CLI build explicitly disables VCS stamping so its synthetic
source identity is the same in archives, worktrees and ordinary CI checkouts.
Production trace provenance checks are unchanged. An unattributed-source control
must retain a complete `inconclusive` report with partial native evidence and
zero judge calls, rather than losing its checkpoint to null-array decoding.
