# PDFI2 agent-led form dialogue test

This runbook checks the PDFI2 operator outcome on a supported AcroForm without prescribing field IDs or raw document
tool actions. Use synthetic data only. The form must not be submitted anywhere.

## Prerequisites

- MintClaw is running from a revision that contains the PDFI2 exit.
- The active profile has the bundled PDF skill, deferred `document` capability, protected interactions, artifact
  delivery, and any configured final-action approval enabled.
- Use a supported AcroForm that has at least one existing value, one missing required text field, and one optional
  field. Record its source digest before the test.

## Normal channel path

Attach or authorize the form and send an ordinary request like this:

> Fill this form with me. First explain what it is and give me a short plan. Ask only for information that is actually
> missing, preserve existing values unless I ask to correct one, and wait for my final confirmation before writing or
> sending a result.

The first response must summarize the form and the bounded collection plan before asking for a value. During the same
job, exercise these paths where they are valid:

1. Ask what one question means. The explanation must not become the field value.
2. Give a free-text answer as a reply to the active protected prompt.
3. Use Back, then answer the revisited field.
4. Skip one optional field.
5. Correct one existing or already answered field in ordinary language.
6. Ask for status. It must report value-free progress without exposing protected answers.
7. At review, check the confirmed, preserved, skipped, unresolved, and blocker counts.
8. Send a separate final confirmation and approve the exact write action if approval is configured.

The result passes only if the source digest is unchanged, verification succeeds, and exactly one new PDF attachment is
delivered after final confirmation. Inspect the resulting PDF independently.

## Cancel path

Start a fresh job with the same ordinary request, then use the channel's Cancel action while a protected question is
active. The job must become canceled, no value may be appended for that question, and no output artifact may be
created.

## Headless live-client path

`mintclaw agent live` is useful for deterministic automation, but it is not a chat transport reply. Use
`--auto-interaction-choice` for typed clarify, back, skip, or cancel. Use `/answer <short-id> <value>` or
`--auto-answer-question` for a protected value. Sending an unrelated `--message` while a protected question is active
is intentionally treated as new conversational guidance that supersedes the question; it is not a protected answer.

Do not put secrets directly in shell history. Generate synthetic values inside the test process, and remove temporary
plaintext files after the privacy scan.

## Required assertions

- preparation itself does not suspend or select the first field;
- every question is agent-selected and bound to one schema-valid stable field;
- clarify and back do not mutate the protected ledger;
- skip/not-applicable are typed intents and never literal field text;
- cancel terminates the job without delivery;
- protected values are absent from model history, public state, traces, logs, and delivery metadata;
- review is a hard boundary before the write action;
- final approval authorizes one reviewed transaction;
- structural and visual verification succeed;
- exactly one verified PDF is delivered;
- the source stays immutable across success, cancellation, restart, and retry.
