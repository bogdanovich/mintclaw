# Background PDF Draft: Short Telegram Check

This is a manual acceptance recipe, not a claim that Telegram passed. Use only
the generated synthetic Community Equipment Loan PDF from the
[PDFI4 operator recipe](pdfi4-free-text-form-test.md#generate-unrelated-fixtures).
Upload it from your computer; do not paste a server path.
Keep any real form draft untouched and use a new disposable test task/topic.

Status on 2026-10-25: the
[automated foundation check](../architecture/pdf-intake-background-draft-foundation-qualification.md)
passed native values/exclusion, unrelated current requests and safe draft
return. Cross-turn unrelated history is **not yet qualified**: the existing
live-only policy loses the earlier user request. Step 4 below tests the desired
completed behavior, not a claim that the current release passes it.

1. Attach the PDF with: “Новая отдельная тестовая задача. Я заёмщик. Объясни
   документ и сгруппируй необходимые сведения. Помоги заполнить обычными
   сообщениями. PDF пока не создавай, ничего не подписывай и не подавай.”
2. After its reply, send: “Я заёмщик Casey Mint. Контакт на случай чрезвычайной
   ситуации — Robin Mint. Телефон 202-555-0147; пока не указываю, чей он.”
   Expect both names accepted and a focused question about ownership of the
   telephone, not an assignment to either person.
3. Send: “Предыдущий телефон принадлежит только заёмщику. Телефон контактного
   лица пока не указан. Город заёмщика Mintford.” The original ownership
   ambiguity is settled. The contact's telephone remains genuinely missing;
   it is valid to ask for that missing number later, but not to ask whether the
   excluded number should be copied to the contact.
4. Send `test`. Then invent three fresh arbitrary word-game labels, send them
   in order and ask for only “принято”, without repeating the labels. Ask which
   label was second, without supplying it again. Expect ordinary conversation
   and the correct historical reference, not a task/form choice or a guess.
   If the acknowledgement echoed the labels, this does not establish retention
   of the user's request; use fresh labels. Arithmetic followed by “Почему?”
   alone is insufficient proof of historical continuity.
5. Send: “Давай вернёмся к предыдущей форме. На чём остановились? PDF пока не
   создавай.” Expect the same draft and accepted data, with only missing or
   genuinely uncertain facts requested. No repeated ownership question.

Optional pending-question check: upload an unrelated small PDF without saying
what to do with it. A scope question is legitimate here. Instead of selecting an
option, ask unrelated arithmetic, then return to the equipment form. The new
file must not be processed or considered approved; the old unresolved request
must not repeatedly block the conversation or be reopened by a redundant resume.

When finished, cancel only this disposable test draft using `/cancel_form`.
This is test cleanup, not a prerequisite for switching conversation topics.
No output is requested or approved in this recipe. Report reply/trace IDs and
unexpected behavior; do not repeatedly resend a finish or external action.
