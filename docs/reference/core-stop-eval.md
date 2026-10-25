# Core Native Stop Eval

`general.control.stop` is pending/non-default. It checks a genuine native
`/stop` during a held read, non-vacuous queued-input settlement and subsequent
same-session reuse. It is not qualified by model-free controls, an ACK's prose,
a closed client, or the existing local `Action{kind: stop}`.

## Inputs And Profile

Use a fresh owned active-tool profile with `conversation`, `active_tool`,
`bounded_calls`, metadata capture and a judge. Pin `one-at-a-time` and
`active_tool.max_parallel_turns: 1`; the existing policy gate also requires both
native capacity settings to equal one. Tool/server authority remains the
[single read-only MCP fixture](eval-active-tool-fixture.md), not arbitrary MCP,
exec, writes, background work or production state. No LLM-user or protected facts.

Four actual inputs are bounded by the source case:

1. Ask for the registered held read. The driver waits for actual tool entry.
2. Send guidance B and observe its native enqueue hash while the read is active.
3. Send `/stop` without releasing the fixture.
4. After native abort proof and its correlated ACK, ask for a new label C.

The extra queued input refines the roadmap's provisional three-input budget:
otherwise queue clearing would only be tested on an empty queue. There is no
copied scheduler, synthetic enqueue, sleep-based success or post-final substitute.
The conversation owner records each real send and enforces four total inputs and
at most two in-flight additions. Only the actually sent `/stop` request ID may
select the first terminal control response; arbitrary/unsent IDs are refused.

```sh
MINTCLAW_HOME=/private/fresh-profile MINTCLAW_CONFIG=/private/fresh-profile/config.json \
  mintclaw eval run --root evals --case general.control.stop --include-pending \
    --preset /private/fresh-profile/reports/preset.json --out /private/fresh-profile/reports \
    --start-target --json
```

## Native Evidence

The mandatory `native_stop` check joins independent native observations:

- A held, unreleased fixture and exactly correlated queued guidance. The native
  tool-start/enqueue/abort records bind the original actual wire request, agent,
  session, turn and call ID, not just a matching marker.
- Fixture `canceled` with `context_canceled`, no driver release, and native
  `agent.turn.end` status `aborted` with one LLM call. Deadline, host close,
  completed/error status, missing events and unknown observation do not pass.
- A canonical committed history retaining the original user root, one tool
  intent and its paired native `error` result, then the new user input and one
  assistant result. Hard abort commits its terminal result before returning
  early, so the normal `tool.exec_end` log may be absent; history is mandatory.
- No queued B retained in any history field, no steering injection into the
  aborted/reuse turn, and a distinct completed reuse turn with one LLM call and
  no further tools. There is no fabricated completion of the aborted request.
- A complete post-stop outbox census: exactly the legitimate stop ACK and reuse
  final on their actual requests, delivered once, no orphan original/guidance
  final. A command ACK is a final delivery but not an LLM `final_reply` and has
  no invented LLM trace scope.

Native assistant history is compared with pre-render outbox content. Wire text
may also contain configured model/token footer metadata; the oracle does not
strip suffixes heuristically or mistake that formatting for a different answer.
Wire request/session/delivery identities and the requested label remain checked.

The shared core owner captures final log/history/outbox evidence after the owned
child stops and before any judge. Clean child/host cleanup, untruncated logs,
observed private surfaces and a settled known native target budget are mandatory.
Missing global evidence blocks all affected judges. The judge sees only the
eligible visible conversation and cannot override a hard failure.

## Bounds And Limits

Four inputs, no LLM-user calls, nominally two target calls and at most one judge;
two-minute turns and five-minute case. Shared native call/input/output/usage/
concurrency ceilings remain independently enforced. Abort observation waits at
most five seconds for actual cancellation and terminal evidence, not elapsed-time
success. Raw log/history and the 3 MiB frozen proof retain existing bounded,
anchored, privacy-inspecting readers; no live store recovery is performed.

Frozen `grade` checks all retained sources before model work and needs no live
history, outbox, trace or fixture host. It calls only the tools-disabled judge.
Native model telemetry may cover only the delivered reuse turn; it never claims
all-call coverage. The independent native budget covers the aborted call too.

These are operator-owned observations, not hostile-local attestation or proof of
unrelated external effects. Model-free native success/failure/unknown, admission
and frozen regrade controls are separate from the later fixed live campaign:
one qualification plus one immutable three-trial batch, no retry-until-green,
promotion only after independent hard-check-plus-judge 3/3. PDF/browser stay deferred.
