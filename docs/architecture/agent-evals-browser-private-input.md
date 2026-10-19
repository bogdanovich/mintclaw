# Browser Evals: Native Protected Input Prerequisite

## Scope And Current Gap

The [manual eval goal](agent-evals-goal.md) requires private fact handles for browser submit and multi-turn cases.
Those cases originally stopped before execution with `browser_private_input_unavailable` because ordinary
`request_user_input` answers enter durable input/history. B1/B2 provide native protected browser input; the B3
workflow below now connects that input to scoped submission evidence. Live qualification remains separate.
Protecting model-produced browser arguments or transient driver input alone does not turn an ordinary chat answer
into protected ingress.

Do not resolve generated browser facts into ordinary messages, pass them through model-driver JSON, use a document
binding for another domain, or mutate the fixture directly to complete the target's task. Keep both mandatory
private cases visibly unqualified until native integration exists.

This prerequisite extends existing native browser/interaction boundaries. It is not a new vault, secret manager,
browser engine, database, generic value-reference service, approval authority or eval-owned tool execution path.
Credential aliases and arbitrary secret injection remain outside the eval goal.

## B1: Broker Target Binding

The native broker provides two bounded operations, not model-visible tools:

- `CreateProtectedFillBinding`: accept one value-free `PrepareActionRequest` for a fresh semantic `fill` target;
  validate owner, lease, snapshot/context, live element semantics and restricted policy through existing broker
  helpers. Return a generation-bound opaque token without creating an action/input slot or dispatching driver input.
- `PrepareProtectedFill`: an authorized native consumer supplies the exact owner, token and transient answer;
  authenticate the token and recheck expiry/controller/profile/policy/catalog and current target authority under
  the broker lock, then use ordinary native action preparation. The answer exists only in the existing ephemeral
  input slot; the prepared action and invocation remain value-free.

The binding contains only the original value-free request, native revision/generation identities and a deadline
bounded by the current session and prepared-action limit. It is authenticated with a purpose-specific HKDF-derived
key from the broker's process-generation key. A prefixed MAC with the existing input-digest key is unsafe:
ordinary fill can authenticate arbitrary input with that key. Regression controls attempt cross-purpose forgery
and signing-key derivation through the actual native input-preparation API. No persistent key is introduced.

Tokens cannot change the action kind, target, owner, declared effect or confirmation policy. Restart invalidates
them; recovery does not recreate a value. Preparation is not dispatch or ingress authority. The future consumer
must independently validate the actual native interaction, route, delivered prompt and accepted answer claim.
Existing `ExecuteAction` still owns approval, final freshness/DOM writability checks, acceptance, terminal outcomes,
no replay and input cleanup. Denied/ask policies remain authoritative. A question can outlive usable target
authority; never retarget its answer automatically after an observation/controller/context change.

Ordinary and protected preparation share request validation, locked native context checks and preparation commit.
There is no second action state machine. B1 does not create questions, advertise a new capability, change browser
tools or eval admission, start an eval, or qualify private input. Its tests cover actual native memory/file-store
preparation, dispatch/idempotence, changed targets/authority, cancellation, competing answers and plaintext absence.

## B2: Native Interaction Consumer

After B1, add a narrowly scoped native browser consumer of the existing protected-answer contract:

1. A trusted browser tool creates a single-field question from a current input target and B1 binding. Model-authored
   prompt text may explain the requested data but must not choose a sink namespace or invent authority.
2. Preserve the same owned browser resource while the native interaction waits. This is task suspension, not
   automatic transfer of local human browser control. Reuse native live-resource handoff/resolution and cleanup;
   do not add an eval lease manager. Real controller/context changes invalidate the old target.
3. Register a browser consumer with the existing interaction coordinator. Protected ingress and registry claims
   keep answer text out of ordinary history/spool/diagnostic projections. Stage only the fixed fill through B1;
   do not dispatch before the native accepted-claim/continuation boundary.
4. Resume the exact trusted browser operation using a value-free receipt. Execute through the existing broker,
   including separately required restricted-policy approval. A protected answer is never an external-submit grant.
   Rejected/stale/canceled input does not become ordinary chat or a newly selected target.
5. Reuse native idempotency/terminal records for competing answers, failed claims, lost replies, cancellation,
   timeout, shutdown and recovery. Do not introduce a durable answer store. Lost ephemeral input fails closed,
   rather than replaying or asking the model to reconstruct it.

Validate actual AgentLoop suspension/resume and cancellation, durable/diagnostic privacy, stale/foreign/reforged
bindings, competing answers, erasure, failed commits and restart/no-replay. Scope is ordinary synthetic form input;
qualify gateway-local placement first. No companion/cloud/credential or Telegram claim follows from an interface.

### B2 Consumer Contract

The gateway registers `browser_input` with its distinct `browser.input` capability and one `browser.fill.v1`
protected-answer sink. It does not claim the existing `browser.act` capability's catalog ownership. `ask` accepts only a
value-free, fresh field authority and a self-contained question; it exposes no value argument or sink selector.
It requires durable gateway history and a prepared-action window longer than the native one-minute question
minimum. Its question deadline is bounded by the broker's signed target deadline. The original execution,
tool-call, actor and route identities come from the coordinator record, not from model-authored receipt data.

The sink borrows the current gateway source on each call. Accept stages the value through B1 but does not execute;
Commit revalidates the existing native preparation after the coordinator claims the answer. `resume` requires the
exact runtime-only protected continuation reference, or a separately consumed native approval whose arguments
match that preparation. A guessed reference or a bare approval flag is insufficient. Restricted-policy `ask`
still suspends for approval; no external-submit permission follows from providing a field value.

Task suspension retains the original browser session under agent control. The existing durable tool-owned
resource resolver checks that resource on resume and closes it on abandonment. Accepted protected input uses
its exact trusted continuation, not the separate model decision protocol for manual browser handoff. Forced
discard and cancellation close the originating native owner, including after the original token's process
generation is gone. Non-forced recovery reads do not erase an in-flight winning answer. Restart never rebuilds
lost input from durable state. A successful fill followed by a failed observation preserves the known native
success and requests observation without replaying the fill.

Tool arguments/results, including question text, use protected history and diagnostic projections. The question
itself remains ordinary display text in the native interaction and must not repeat entered values. This is not a
general model-output non-disclosure guarantee; B3 still rejects private echoes in prompts and final responses.
The bounded
native hexadecimal receipt ID survives durable approval history; it contains no field value and alone cannot
authorize execution. Malformed receipt data is redacted. Operational argument logging records only a bounded
operation and argument count. Offline regression tests exercise the real
AgentLoop, interaction coordinator, browser broker and file store with a synthetic worker, plus wrong ownership,
stale/generation changes, competing answers, separate approval, forced cleanup and lost ephemeral input. These
are runtime integration tests, not a live browser/LLM qualification or admission of private eval cases.

## B3: Eval Consumer And Mutation Evidence

### Native Target Observation

The first B3 packet produces a bounded `browser_input_target.v1` observation through the existing native trace
path, following the document-field-read pattern. It adds no store, pending map, persistent signing key or new
interaction state machine. The broker includes field-role/name-digest/position, URL/origin digests and page-state
hash in its existing generation-authenticated value-free binding. Inspection returns those immutable scalars with
the original owner, request, ref, snapshot/context/controller/catalog identities, deadlines and a binding digest.
It never returns field values, raw labels, URL text or the opaque token as trace metadata.

The tool returns this observation outside model-visible/durable tool content. The AgentLoop copies the actual
successful synchronous invocation's scalar observation before result hooks run. Only after the runtime durably
owns a protected question does it publish `tool.result.browser_input_target` with the resulting interaction ID;
the final tool/namespace/token must still match the captured observation. Failed, async, hook-only, retargeted,
dropped and non-durable suspensions cannot produce this evidence. Both metadata-only and redacted capture modes
retain the bounded observation, without argument/result previews or ordinary runtime-log serialization.

This proves an original target observation, not current freshness, accepted input, permission, mutation or
completion. Consumers must independently join an untruncated identified-source suspended trace and its exact
tool-call/generation to the native registry/outbox delivered prompt, owner/session/config revisions and current
snapshot/context. URL/origin and field-name digests must match the owned fixture's exact route and expected
semantic field, not question prose. Native preparation, accepted-claim, approval, effect and cleanup evidence
remain separate. A binding digest only correlates the exact token; it does not authenticate arbitrary metadata.

### Eval Admission And Trials

The next B3 packet implements only the pre-answer eval consumer. It joins the delivered native protected
question to one complete identified-source suspended trace, an exact tool-call/generation pair and the current
owned browser session/snapshot/controller/profile/policy. The value-free target must name the exact trial URL
and origin and the fixture's unique Name or Note textbox. This initial consumer supports the fixture's main
document only; framed/context-catalog input remains unavailable rather than being guessed.

`browser.fields[].input_facts` optionally enumerates the private handles admitted to that one semantic field,
including both initial and corrected handles. It defaults to the existing final `fact`; an explicit list must
include that final fact, remain bounded and contain existing, nonduplicated handles not shared with another field.
The fixture's independent final-value oracle still uses `fact`, not `input_facts`. Neither mapping reaches the
LLM-user or judge. Question prose and handle spelling cannot choose a field. Values expand only into a freshly
rechecked explicit native answer, never ordinary chat. An attempted answer is not retried after send uncertainty.

Native registry, outbox, trace and browser-state bytes are privacy-scanned before decoding, and a raw leak in a
later trace still dominates an earlier invalid file. The existing post-stop trace reader retains its owned-exit
and private-census gates; sharing its bounded snapshot reader does not qualify live observations for grading.
Deterministic tests use real broker/registry/outbox producers with a synthetic worker/trace. They prove admission
and refusal contracts, not browser effects, retained context, accepted claims or cleanup.

At this pre-answer component's boundary the runner gates remained closed. Native mutation/approval/context evidence, fresh fact
generation/cohort retention and post-stop privacy/cleanup assessment must be connected before any live private
trial is admitted. No production deployment or successful live private-form result follows from this packet.

### Accepted Input Checkpoint

The subsequent B3 component retains each attempted answer's original native identity, fact handle and request
ID, not its value or outgoing message body. It replaces the redundant sent-ID map with one bounded attempt
sequence; an uncertain send still cannot be retried. The trace's exact source, digest, turn and tool-call
generation survive the projection for later audit.

`form_inputs` is an intermediate diagnostic checkpoint. It joins the unchanged delivered protected question,
native accepted receipt and original ask owner/request to the existing native preparation, exact field and one
successful fill invocation. Both the native registry and browser state are inspected from bounded original bytes
before decoding. Their byte digests accompany the value-free projection. The broker's input digest is keyed;
this observer does not invent a raw-value hash or decode a protected token to authenticate a fill.

A successful fill can precede final interaction delivery, so the checkpoint explicitly retains whether the
native record is still resuming or resolved. Neither status proves overall settlement. This checkpoint does not
prove field readback, correct final submitted values, confirmation, action permission, retained context, delivery,
privacy census or cleanup, and never admits a judge or makes a trial pass. Read/artifact/denial contracts reject
a mixed-in form checkpoint. This checkpoint alone leaves the private runner gates closed; the complete workflow
below is the separate admission boundary.

### Private Form Workflow

The runner admits submit/followup only with an owned fresh gateway and explicit metadata-capture opt-in, checked
before probes or model work. It materializes both supported corpus generation markers into independent per-trial
values, retaining every trial's canaries in the existing bounded cohort until native shutdown, raw privacy scans
and judging finish. Corpus digests remain source-manifest digests. The profile exposes native `browser_input`;
ordinary answers never carry private values.

Review requires all accepted fills, the requested public select, fixture-only native actions and a fresh ready
snapshot in one session/tab/controller/execution. Followup requires an earlier name and an actual later correction;
the final fixture oracle independently checks the submitted name, note and color. A native ordinary question keeps
the task suspended while the LLM-user chooses the separate confirmation intent. The runner freezes the accepted
input identities and page-state hash, then checks the native consumed confirmation before even considering the
exact subsequent Submit-form approval. A fresh passive read must still match that review. A changed page, controller,
input set, route, action or prompt is refused rather than retargeted. Confirmation and `allow_once` are different
native interactions; neither private input nor the LLM-user's prose grants permission. Existing native HMAC and
broker dispatch remain the authority, and uncertain sends are never replayed.

After owned shutdown, the runner rejoins resolved inputs and confirmation, the consumed exact approval, successful
fill/select/submit invocations, original native policy/owner records, delivered final replies and empty temporary
roots. Exactly one independently matched fixture submission and no account removal are required. Only this complete
form contract, the raw owned-profile privacy census and owned exit may admit the separate tools-disabled judge.
The existing runner finalizer publishes the canonical private report; a positive judge cannot repair missing hard
evidence. Offline tests use actual broker/registry/outbox producers with a stub worker/trace and are not live results.

Private offline regrade is deliberately still unavailable: the actual generated canaries are not persisted and a
later judge input must not bypass their raw-value guard. A safe bounded regrade contract remains E5 work; adding a
secret vault or silently judging without that guard is not an acceptable shortcut. Ambiguity/stale-account action
admission and the fresh merged-source browser experiments also remain required. No production deployment follows
from this workflow packet, and none of the preserved earlier negative results is upgraded.

Only after B2 is reviewed and merged, generate fresh facts for the existing required browser cases and retain them
in the runner's bounded private cohort. Expand a fact handle only at a freshly observed native protected prompt.
Use registry/outbox/route/owner/operation bindings, not text-extracted IDs or an unauthenticated token decoder.

Reuse role-input/output, wire, native-state, SQLite and owned-profile raw-before-projection privacy guards. The
existing local fixture remains the independent submitted-value/correction oracle. Add native fill/select/submission
authority, confirmation ordering, retained-context and post-stop settlement/cleanup evidence before judging or
offline regrade. Exact fixture-bound external actions still require reviewed native `allow_once`; do not broaden
policy, use public-site mutations or substitute private data into ordinary messages/captions/prompts.

Required tests include protected fill and correction, wrong/foreign/stale prompt refusal, lost input, replay,
no-confirmation/denial, wrong fixture/account, missing native evidence, private echoes and a positive judge that
cannot override a hard failure. Ambiguity requires actual selected-target evidence: identical Remove-account labels
alone cannot authorize choosing South rather than North.

Then run separately identified fresh merged-source private submit and multi-turn experiments, with one bounded
attempt per declared repetition and independent state/privacy/cleanup results. Preserve prior PDF/browser negatives.
B1/B2 code or stub-only tests do not complete these mandatory cases or E0-E5.
