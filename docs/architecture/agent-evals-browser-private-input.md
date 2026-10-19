# Browser Evals: Native Protected Input Prerequisite

## Scope And Current Gap

The [manual eval goal](agent-evals-goal.md) requires private fact handles for browser submit and multi-turn cases.
Those cases currently stop before execution with `browser_private_input_unavailable`. This gate is intentional:
ordinary `request_user_input` answers enter durable input/history, while protected-answer consumers are currently
registered only for document forms. Existing browser fill protects model-produced tool arguments and transient
driver input; it does not turn an ordinary chat answer into protected ingress.

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
