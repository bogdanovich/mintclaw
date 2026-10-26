# Core Owned Restart Eval

`general.history.restart` sends a synthetic marker, observes a delivered native
final, closes the first client, cleanly stops its owned gateway, starts an
admitted successor and reconnects the same client session before asking for
the marker. This tests process restart, not merely reconnect or agent CLI
history. The owner-controlled [restart foundation](eval-owned-restart.md)
remains the sole process/admission authority.

## Admission And Limits

The dedicated profile has exactly `conversation`, `bounded_calls` and
`owned_restart`, native model metadata, a judge and one tool-free Seahorse agent.
No workspace inputs, tools, interactions, model user, always-on skills, fallback
models, streaming or parallel turns are admitted. Configuration, executable,
home identity and original preset cannot change between generations.

One case may run at a time, with at most three repetitions. Each trial uses a
fresh native session and exactly two scripted inputs. A restart occurs between
its inputs; the resulting four-process chain for repeat-three still shares one
original call/token ceiling. Each successor subtracts actual completed native
usage, preserving per-call/output/concurrency ceilings. Unknown usage prevents
the next input. There is no public used-home, attach-PID or budget-reset option.

Turns are bounded to two minutes, cases to five minutes, and the judge to one
call per trial. A successor attempt is not retried after uncertain admission.
Every retained child is cleaned up even if reconnect or observation fails.

## Evidence

The private run retains one `target_generations` entry per actual child, with
its runtime, lifecycle, native budget receipt, private-surface observation and
native model projection. Top-level lifecycle/runtime/budget describe the last
child, not an aggregate native receipt. Offline validation checks the complete
remaining-ceiling chain, distinct PIDs/instance identities/nonces, unchanged
source/configuration and exact agreement with those top-level fields.

Native turn IDs may repeat after reboot. Events and model bindings therefore
carry a generation number; old single-process reports omit it and keep their
existing hashes. A successor may exclude only predecessor traces already
frozen by the owner, and must still find their exact unchanged hashes in the
bounded trace store. Missing or modified predecessor files remain non-passing.
No timestamps or model prose substitute for this identity boundary.

Each trial freezes two canonical messages after its first shutdown and four
after the final shutdown, including native session/route metadata. The first
messages must remain byte-identical; there must be no skipped history, summary,
tool calls, repeated input or extra final delivery. The recall answer must be
the originally supplied marker, which is absent from the recall request.
Both actual requests must match their generation's native model bindings.

All generation receipts, budget/model counts, canonical history and delivered
outbox intents are checked before the semantic judge. Any failed/unknown later
generation also prevents earlier repetitions from passing. Offline `grade`
uses these frozen facts without restarting a process, rereading live stores or
replaying target requests. These are operator-owned observations, not signed
attestations against a hostile editor of private reports.

## Status

The public case is pending/non-default until separate bounded live
qualification and an immutable three-trial batch succeed. Model-free HTTP/CLI
repeat-three, frozen mutation and offline grading controls pass, including
grading after deleting live stores/logs and refusal of missing frozen evidence
before a judge call. These are development controls, not paid semantic
qualification. The six
existing General defaults and deferred PDF/browser policies are unchanged.
