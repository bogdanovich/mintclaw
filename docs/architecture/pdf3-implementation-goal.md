# PDF3 Conversational Form Implementation Goal

## Status

Complete for `linux/amd64`. The protected store, interaction, mapping/review, approval, PDF2 handoff,
single-delivery, deployment, live-channel, privacy, rollback, and cleanup gates are recorded in the
[PDF3 exit record](pdf3-exit-record.md). PDF0A, PDF0B, PDF1A, and PDF2 remain its completed prerequisites.

This document remains the frozen source of truth for PDF3 scope and acceptance criteria. The exit record is the
authoritative completion evidence; it does not admit PDF1B, PDF1C, PDF4-6, or macOS implementation.

PDF3 turns the explicit one-call field map admitted by PDF2 into a durable multi-turn workflow. It collects protected
answers, maps them to the frozen field schema, presents a bounded review, requires the configured approval when
applicable, and hands one confirmed assignment set to the existing PDF2 transaction. It does not add a second PDF
writer, sender, task system, or form-specific assistant.

## Operator outcome

An operator can attach or authorize one supported AcroForm and ask MintClaw to complete it over multiple messages.
MintClaw:

1. keeps the job across unrelated conversation, compaction, and service restart;
2. asks only unresolved questions using channel-native choices while also accepting a natural free-text reply;
3. accepts corrections, skip, not-applicable, and cancel without requiring an interaction ID syntax;
4. shows a bounded redacted review and refuses to proceed while required facts are missing, ambiguous, invalid,
   conflicting, or below the admitted confidence threshold;
5. obtains the configured approval before the final mutation when policy requires it; and
6. returns exactly one independently verified PDF or one truthful typed failure.

The source bytes remain unchanged. No canceled, expired, deleted, unauthorized, stale, unreviewed, or unapproved job
may fill or deliver a document.

## Fixed architecture decisions

### One workflow owner

`pkg/document` remains the sole owner of document semantics. A focused form-workflow component under that package owns
the PDF3 job, field ledger, mapping state, review revision, and PDF2 handoff. It composes with existing runtime owners:

| Owner | PDF3 responsibility | Explicitly does not own |
| --- | --- | --- |
| Form workflow | Job lifecycle, encrypted field ledger, mapping, review, and commit identity | Chat transport, PDF mutation, or delivery attempts |
| Interaction coordinator | Prompt correlation, channel controls, expiry, answer claim, and cancel | Raw protected values or form mapping |
| Agent runtime | Route binding, protected continuation, configured model execution | Durable value storage or job truth |
| PDF2 service and journal | Transactional fill, structural/visual verification, artifact registration | Multi-turn collection |
| Outbox/channel manager | One logical delivery intent, attempts, acceptance, and ambiguity | Job or document correctness |

PDF3 adds no daemon, broker, MCP server, unrestricted shell fallback, browser-authoritative filler, parallel task
registry, or alternate delivery queue. The deferred model surface remains the single compact `document` tool.

### Identity and authority

A job is bound at creation to:

- a generated `job_id` and monotonic revision;
- agent/profile, workspace, route session, channel account, chat, sender, and topic/space scope;
- the immutable authorized `DocumentRef`, source SHA-256, PDF2 field-schema digest, and backend capability revision;
- retention deadline and deletion generation; and
- the configured audit-policy revision.

Every status, answer, correction, review, approval, commit, and delete compares the full owner scope and expected job
revision. A current chat message is not authority for a job owned by another sender, route, account, profile, or source.
Source digest, field-schema digest, backend capability, or audit-policy changes make the job stale and prevent commit.
Re-admission creates a new job; it never silently adopts old protected values.

### Public job snapshot and encrypted field ledger

The durable store separates a safe public snapshot from an encrypted append-only value ledger.

The public snapshot may contain job identity, owner hashes, source and schema digests, stable field IDs, value-state
enums, confidence bands, provenance kinds, validation codes, interaction IDs, revision counters, counts, timestamps,
operation/artifact/delivery IDs, and terminal outcome. It must not contain raw answers, normalized values, document
content, rendered pages, host paths, filenames supplied by the user, or model-authored summaries of protected values.

Each protected ledger event contains one field ID, value kind, normalized value, provenance, confirmation state,
superseded event ID, and bounded validation metadata. Events are immutable; correction appends a successor. The
current value is a deterministic projection of the latest valid non-superseded event.

Ledger records use a versioned XChaCha20-Poly1305 envelope with a fresh nonce and associated data binding the schema
version, profile/workspace owner, job ID, field ID, event ID, and revision. A randomly generated 256-bit profile key is
created before the first job, stored outside the agent-visible workspace with directory mode `0700` and file mode
`0600`, and never serialized into config, argv, environment, logs, traces, task state, or backups that contain only job
data. Key creation and ledger writes are atomic and durably synced. Wrong key, modified ciphertext, owner mismatch,
rollback, truncated data, or unsupported envelope version fail closed.

This protects values from ordinary history, diagnostics, accidental workspace disclosure, and state-only backups. It
does not claim protection from a root compromise or an attacker that can read both the profile key and ciphertext.
Key loss makes jobs undecryptable and therefore uncommittable; it never falls back to plaintext. Rollback restores key
and ledger material as one generation. Deletion cryptographically and physically removes the job records under the
documented filesystem guarantees, then leaves only a path-free tombstone until retention expiry.

### Job state machine

The sole forward state machine is:

```text
prepared -> collecting -> review_ready -> awaiting_approval -> committing
          -> delivering -> completed

prepared | collecting | review_ready | awaiting_approval
          -> canceled | expired | deleted | failed

committing | delivering -> failed | uncertain
```

`collecting` can cycle only by appending ledger events and increasing the job revision. `review_ready` names one exact
field-state digest. Any correction invalidates that review and returns the job to `collecting`. Approval binds the job
ID, owner, source/schema digests, review revision, assignment digest, output policy, and expiry. It does not contain raw
values. Commit is irreversible after PDF2 accepts the write operation; cancellation after that boundary reconciles the
same operation rather than allocating a replacement.

Terminal jobs cannot transition back to an active state. A definitely incomplete pre-write job may be cloned only by
an explicit new start action with a new identity. An uncertain write or delivery is inspected, never blindly replayed.

### Protected interaction mode

Existing ordinary `request_user_input` remains unchanged. PDF3 extends the trusted interaction control plane with a
protected-answer binding produced only by the form-workflow service, not by model arguments. The binding contains an
opaque job/field/revision token and an idempotency key derived from interaction ID plus inbound message identity.

For a protected question:

1. the interaction registry persists prompt metadata, choices, owner route, and the opaque binding, but no answer text;
2. Telegram and other channels continue to project button replies and reply-to free text through the existing inbound
   interaction path;
3. before ordinary history admission, the interaction coordinator gives the raw answer once to the protected sink;
4. the form workflow validates authority and revision, durably appends the encrypted ledger event, and returns an
   opaque value-event reference plus safe state;
5. only that reference is atomically claimed in the interaction record; and
6. continuation receives safe state and a scoped protected view only when the next workflow step requires it.

The sink is idempotent. A crash after ledger append but before interaction claim reuses the same event ID. A conflicting
retry fails closed. An orphaned unclaimed event is not current and is removed by bounded recovery. Store failure leaves
the interaction waiting or failed with an actionable safe code; it never admits the raw reply to ordinary chat history.

One PDF3 interaction asks one logical field or one explicitly versioned composite value. It may show up to the existing
bounded number of choices and always accepts free text. Channel-native controls include relevant yes/no, skip,
not-applicable, and cancel actions; cancel remains available even when it is rendered outside the choice row. A normal
text reply, reply-to prompt, voice transcription, or unambiguous button answer is accepted without `/answer`, short ID,
or `question_id` syntax. Duplicate button/text ingress resolves to the same idempotency key and cannot append twice.

### Mapping, validation, and correction

The field schema from PDF2 is immutable for the job. Mapping uses stable field IDs rather than labels guessed at commit
time. Each field projection records provenance as user-supplied, source-extracted, deterministic, model-suggested, or
user-confirmed, plus confidence, validation state, blank reason, and superseded event reference.

Deterministic type and choice validation runs before model interpretation. A configured deliberative document model may
interpret user language, suggest mappings, identify conflicts, or audit the bounded review. It receives only the
minimum scoped protected view for the active job and never durable host paths or unrelated values. Model output is a
typed proposal, not authority. Suggested or low-confidence values require confirmation. A model/provider fallback is
usable only when the frozen audit policy declares it equivalent; otherwise the job reports `audit_unavailable`.

Confirmed values are never re-asked unless the user requests correction or the source/schema becomes stale. Correction
appends a successor and invalidates every derived review/approval digest. Optional blank, required blank, skip, and
not-applicable are distinct. Required blank, ambiguity, conflict, invalid choice/type, unsupported field, or unresolved
low confidence prevents `review_ready`.

### Review and approval

The review is deterministic and bounded. It lists document identity in safe form, field labels/IDs, completion state,
provenance, masked value summaries where safe, unresolved blockers, and the exact requested output action. It does not
place raw protected values in canonical history, generic task fields, interaction snapshots, traces, or the outbox.
Individual channels may already retain the operator's own inbound answer; MintClaw does not duplicate it in review.

When policy requires approval, the workflow emits an existing durable approval interaction. The approval action binds
the exact review and assignment digests and shows the requested outcome without raw values. Denial, expiry, owner
mismatch, correction, source/schema change, or policy change invalidates it. Approval consumption and PDF2 operation
acceptance are ordered so one approval can authorize at most one exact commit.

### PDF2 handoff and delivery

Only the workflow service decrypts current confirmed ledger values. At the commit boundary it builds the in-memory
typed PDF2 assignment map, invokes the existing PDF2 coordinator, then zeroes/drops the temporary projection as far as
Go runtime semantics permit. Neither the model nor a second writer performs the fill.

The PDF2 operation ID is deterministic from job ID, review revision, assignment digest, and output policy. Recovery
always queries that operation. A verified artifact is registered by PDF2 and creates one outbox media intent whose
source identity is derived from the same operation and artifact-set digest. The job records only opaque operation,
artifact, and delivery IDs. Confirmed, definitely failed, and ambiguous delivery remain distinct; ambiguous acceptance
is terminal until an operator explicitly reconciles it.

### Model, history, diagnostics, and privacy boundary

- The single deferred `document` tool gains workflow actions but retains a compact schema and sensitive projections.
- Raw protected replies bypass canonical history entirely. Durable assistant/tool projections contain only job IDs,
  field IDs/states/counts, safe codes, and opaque event references.
- A protected model view is ephemeral, job-scoped, size-bounded, auditable by safe counts/digests, and absent from
  passive traces. It is released after the one interpretation/audit call.
- Logs, runtime events, task field deltas, diagnostic bundles, crash metadata, PR fixtures, and smoke output never
  contain values, plaintext ledger events, keys, host paths, document bytes, or rendered pages.
- Test leak sentinels use synthetic unique values and scan canonical history, interaction/task/job public state,
  outbox metadata, logs, and passive traces. Ciphertext is allowed; recoverable plaintext outside the protected view is
  not.
- Retention, cancel, expiry, and explicit delete are enforced by the form-workflow owner. Deletion failure is visible
  and retryable; success is never reported before durable removal completes.

## Failure vocabulary

PDF3 adds at least:

- `form_job_not_found`, `form_job_unauthorized`, `form_job_conflict`, `form_job_stale`, and `form_job_expired`;
- `protected_store_unavailable`, `protected_key_unavailable`, `protected_record_corrupt`, and
  `protected_answer_conflict`;
- `field_unresolved`, `field_ambiguous`, `field_conflicting`, `field_invalid`, `field_required_blank`, and
  `field_confirmation_required`;
- `audit_unavailable`, `audit_policy_changed`, and `review_stale`;
- `approval_required`, `approval_denied`, `approval_expired`, and `approval_stale`; and
- the existing PDF2 write, verification, artifact, delivery, cancellation, and uncertainty failures.

Messages are path-free, content-free, bounded, and actionable. Backend/provider error bodies are never forwarded.

## Pull-request sequence

Use one dedicated autonomous worktree. Each dependent PR starts from the latest merged `origin/main` and does not begin
until its predecessor is merged.

1. **Admission:** this goal, roadmap status, architecture decisions, PR boundaries, matrix, and stop gates. Docs only.
2. **PDF3A store:** job/public snapshot, encrypted append-only ledger, key lifecycle, migrations, retention,
   cancel/expiry/delete, recovery, permissions, and privacy-negative tests.
3. **PDF3A interaction:** protected-answer binding/sink, idempotent claim/recovery, buttons plus natural free text,
   skip/not-applicable/cancel/correction ingress, Telegram tests, and no-history proof.
4. **PDF3B mapping:** schema-bound mapping, typed validation, provenance/confidence/conflict state, append-only
   correction, confirmed-fact reuse, and stale-schema behavior.
5. **PDF3B audit/review:** configured deliberative model role, fail-closed fallback policy, scoped protected view,
   deterministic redacted review, unresolved blockers, restart/compaction tests, and agent/channel vertical proof.
6. **PDF3C commit:** approval binding/expiry, one-time consumption, PDF2 handoff, stable operation identity, cancel/race
   semantics, and structural/visual verification evidence.
7. **PDF3C delivery and smoke:** one outbox-owned result delivery, ambiguity/no-replay recovery, automated PDF3 smoke,
   full agent/channel/worker regression series, and copy-pasteable manual test.
8. **Deployment and exit:** deploy exact merged code, run live real-channel, trace/privacy, restart/compaction,
   structural/visual, single-delivery, cleanup, and rollback proofs; then merge the exit record.

A prerequisite exposed by executable evidence may become a smaller PR, but may not broaden product behavior. Four
substantive review/fix cycles or cross-subsystem growth triggers the autonomous architecture checkpoint.

## Acceptance matrix

| Invariant | Minimum executable proof |
| --- | --- |
| At-rest protection | AEAD round trip, wrong key/AAD/tamper/truncation refusal, permissions, atomic create, restart |
| No ordinary persistence | Unique sentinel absent from history, interactions, tasks, public jobs, logs, traces, and outbox |
| Natural interaction | Telegram button, reply text, ordinary text candidate, voice transcription, duplicate ingress |
| Lifecycle | Start, resume, status, cancel, expiry, delete, restart, compaction, unrelated conversation |
| Mapping | Stable IDs, validation, ambiguity/conflict, required blank, confidence, confirmed-fact reuse |
| Correction | Append-only successor, old review/approval invalidated, no stale value at commit |
| Audit routing | Configured role used, equivalent fallback only, unavailable and policy-change refusal |
| Approval | Exact digest binding, owner/expiry/denial/stale checks, one consumption |
| PDF2 handoff | Same source, explicit assignments, unchanged source, structural and visible verification |
| Delivery | One stable outbox identity across restart, duplicate update, failure, and ambiguity |
| Rollback | Schema/key/state backup set, old-binary refusal or documented forward-only migration |
| Platform | Capability unavailable outside qualified `linux/amd64`; macOS parity remains queued |

The stable automated harness extends `scripts/document-deployed-smoke.sh` and emits distinct PDF3 markers for protected
store, interaction UX, restart/compaction, mapping/review, approval, PDF2 commit, source unchanged, single delivery,
privacy, cleanup, and overall success. It uses only deterministic synthetic fixtures and values.

The final manual test uses real checked-in or deployed paths, not placeholders. It starts a job from Telegram, answers
once with a button and once with natural text, corrects one answer, restarts the gateway between questions, forces or
simulates compaction, reviews, approves, downloads exactly one PDF, verifies fields independently, renders affected
pages, cancels a second job, and runs documented privacy queries.

## Completion criteria

PDF3 is complete only when:

- all eight stages are merged and the roadmap links a final exit record;
- buttons and natural text work without ID syntax, duplicate ingress stores one event, and protected replies never
  enter ordinary history;
- active jobs survive restart and compaction, confirmed facts are not re-asked, and correction/cancel/expiry/delete are
  deterministic;
- incomplete, stale, ambiguous, conflicting, invalid, low-confidence, or unaudited state cannot reach PDF2;
- one exact approval authorizes at most one exact assignment digest;
- PDF2 independently verifies the output, source bytes are unchanged, and one outbox intent owns final delivery;
- restart and ambiguous acceptance never create a second fill or send;
- privacy sentinel scans, race/cancellation tests, migrations, rollback, full CI, and automated review pass;
- the exact merged revision is deployed to `server@oc`, all configured services remain healthy, and live real-channel,
  passive-trace, structural/visual, delivery, cleanup, and rollback evidence is recorded; and
- the exit record states backend/schema versions, merges, deployment SHA, test commands, known limitations, retention,
  deletion guarantees, and the next explicit boundary.

## Stop gates and exclusions

Each PR stops at its declared boundary. PDF3A cannot claim mapping or fill; PDF3B cannot mutate a document; PDF3C may
commit only the exact reviewed state through PDF2. Failed protection, authority, audit, review, approval, verification,
or delivery evidence blocks completion rather than weakening a gate.

Out of scope are PDF1B provider-native PDF transport, PDF1C decryption/passwords, PDF4 XFA mutation, PDF5 OCR/tables/
redaction/transforms/generation, PDF6 companion placement, macOS implementation, browser-authoritative filling,
signed/encrypted document modification, arbitrary filesystem access, legal/business correctness, and any USCIS,
I-134, tax, medical, financial, or other form-specific rules.
