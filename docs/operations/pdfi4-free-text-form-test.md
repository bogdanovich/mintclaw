# PDFI4 Free-Text Form Intake Operator Test

## Status And Prerequisites

This is a reproducible test recipe, **not a qualification report or completion claim**. Run it only after the
protected-intake composition and its backend prerequisites are merged and deployed. Until the
PDFI4 exit report records successful qualification in the
[roadmap](../architecture/pdf-agentic-intake-roadmap.md#pdfi4-coherent-sections-and-large-form-qualification),
expected outcomes below remain assertions to verify, not established guarantees.
The [goal contract](../architecture/pdf-agentic-intake-pdfi4-goal.md) is authoritative.

Use the main profile with the bundled PDF skill, enabled document tool, a working audit model, protected interactions,
normal final-action approval and PDF delivery. Record the deployed SHA and effective skill before testing. Do not enable
approval bypass, use real personal data or submit a form externally. A stronger model alone does not prove correct intake.

Attach PDFs from your own computer to the actual Telegram chat. A path on your laptop is not a path on the gateway.
Do not send a bare absolute path as a slash command. Headless `agent live` can provide additional evidence, but cannot
replace actual Telegram user messages, replies and callbacks.

## Generate Unrelated Fixtures

From a repository checkout, create a fresh fixture directory and generate the medium form:

```sh
pdfi4_test_dir=$(mktemp -d "${TMPDIR:-/tmp}/mintclaw-pdfi4-test.XXXXXX")
uv run --with reportlab==4.4.10 python docs/operations/pdfi4-fixtures.py "$pdfi4_test_dir"
```

The generator refuses to overwrite its PDF. It reuses the PDFI3 layout helpers and emits an invariant three-page
equipment-loan AcroForm: two different people, eight fields each, a prefilled equipment field, optional delivery note,
checkbox and read-only handwritten signature. There are 20 fields, 19 writable. The expected SHA-256 is
`ca3ea05ef03e6356ab5ea3ab8170d6712fcf6c60b9198de16d1b5db17efeaeff`.

For the additional small, conditional and ten-page/100-field sources:

```sh
uv run --with reportlab==4.4.10 python docs/operations/pdfi3-fixtures.py "$pdfi4_test_dir"
```

Keep original PDFs separate from downloaded results. Record source hashes before and after the test. The examples
below are public synthetic values, not privacy sentinels. Formal privacy qualification must use fresh distinctive
synthetic literals held privately, never published in a PR or retained as ordinary agent context.

## Complete Medium Form

Attach `pdfi4-medium.pdf` in Telegram with:

> Заполни эту форму вместе со мной. Это технический тест, ничего никуда не подавай. Я заёмщик оборудования.
> Сначала объясни, что это за документ и какие данные нужны, сгруппировав их по смыслу. Спрашивай только
> недостающее или неоднозначное. Уже заполненное сохрани, подпись не заполняй.

The response must understand the actual form, distinguish borrower from emergency contact, present grouped needs
and explain protected ordinary-text intake before collecting values. It must not ask again who the user is when that
role was already supplied. Wait for the active form intake before sending personal facts.

Send these as three **ordinary standalone messages**, not `/answer`, JSON, field IDs or replies to the old question.
Wait for an acknowledgement between them for the baseline test; test rapid consecutive ingress separately.

> Я заёмщик, Casey Mint. Родился 1989-02-15, почта casey.mint@example.invalid, телефон 202-555-0147.
> Живу по адресу 14 Mint Lane, город Mintford, индекс 20500, страна United States.

> Мой контакт на случай чрезвычайной ситуации — Robin Mint. Дата рождения 1975-11-23, почта
> robin.mint@example.invalid, телефон 202-555-0168. Адрес: 8 Paper Road, город Paperport, индекс 10001,
> страна United States.

> Верну оборудование по почте. В примечании для доставки укажи Leave the camera at reception.
> Существующее название оборудования сохрани. Подпишу от руки.

The first paragraph must accept at least five distinct facts. All three messages must augment one source-bound job,
including fields beyond its first eight-field window. Progress must say what was accepted or needs clarification
without echoing protected values. An already accepted surname/name must not be asked for again.

If a checkbox phrase is ambiguous, the agent may ask a targeted confirmation; it must not store the whole sentence
as a checkbox value. Answer using the offered control or ordinary protected clarification. No silent guessing.

Then send:

> Исправление: мой телефон должен быть 202-555-0193. Телефон контактного лица не меняй.

Only the borrower's phone may change. Any earlier review/approval must become stale. Ask for progress if needed;
the public response must remain value-free. When a new review appears, check applicable missing/conflicting counts
and preserved equipment/signature. **Review itself must not create or deliver a PDF.**

In a new message after review, send:

> Проверку посмотрел. Создай итоговую заполненную копию.

Approve the exact document action using its current confirmation control. Do not approve unrelated or stale prompts.
Expect one logical PDF, not a caption claiming completion. Independently inspect all pages and field values:

- the 16 person facts are in the correct roles, including the corrected borrower phone;
- return-by-mail is checked and the delivery note matches the supplied text;
- `Synthetic Mint Camera` and the untouched read-only signature remain unchanged;
- no text is clipped or invisible; page count/background remain intact;
- the source hash is unchanged and exactly one result was delivered.

This ordinary AcroForm result must retain its form structure. Native validation and independent field/visible
verification are required; a model saying “done” is not proof. Keep the downloaded artifact privately.

## Editable Partial Draft

Start a **new** job from the original medium PDF, send the same initial request and only the borrower paragraph.
Then send ordinary text:

> Контактное лицо и все остальные незаполненные поля оставь мне. Сделай редактируемый частичный черновик,
> я заполню их вручную. Уже введённые данные и существующее название оборудования сохрани.

The agent must distinguish deferred fields from intentional blanks, N/A, unresolved values and preserved facts.
It must not invent contact data or claim the application is complete. After review, send a separate finish message
and approve the current action. Independently verify one editable PDF: all eight borrower facts present, deferred
contact/arrangement fields unchanged, equipment preserved, signature still read-only and remaining widgets editable.

Repeat on the PDFI3 small source while leaving its required contact reference for later. The draft must retain the
source Required flag and a clear remaining-required warning. A subsequent full-completion request must not inherit
partial scope to bypass required missing-data checks.

## Clarification, Controls And Recovery

Use fresh jobs as necessary; do not corrupt the baseline transaction while trying every negative scenario.

1. Supply an unqualified name when both people are candidates. Expect a role clarification, not copying to both.
   Also test two distinct people with the same name and one telephone whose owner is initially unspecified.
   Clarify which person owns it without repeating the number; accepted names must not be requested again,
   and the other person's telephone must remain unfilled unless explicitly supplied or intentionally blanked.
2. Mix a fact with “что означает это поле?”. The explanation request must not be written wholesale into a field.
3. Give an invalid date or contradictory phone. Only targeted clarification/correction may resolve it; other
   accepted facts must survive unchanged.
4. Exercise the currently offered Clarify and Back controls. Navigation must not append an answer or reset identity.
5. Use `/pause_form`, then `/resume_form`; the same job/source and accepted receipts must survive. Natural pause
   intent may also be interpreted during active intake. While paused, use the explicit resume control: new fact
   messages are refused rather than replayed into ordinary chat.
   If processing fails, use `/retry_form` to retry retained inputs in the same active form. This control does not
   ask for the facts again, add another protected message, reset extraction limits or approve output. Resume a paused
   form first; an empty, mismatched or closed intake cannot be retried.
6. Use Cancel or `/stop` while collection is active. Expect acknowledgement, cleared controls, no output and terminal
   protected-state cleanup. Do not keep supplying facts after cancellation.
7. With an operator, test an actual gateway restart and actual compaction mid-intake. Confirm same identity and no
   repeat question for already accepted facts; record the real transition, not a mock or assumed restart.
8. Protocol tests separately replay an exact transport ID, a stale receipt and a lost delivery acknowledgement.
   Forwarding/copying text in Telegram creates a new message and does **not** prove transport idempotency.

After a failure, preserve the source/job/interaction/operation/trace identifiers and exact error code, not protected
values. Do not resend finish/approval blindly or create a second job merely because a response timed out.
`/retry_form` cannot recover an exhausted persisted extraction budget by waiting or resetting it. Retain the
original task and obtain an explicit decision before substituting a separately labeled fresh qualification;
do not describe that new test as recovery of old input. Native capacity state outranks a model's “temporary” wording.
Also compare acceptance counts with the failure stage: a turn can accept the new clarification while older retained
input is still capacity-blocked. Do not treat “could not apply” prose as authoritative or resend confirmed facts.
The 2026-10-18 fresh I-134 run exposed this incorrect reporting/repeated-question behavior. The later
[bounded repair checkpoint](../architecture/pdf-intake-clarification-checkpoint.md) records the generic fix and
automatic qualification, not recovery of that exhausted input or completion of I-134.

### Bounded clarification repair check

The [clarification repair goal](../architecture/pdf-intake-clarification-goal.md)
does not qualify I-134/XFA. On a **new unrelated synthetic form**, use this short
manual Telegram scenario after the repair is deployed:

1. Attach the generic fixture with: “Заполни тестовую форму. Я заёмщик.
   Объясни документ и сгруппируй нужные сведения. Ничего не подписывай,
   не создавай итоговый PDF и никуда не подавай.” Wait for active intake.
2. Send: “Я заёмщик Casey Mint. Контакт на случай чрезвычайной ситуации —
   Robin Mint. Телефон 202-555-0147; пока не указываю, чей он.” Both names
   must be accepted, but the telephone owner must not be guessed.
3. Send: “Предыдущий телефон принадлежит только заёмщику. Телефон контактного
   лица пока не указан. Ещё одно сведение: город заёмщика Mintford.”
   Previously accepted facts must survive; only genuinely unresolved details
   may be asked again. A real conflict must not disappear just because that
   field also has a previously confirmed value.
4. If older text remains pending, the response must distinguish the new
   accepted decisions from that retained input. It must not say the new answer
   failed wholesale. Do not resend accepted facts.
5. A normal new message must not automatically revisit already completed
   unrelated extraction windows. `/retry_form` deliberately retries its selected
   retained receipt, using the same persisted budget; it is not a reset.
6. At `capacity_exceeded`, stop blind retries and retain the job for diagnosis.
   No replacement task, limit reset or claim of completed output is implied.

For this short check, finish with `/stop` rather than requesting a PDF. Expect
acknowledged cancellation and no document delivery. Keep the reply/trace IDs
for verification. The automatic checkpoint uses authenticated headless gateway
ingress; this actual Telegram scenario is still an operator acceptance step.

For operator verification, `received_input_state=accounted` means the newly
received message has been accounted, not that the whole form is finished.
`accepted_count` and `acknowledged_count` describe this continuation;
`progress.pending_inputs` still fences review/output. `retained_finding_count`
includes native older findings even when advisory questions about already
confirmed/preserved fields are omitted from `findings`/`finding_fields`.
That presentation filtering never deletes native findings or pending text.
It applies only to older advisory findings backed by a strictly later,
still-current native decision for that same field. Existing confirmation or
an unrelated later update is not enough to hide a genuine new ambiguity.
The same chronology rule applies when `/retry_form` selects the old receipt:
receipt selection must not make superseded advice into a missing-field
question again. It still cannot extend an exhausted extraction budget.

## Large Public Form And Unsupported Output

The official I-134 is a separate required complete-applicable transaction, not a substitute for the unrelated form.
It is **not fully qualified today**. The fresh 2026-10-18 test found three applicable read-only source fields
controlled by XFA conditional/calculation scripts; the current backend neither executes those scripts nor writes
those fields. The operator deferred that capability to the
[separate follow-up](../architecture/pdf-conditional-calculated-fields-roadmap.md). Do not bypass flags, omit
these targets from a full-output oracle, or label a static subset as a complete I-134. Test ordinary-form intake
independently while this official-form gate remains open.
Use the pinned edition/hash/provenance from the goal's qualification record, synthetic sponsor/beneficiary/address/
financial data and actual role/applicability instructions. Do not infer legal or financial facts, sign, certify or submit.
Inspect every visible page independently and correlate the one delivered PDF to the verified native operation.

On the admitted Linux hybrid lane, output is flattened and print-ready. Editable hybrid/I-134 output is deliberately
deferred. An explicit request to finish missing fields manually must receive a truthful limitation and retain the job;
it must not silently return a flattened partial PDF while promising editability. See
[hybrid output](../guides/document-forms.md#hybrid-acroformxfa-print-ready-output).

The large synthetic fixture also exercises bounded browsing, retained facts beyond eight-field views and truthful
budget exhaustion. An all-schema prompt, silent truncation or a first question is not large-form qualification.

## Record A Pass

Record deployed SHA, effective model/skill, fixture edition/hash, actual channel, same-job revisions, interaction and
write identities, delivery count, source hash, private artifact hash/size, independent field/render results, and
protected-store cleanup. Scan fresh protected sentinels across ordinary history, summaries, logs, traces and public
projections; exclude only explicitly private input/oracle/artifact surfaces. Truncated or unavailable evidence is
unknown, not a privacy pass. Correlate recovery and cleanup with actual native state.

Full PDFI4 completion additionally needs all goal acceptance rows, merged code/review/CI/approval gates, coherent
deployment backup/rollback and a merged exit report. Passing only this happy path, or merging this runbook, is not done.
