# Natural PDF form intake test

This runbook verifies that a normal operator request reaches the protected conversational form workflow without a
technical prompt. Use only the checked-in synthetic form and synthetic answers. Do not use personal, legal, tax,
medical, financial, or production data for this qualification.

## Automated gate

On a Linux AMD64 checkout with the pinned document backends installed, run:

```sh
make test-document-form-agent
```

Success includes `marker=MINTCLAW_PDFI1_NATURAL_INTAKE_OK`. The vertical test starts with a short ordinary request,
discovers the deferred document capability, inspects before starting the protected workflow, accepts ordinary free
text answers, advances through opaque receipts, commits after approval, leaves the source unchanged, emits one
verified PDF, and scans durable state and traces for protected-value leakage.

After deploying the exact merge under test, run:

```sh
scripts/document-deployed-smoke.sh --host server@oc
```

The result must name the deployed core SHA and include `marker=MINTCLAW_PDFI1_DEPLOYED_OK`.

## Prepare the synthetic attachment

Use `pkg/document/testdata/acroform-fields.pdf` from the deployed revision. Either attach that file directly to the
main profile's General chat or copy it into the authorized workspace and attach it from there. Record its SHA-256
before the conversation so the source can be checked afterward.

## Copy-paste operator request

Send the PDF with this message:

```text
Помоги заполнить эту PDF-форму. Спрашивай только недостающие данные, затем дай мне проверить результат и верни заполненный PDF.
```

Do not mention document tools, workflow actions, stable field IDs, interaction IDs, or answer commands. Expected flow:

1. MintClaw asks one form question. Answer it as an ordinary message; do not use `/answer`.
2. Continue with synthetic values matching the visible constraints. Use a unique sentinel such as
   `PDFI1-PRIVATE-SENTINEL-20260921` for one free-text answer.
3. Ask `Какой статус заполнения?` once. The existing job must continue without re-asking accepted values.
4. Before commit, ask to correct one named field in ordinary language. MintClaw must resolve the safe review field and
   must not ask you for a field ID.
5. Review the bounded summary. Ask MintClaw to finish and press **Allow once** if approval is required.

Expected result: one conversation produces exactly one `filled-document.pdf`. There is one channel-owned question per
missing value, no duplicate assistant paraphrase of a question, no second confirmation before approval, and no request
for technical identifiers.

## Independent checks

- The recorded SHA-256 of the source is unchanged.
- The delivered file opens as a PDF and shows the reviewed synthetic values.
- Asking for status after completion reports the same completed job and sends no second file.
- The unique sentinel is absent from ordinary history, task state, passive diagnostic traces, interaction records,
  public form-job state, and outbox metadata.
- The deployed aggregate smoke reports the exact active SHA and both PDF3 and PDFI1 success markers.

The browser preview is supplementary. The document service's structural and visual verification plus the operation and
delivery records are the authoritative result.
