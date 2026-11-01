# Core Compaction Eval

`general.context.compaction` remembers a synthetic code, corrects it, sends two
bounded unrelated acknowledgements and recalls the corrected value. The fifth
input is not sent until a native Seahorse compaction has actually completed.
An answer claiming to have compacted context is not evidence.

## Controlled Profile

The profile has exactly `conversation`, `bounded_calls` and `compaction`, one
tool-free Seahorse agent, native metadata capture and a tools-disabled judge.
No model user, tools, workspace inputs, interactions, always-on skills,
fallback models, streaming or parallel turns are admitted. Native runtime and
process ownership remain unchanged.

`compaction` pins four numeric context settings. History/summary budgets are
between 128 and 2048 tokens, fresh-tail tokens equal the history budget, and
`recentTailTurns` is explicitly zero. The raw native configuration must contain
exactly those four correctly cased keys with matching values. Missing or
differently cased zero-tail settings are refused: native omission otherwise
selects its default tail. Database paths, ignored/stateless session patterns,
retrieval settings and other raw configuration are not granted.

Zero tail is a deliberate **test profile**, not stateless mode or a production
default change. Native leaf summaries require eight eligible messages. Four
completed turns provide exactly eight; the explicit tail permits that complete
prefix to compact before recall. A short three-turn prefix cannot exercise this
path, even with a small context budget. The harness does not change fanout,
copy the compaction engine, call a coding-only manual command or use a large
prompt to bypass the native boundary.

Each case has five inputs, two-minute turns, a five-minute case and one judge.
At most three repetitions share one owned child and native call/token ceiling;
their fifteen root turns retain their existing sixteen-binding limit. Native
compaction attempts also create separate continuation traces. Only this
profile has a fixed 64-file retention ceiling so those attempts cannot evict
the root evidence; ordinary profiles retain their existing sixteen-file limit.
Trace bytes, records, content mode and retention hours are unchanged. The native
budget must allow up to 4000 output tokens per auxiliary summary call, while
ordinary target turns are restricted to 64-256 configured output tokens.
Auxiliary summarization calls use the same native budget, never a reset or
unaccounted helper model.

## Evidence And Ordering

Metadata-only turn start/end and compaction lifecycle events are captured from
the owned gateway log. Before sending recall, the driver observes a bounded
snapshot containing a matching native start/end attempt, the actual session
and agent, canonical transcript count/revision eight, background proactive
reason, `completed` status and positive saved tokens. Failed, interrupted,
no-progress, missing, foreign or earlier-frontier attempts do not satisfy it.
Polling reads native evidence; it does not sleep in place of an operation or
retry a target request.

After clean owned shutdown, the shared core owner freezes the complete log,
ten canonical messages and five delivered final intents. The barrier snapshot
must remain an exact prefix of the final log; the actual recall request's
native turn start must occur after it. All five request/turn/session bindings,
one completed target LLM call per turn, unchanged scripted inputs, clean
canonical history and exact corrected recall are independently checked before
judging. Later unknown native budget or privacy coverage prevents earlier
repetitions from passing too.

The receipt proves completed native compaction before recall, not a general
theorem about summary quality or every possible production context profile.
Operator-owned reports are not authenticated against a hostile editor.

## Validation And Status

The model-free real-CLI control runs three native sessions with fifteen target
requests and actual auxiliary summary requests. Its synthetic provider derives
recall from the received context; the original correction text must no longer
appear in recall requests. Fourteen frozen negatives and five run-wide unknowns
exercise the oracle. Offline grade succeeds after deleting live session/SQLite,
outbox, trace and log evidence, without target or summary calls. Missing frozen
proof is refused before another judge call.

These controls do not qualify a paid model. The public case stays
pending/non-default until separate bounded qualification and an immutable
three-trial batch succeed. The existing six General defaults, historical case
digests and deferred PDF/browser policies are unchanged.
