# Manual PDF Eval Fixtures And Oracle

## Current Status

The repository contains six PDF scenarios and four deterministic synthetic AcroForms, with exact SHA-256 checked
by offline corpus validation. The dialogue packet lets `eval run` attempt PDF conversations on a narrowly admitted
isolated profile, using protected facts and owned fixtures. The write-intent packet adds native review-bound separate
confirmation and exact fixture-scoped approval. Read-only native receipts bind consumed approval to a completed
form job, delivered write operation, and recovered outbox intent. Authenticated artifact collection retains the exact
downloaded PDF for independent field/structure/appearance checks. Delivery uniqueness, complete private-surface
coverage, and complete owned-resource cleanup remain pending. Owned post-stop census can establish a scoped no-write
check and remove a verified settled source fixture. Required missing evidence still prevents a passing trial;
a first question, caption, or oracle control is not qualification.

The cases cover loan filling, correction/Back, own-request conditional intent, empty required values, cancel/no-write,
and a bounded large-form section. Four use an adaptive user. The large case requests two Safety findings in an editable
partial draft, not completion of all 100 fields. Negative cases never grant approval.

`pdf.fields` is hidden oracle data covering every field with an expected value, private fact handle, or preserve
predicate. Models receive natural tasks/personas and handles, not golden field mappings. The public
`MC_EVAL_GENERATE_PRIVATE_V1` marker is reserved for fresh per-trial private generation by the integration packet;
it is not an answer and must never be sent literally. `{{fixture}}` is one runner-owned source substitution, not a DSL.

## Native Evidence Preparation

For owned runs, use `eval prepare` to create the empty native write-journal hierarchy before launch. Fresh-target
admission accepts only that exact empty private hierarchy for PDF, not prior native records or locks. The native
gateway initializes its outbox; the census still reads both real stores and never creates a missing root.
The child receives a new private `HOME/eval-tmp` as its temporary-directory environment, keeping default document
scratch within the later raw-file observation. These are isolation prerequisites, not positive PDF check results.

The integration foundation prepares a new private fixture directory per trial, rechecks pinned source bytes, and
resolves public generation markers to fresh ephemeral values. It never resumes an existing directory. Local release
retains a potentially active remote source; terminal cleanup removes only the owned source and empty directory,
not unexpected files recursively. Neither rule certifies native job/resource cleanup on its own.

Protected input requires a fresh native waiting record matching the full interaction ID, short ID, agent, session,
route/sender and expiry. The native `PromptDeliveryID` is an **outbox ID**, not the platform message ID seen on the
wire. The observer reads the exact delivered outbox intent and binds its `PlatformMessageIDs` to the observed prompt.
Pending/ambiguous delivery, stale controls and mismatched routes cannot admit private input.

Opaque protected-answer tokens are read only for a local job locator, never decrypted or treated as authority.
The native form store authenticates the public job and owner; the fixture source and job digest must match.
Explicit `/answer` ingress remains responsible for sealed binding validation and consuming stale/wrong-route
input before ordinary history. Private values appear only in an ephemeral request, not a stored event or role context.
Raw admitted registry, outbox and form-store bytes are scanned before projection; unavailable or unscannable evidence
does not establish safety. These helpers do not grant approval or settle delivery.

## Bounded Native Census

The runner captures a private baseline before sending the first PDF task and observes native write-journal/outbox
records again after the conversation. Write journals use the native home-scoped `state/document-writes/journal`;
the instance outbox uses the configured workspace's `state/outbox`. The observer never creates either store or
acquires its locks. An absent root is unavailable evidence, not an empty journal.

Each observation is bounded to 2,048 JSON records and 16 MiB of raw bytes, with native per-record limits. Directories,
files and their hierarchy must match the native private layout without symlink aliases; entries, inode identity,
size and modification time are rechecked. An unexpected file, partial inventory, concurrent replacement, excessive
size, invalid native record or duplicate JSON key refuses a complete projection. The known native journal lock
is inspected only as an empty private control file, never acquired. Raw known facts/credentials are scanned before
native decoding or owner filtering. Private baseline bytes are cleared when local trial handles are released.

Only validated native prompt/approval authority identifies owned write records. All records are inspected, including
foreign history: a valid prior foreign record is not an owned write, a malformed foreign record is not ignored,
and foreign additions/changes/disappearances make isolation uncertain. A baseline cannot be recaptured after task
effects. Missing owner/record evidence has no fabricated zero counts in retained reports.

Observed preexisting trial effects, changed source bytes, wrong scope, forbidden writes/document-fill deliveries,
multiple write operations or multiple delivered document-fill receipts fail the trial. Native intent/receipt counts
do not independently establish file type or artifact correctness. A complete bounded observation still records
`native_idle_unproven`; it grants no positive no-write, single-delivery, whole-runtime privacy or cleanup check.
Those require separate native activity/settlement and independent artifact witnesses. This packet is not E3 exit
or real-model PDF qualification.

## Owned Post-Stop Finalization

Owned PDF trials keep private `pending-trial-<id>.json` checkpoints until the admitted gateway stops and the raw
profile scan runs. Only then is the immutable canonical `trial-<id>.json` written. A complete `run.json` references
the final verdicts and coverage, not the earlier checkpoint. Attach mode cannot claim this post-stop observation.
Report failure retains private partial evidence; it never restarts a target or replays a task.

The runner retains source handles and native baselines within a 128 MiB run-wide budget, refusing the next PDF task
before sending it if retention cannot fit. Artifact-oracle source byte caches are cleared after use. Finalization
has a separate 30-second budget and reuses one bounded final native inventory; limits do not become empty counts.
The original conversation census remains in `pdf.census`; `pdf.post_stop` records the distinct later observation.

Only a verified clean owned-child exit, complete owner-bound census, unchanged source, zero pending operations and
no unexplained foreign changes can establish `native_post_stop_observed`. For a canceled forbidden-output case,
the current native interaction and, when present, protected form job must still match the cancellation witness.
Zero owned writes, media intents and document deliveries, plus a complete client window with no observed media,
can then pass `pdf_no_write` for a successfully executed cancellation. Exit zero or final prose
alone cannot. Late forbidden writes and changed sources fail even if the earlier conversation looked safe.

Batch finalization explains only known cohort effects: an unchanged write hash previously bound to that trial's
native settlement, or a terminal native outbox record whose route and platform IDs match actual observed messages.
Unknown routes/IDs, pending records, unexplained changes and disappeared records remain non-passing uncertainty.
Records are still decoded and raw-scanned before this filtering; cohort membership is not a blanket ignore rule.

Source removal additionally requires current logical settlement, a completed raw profile scan, original workspace,
parent/directory/file identities, pinned source bytes and a directory containing only that source. Unexpected files,
replacement objects and incomplete scans are retained rather than recursively removed. The receipt reports only
this owned fixture cleanup, not deletion of native history, protected state, outputs or all temporary resources.

`pdf.post_stop` never grants whole-runtime privacy, positive PDF delivery qualification or PDF judge/regrade eligibility.
Those witnesses and real-model qualification remain mandatory. These deterministic controls use native stores and
an owned protocol child stub; they are not evidence of a live model/backend scenario passing.

## Post-Stop Raw File Observation

An owned `eval run --start-target` retains bounded, ephemeral copies of resolved PDF facts until its gateway has
been stopped and the runner has inspected raw files under the admitted profile home. This includes files written
only during shutdown, before any sanitization or deletion. Attach mode cannot claim this observation. The run-owned
fact budget is 32 MiB; exceeding it refuses the next PDF attempt before sending its task.

The scan is bounded to 30 seconds, 8,192 entries, 128 MiB total and 8 MiB per file. Root `.security` and `keys`
directories are authorized credential/key stores and are excluded. Symlinks, special files, read failures, limits,
opaque binaries and unscannable escaped content leave coverage unavailable. Known literal facts in unexpected binary
files still fail. Source PDFs and independently retained, native-settled output PDFs are exempted from fact matching
by exact SHA-256, since those are intended document contents; credential literals remain forbidden there.

The value-free `private_surfaces` receipt contains only scope, counts and an observation code. A known leak is a
run-level hard failure; unavailable coverage prevents a passing run. Regrading refuses known-leak sources and
preserves unavailable coverage without rescanning or target work. Comparison retains both run verdicts separately
from per-case trial counts. Older reports without this optional receipt do not acquire coverage retroactively.

`owned_profile_files_v1` is a bounded file observation, not proof of native idle, no-write, delivery uniqueness,
complete transport/encoded-data privacy or cleanup. Forced/unobserved shutdown remains independently non-passing.
The final value-free run receipt and summary are written after observation. Required per-trial privacy checks
remain inconclusive until the independent witnesses are complete; this does not qualify E3.

## Dialogue Admission

The prepared profile must declare `pdf` and `interactions`, enable the document tool with an enabled native audit
model, and allow only `document` and `request_user_input` under a deny-default tool policy. Exec, MCP, browser,
hooks, trace capture, external channels, reload/deploy, broad path permissions, and approval bypass remain refused.
Undeclared document access and unadmitted audit fallbacks are refused before sending a task.

The common conversation runner owns streaming, request correlation and follow-ups. The PDF adapter substitutes
the owned source path, admits native public or protected question prompts, and binds all protected answers to one
source-pinned, authenticated job and owner. Raw WebSocket frames, including discarded heartbeats/auxiliary frames,
are inspected before map decoding; duplicate keys cannot hide an earlier private value. A confirmed private value
is a hard failure. Truncated, opaque escaped or otherwise unscannable evidence is unknown, not proof of a leak.
Only a non-secret protected-input marker enters retained transcript evidence; the actual value travels in the
ephemeral native `/answer` request. Ordinary scripted slash commands cannot bypass this path.

Only the narrowly admitted write-intent path below can send `allow_once`. A public native job state is a value-free
observation, not a
claim of artifact correctness, no-write safety, complete privacy coverage or remote cleanup. All attempted PDF
fixtures are conservatively retained while native settlement is unknown. PDF evidence is not yet eligible for
judge-only regrading. Later write/delivery witnesses must replace these explicit unknowns before E3 exits.

## Question Cancellation Receipts

An ordinary `request_user_input` cancellation uses the shared native question observer. A protected document
question additionally requires a source-pinned, authenticated job owned by the admitted route. The waiting prompt
must match its native execution request, complete workspace/turn scope, and delivered prompt receipt before the
shared runner sends one prompt-bound cancel. Observation never invokes a form-store lifecycle operation.

A protected cancellation final can omit LLM trace scopes, but must retain the expected agent/session and question
controls-removal metadata. Its corresponding native question must advance to `cancelled` with unchanged origin,
route, questions and protected binding, no answer, and the native stop failure code. The authenticated job must
advance to `canceled`, retain its owner/schema/source identity, and have its public fields and review/assignment
digests erased. Both terminal timestamps must fall after cancel preparation and before observation.

The value-free `pdf.cancellation` receipt records question/request/delivery identities and question/job revisions.
A final message alone, only one terminal record, changed identity/source, or unavailable evidence cannot establish
cancel completion. Known private values in raw native evidence are hard failures before projection. An unsettled
observation remains inconclusive; no input is replayed and no timeout is interpreted as cancellation.

This proves cancellation of one native question and its bound job, not whole-runtime idle, absence of other writes
or deliveries, complete privacy coverage, or owned-resource cleanup. Those required checks remain independent and
unknown without their own witnesses. Offline native controls are not live PDF case qualification.

## Reviewed Write Intent

For a case requiring PDF output, the PDF-specific user prompt may return a typed `confirm` after a visible review,
then `allow_once` at the native approval prompt. Neither intent supplies job IDs, prompt IDs, arguments or authority.
General conversation and forbidden-output cases keep the original deny-default parser. Scripted write intents are
valid only for required-output PDF cases with the corresponding final/approval expectation.

Confirmation requires the current authenticated, source-pinned job to be native review-ready, with a current
review revision, assignment/review digests, no audit blockers, the requested complete/draft scope, and the same route.
The runner sends a separate ordinary confirmation request. Approval requires a native waiting/delivered document
prompt whose original execution context names that exact confirmation request and client session. The current job
must be awaiting approval, and its frozen native binding must retain the confirmed review, source, owner, assignment,
schema/backend/audit revisions, output scope/policy and expiry. The native HMAC helper verifies the exact native
approval argument map using an existing admitted key; no key, arguments, grant or store state is created by observation.

Only one prompt-bound `allow_once` request can be constructed. Any ordinary follow-up invalidates the prior
confirmation, and a native correction invalidates its review/binding. Permanent and unrelated approvals stay refused.
Native ingress and the document tool still authorize and consume the grant; the harness does not perform the commit.

Reports retain a value-free `write_intent` with review revision and confirmation/interaction/operation identities.
This is not evidence of consumed approval, successful write or delivery. Without settled operation/outbox/artifact,
privacy and cleanup witnesses, required PDF checks remain unknown and the evidence cannot be regraded as qualified.

## Native Settlement Receipts

The observer retains the exact `allow_once` request and waiting native record. A settlement receipt requires the
same resolved, allowed, non-superseded answer, its consumed grant before expiry, original confirmation execution
context, unchanged frozen review binding, and authenticated prompt delivery. An unconsumed intent is insufficient.

The owner-bound completed job must name the same operation and artifact digest. The native write-journal decoder
validates the exact bounded bytes after raw privacy scanning, without acquiring write authority, opening a second
snapshot, or changing the journal. The operation must be delivered, verified, and bound to the same source/artifact.
Its exact outbox intent must be delivered with recovery settled, no pending transcript projection, one platform
message ID, and matching document recovery identities, hashed owner, route, job, operation, and artifact.

`pdf.settlement` contains only identifiers, timestamps, revisions, backend generation, digest, and size. It is not
proof that another outbox intent did not duplicate delivery, that PDF bytes match those receipts, or that all runtime
surfaces/resources are private and closed. No required PDF check becomes passing from metadata alone. A one-shot
observation that races unfinished native settlement remains inconclusive; it never replays input or cancels a job.
The test controls drive the native registry/store/journal/outbox state machines but do not run a PDF worker or model.

## Client Media Observation

The shared client can observe decoded messages in its sole reader after raw privacy inspection and before queueing
or heartbeat discard. PDF runs capture a bounded projection of attachments even when the message has no request ID.
The observer never retains captions, filenames or complete protocol maps. `Close` joins the reader before the PDF
adapter examines the projection, including a message admitted beside a final reply or during local closure.

`pdf.wire` binds the observed platform message ID, delivery ID, session/agent/route and exact native artifact download
path to `pdf.settlement`. Its `native_bound_client_media_v1` scope covers only that client connection through local
closure. A matching artifact in two different logical message IDs fails; an identical repeated message ID is one
logical observation. Conflicting metadata for the bound message or a witnessed binding mismatch is a hard failure.
Limits are 16 unique media messages, 16 distinct metadata variants per ID and 16 attachments per variant, with bounded
identity/URL/type fields. Identical retries do not consume the variant budget. All retained variants contribute to
duplicate detection; conflicting repeats cannot hide an already observed delivery. Malformed,
missing, unbound, limited or incomplete evidence remains unavailable; a known duplicate still fails despite a limit.

The receipt exports only scope, completion, counts, code and a native-bound message ID. It never follows a URL from
the wire: authenticated download still uses only the admitted gateway and native artifact reference below.
No native receipt or downloaded PDF can substitute for the actual wire message. Conversely, one observed message
does not establish no future duplicate delivery, PDF content, whole-runtime privacy or cleanup; required checks and
PDF regrade eligibility remain pending until the other independent witnesses are complete.

Owned runs inspect every admitted raw frame against the retained union of all resolved PDF trial facts, including
values from earlier trials and discarded pongs. The serial run adds facts only after the previous client's reader
has joined. A later general trial therefore cannot hide an earlier PDF canary in an ignored frame. This is a raw
client-frame guard, not a claim that all role inputs or every runtime surface have complete privacy coverage.

## Authenticated Artifact Checks

The runner derives the download endpoint from its admitted local gateway and native media reference, never a URL
in an assistant reply. It sends the channel token only in an Authorization header. No proxy, redirect, compression,
retry or wire-supplied filename/path is accepted. The body is limited to the admitted native size plus one byte,
at most 64 MiB, and must match the native SHA-256, size and PDF content type. An observed metadata mismatch is a
hard failure; unavailable, interrupted or unauthorized download remains unknown.

The exact PDF is retained as a private `artifact-<trial-id>.pdf` alongside private evidence, without overwriting an
existing file. Binary artifacts have a separate 64 MiB bound; JSON reports remain limited to 8 MiB. PDF bytes can
contain synthetic private answers and must never be published or supplied to the user/judge models. JSON evidence
contains only a relative file reference and the value-free oracle projection. Retention failure cannot pass.

Pass `--pdf-oracle-python /absolute/private-eval-oracle/bin/python` to `eval run`, or set
`MINTCLAW_EVAL_PDF_ORACLE_PYTHON`. The pinned independent oracle consumes source/artifact bytes and ephemeral field
expectations over stdin. Its predicates determine `pdf_fields` and `pdf_structure`, including visible appearance
and preserved background. Unavailable oracle evidence remains inconclusive, even after successful delivery.

A one-second read-only grace handles native receipt transitions that finish just after a final response. This
does not resend an input, grant permission, infer cancellation, or make a timeout successful. Native receipts and
artifact checks still do not establish delivery uniqueness, whole-runtime privacy or cleanup. Those required
checks remain unknown, and this packet does not enable PDF judge-only regrading or claim live-model qualification.

## Optional Dependencies

The oracle uses [pypdf](https://pypdf.readthedocs.io/en/stable/user/forms.html) for structure/value readback and
[PyMuPDF](https://pymupdf.readthedocs.io/en/latest/widget.html) for widget text and raster observations, independently
of production pdfcpu/PDFium. Direct and transitive versions are pinned in
[pdf-requirements.txt](../../evals/oracles/pdf-requirements.txt). Reportlab generates fixtures and negative controls.
None of these packages is linked into the Go binary or installed into the production gateway by this packet.

PyMuPDF has an [AGPL or commercial license](https://pypi.org/project/pymupdf/). Operators must admit that dependency
for their manual eval environment; this is not a claim that a subprocess boundary removes license obligations.
The environment is optional for offline catalog commands, general conversations, and ordinary CI.

```sh
uv venv /absolute/private-eval-oracle --python 3.12
uv pip install --python /absolute/private-eval-oracle/bin/python -r evals/oracles/pdf-requirements.txt
/absolute/private-eval-oracle/bin/python -B evals/oracles/test_pdf_oracle.py
MINTCLAW_EVAL_PDF_ORACLE_PYTHON=/absolute/private-eval-oracle/bin/python \
  go test -tags goolm,stdjson ./internal/eval -run '^TestPDFOracleManualQualification$' -count=1
```

The Go adapter runs the embedded script in isolated Python mode with a minimal environment. One bounded JSON
metadata line and exact-length source/artifact bytes travel over stdin, not argv, temporary files, or model context.
Bounds: 64 MiB per PDF, 32 pages, 256 unique flat text/checkbox fields, 16 million rendered page pixels, 8 KiB returned
output, one minute of child execution. The child starts no further subprocesses. This does not certify hostile-PDF
sandboxing, prove target isolation, download artifacts, or grant approval.

Errors are value-free; parser diagnostics are discarded. Results contain code/version fingerprints, source/artifact
digests, predicates and actual counts only. Missing dependencies, malformed/unsupported inputs, exhausted bounds
or process failure cannot pass. Complete observed mismatches remain explicit negative evidence.

## Coverage And Limits

- Exact field values, preserved/blank values, and checkbox state semantics.
- Editable field names/types/flags, original widget geometry, page count, and absence of XFA.
- Text extracted within widget bounds must match the expectation. The observed raster differs from an independently
  regenerated whitespace-only/unchecked appearance exactly when nonblank data is expected.
- Page rasters preserve background outside masked widget rectangles. This conservative exact comparison is for
  these synthetic fixtures, not a universal font/layout equivalence metric.
- Controls: correct independent fill, unfilled fake success, old corrected value, stale/invisible appearance,
  altered page background, wrong schema, malformed private input, and serialized empty required values.

This is not full OCR, proof of glyph shapes in arbitrary fonts, repeated/hierarchical widget or hybrid/XFA support,
native audit/approval proof, or logical delivery proof. E3 must combine the oracle with native source/job/operation/
outbox identity and the authenticated observed artifact, not merely a caption or session presence. Raw non-artifact
surfaces require their own bounded private-value scan before judging.

## Regenerate

```sh
/absolute/private-eval-oracle/bin/python -B evals/oracles/generate-pdf-fixtures.py
mintclaw eval validate --root ./evals --suite pdf --json
```

Generation reuses PDFI3 small/conditional/large layouts and adds an empty-required variant. It is not run during
ordinary evals or required CI. Intentional fixture changes require visual review, case digest updates, and renewed
oracle controls; changed fixture bytes are never silently admitted.
