# PDF3 conversational form test

This runbook qualifies the durable conversational AcroForm workflow on the supported `linux/amd64` deployment. Until
the PDF3 exit record is merged, the workflow is qualification-only and must use the checked-in synthetic fixture and
synthetic answers below. Do not use personal, legal, tax, medical, financial, or production form data.

The workflow keeps submitted values out of ordinary model history, task state, logs, traces, and delivery metadata. A
final mutation still requires an approval, uses PDF2's verified fill path, and creates one outbox-owned PDF delivery.

## Automated gate

Run the focused agent/channel and recovery series in a Linux AMD64 checkout with the pinned PDF backends installed:

```sh
make test-document-form-agent
```

Success includes every `MINTCLAW_PDF3_*_OK` marker and covers protected storage, natural interaction, restart and
compaction-independent state, mapping/review, approval, PDF2 commit, source immutability, delivered/failed/ambiguous
delivery settlement, no replay after reopen, privacy, cleanup, and the real agent/channel loop.

After deploying the exact merge under test, run the complete document series from the operator workstation:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

The command may use `server@oc-ts` only as the documented Tailscale fallback. It prints the deployed core SHA and ends
with `marker=MINTCLAW_PDF3_DEPLOYED_OK`. A marker from another revision is not deployment evidence.

## Prepare the deployed fixture

Copy the immutable checked-in synthetic form into the main profile's authorized workspace and record its digest:

```sh
ssh server@oc '
set -eu
source=/home/server/src/mintclaw/pkg/document/testdata/acroform-fields.pdf
target=/home/server/.mintclaw/main/workspace/manual-tests/mintclaw-pdf3-form.pdf
install -d -m 700 /home/server/.mintclaw/main/workspace/manual-tests
install -m 600 "$source" "$target"
sha256sum "$source" "$target"
'
```

The two digests must match. Keep this terminal open for the restart and final source check.

## Real Telegram flow

Use the existing General chat of the main profile. Send this exact first message:

```text
Заполни синтетическую PDF-форму
"/home/server/.mintclaw/main/workspace/manual-tests/mintclaw-pdf3-form.pdf"
через защищённый многошаговый document form workflow. Спрашивай значения по одному. Не используй shell,
обычное чтение файла или прямой fill. Перед изменением покажи review и запроси approval.
```

Exercise the interaction surface while answering the generated field questions:

1. Answer the first free-text question with `PDF3 Test User`.
2. For a displayed single-choice field, press one of its Telegram buttons. Do not send a `question_id` or `/answer`
   command.
3. Answer the other fields with synthetic values that match the displayed constraints. `09/19/2026` is a valid date
   value for this fixture. Use **Skip** or **Not applicable** once if the control is offered for an optional field.
4. Before final review, send `Исправь Full name на PDF3 Corrected User.` The next review must supersede the old answer
   and show the corrected field as resolved without exposing either raw value in ordinary diagnostic output.
5. While another field is pending, send an unrelated message such as `Какой сейчас статус заполнения?` The same job
   must resume without re-asking already confirmed fields.
6. Restart the gateway from the operator terminal:

   ```sh
   ssh server@oc 'systemctl --user restart mintclaw-main.service'
   ```

   Wait until `systemctl --user is-active mintclaw-main.service` prints `active`, then answer the pending question. The
   same job must resume. The automated gate is the deterministic proof that protected state also survives context
   compaction; ordinary Telegram has no operator command that safely forces compaction.
7. At review, verify that there are no unresolved, ambiguous, conflicting, invalid, or required-blank fields. Ask
   `Покажи статус и review, но пока не заполняй PDF.` if review is not shown automatically.
8. Ask `Заполни и проверь PDF по этому review.` Approve the single final mutation with **Allow once**. Do not approve
   any request whose operation or review changed.

Expected result: one response contains exactly one `filled-document.pdf`. The response reports a completed, verified
form job. It must not contain the local source path or submitted values as diagnostic metadata.

After delivery, send both messages below:

```text
Покажи статус этого form job. Ничего повторно не заполняй и не отправляй.
```

```text
Повтори результат предыдущего form job, только если это не создаст новую доставку.
```

The job remains completed and no second PDF is sent. A timeout, reconnect, restart, or ambiguous send result must never
be resolved by creating a second fill or delivery.

## Independent result and source checks

Download the one delivered PDF and verify it through the document service using its returned `media://` reference and
operation ID. The expected field assignments must verify structurally and visually; render every affected page and
inspect the PNGs. Then prove the deployed source did not change:

```sh
ssh server@oc '
sha256sum \
  /home/server/src/mintclaw/pkg/document/testdata/acroform-fields.pdf \
  /home/server/.mintclaw/main/workspace/manual-tests/mintclaw-pdf3-form.pdf
'
```

The digests must still match the preparation output. Do not treat opening the delivered PDF in a browser as the
authoritative verification; it is only a supplementary visual check.

## Cancel path

Start a second job with the same source. Answer one question, then send `Отмени заполнение этой формы.` Confirm its
status is canceled. Further answers, review, approval, fill, and delivery must be refused for that job.

## Privacy and cleanup

Use a unique synthetic sentinel for one answer, for example `PDF3-PRIVATE-SENTINEL-20260919`. After the job is terminal,
search the main-profile logs, passive diagnostic traces, ordinary history/task state, public job records, and outbox
metadata. The sentinel and the authorized host path must be absent. Protected key/value/event/pending material must be
deleted at successful completion; retained review, commit, and delivery evidence is value-free.

Remove only the copied manual fixture after evidence collection:

```sh
ssh server@oc 'rm -- /home/server/.mintclaw/main/workspace/manual-tests/mintclaw-pdf3-form.pdf'
```

The checked-in source fixture, delivered test result, and rollback backup are separate objects and are not removed by
that command. Record the exact deployed SHA, output operation ID, delivery count, trace identifiers, source/result
digests, rendered-page checks, privacy queries, service health, rollback backup, and cleanup in the PDF3 exit record.
