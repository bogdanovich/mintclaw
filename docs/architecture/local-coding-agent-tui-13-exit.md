# Local Coding Agent TUI.13 Exit Record

Roadmap packet:
[TUI.13 — Composer, queued input, and visual hierarchy polish](local-coding-agent-codex-tui-roadmap.md#tui13--composer-queued-input-and-visual-hierarchy-polish).

The merge containing this record closes TUI.13. The coding composer is now a
compact, terminal-native input surface that grows with multiline content, and
same-turn guidance has an explicit causal presentation lifecycle instead of
appearing prematurely as submitted history.

## Shipped behavior

- The idle composer is one unboxed row with a `›` prompt and the exact
  `Ask MintClaw to do anything` placeholder. It grows up to four rows for
  multiline and wrapped input, then yields the remaining height to transcript
  history.
- Composer base, text, prompt, placeholder, line-number, cursor-line, and
  end-of-buffer styles inherit the terminal foreground and background. This
  removes the forced light/dark textarea background that could make typed text
  unreadable. The semantic transcript palette uses an explicit light or dark
  theme selected by `MINTCLAW_TUI_THEME=auto|light|dark`.
- Automatic theme selection reads the conventional `COLORFGBG` background
  index and falls back to dark. It deliberately does not issue an OSC terminal
  query, so startup cannot stall or consume user input through SSH or tmux.
- Pressing Enter during an active coding turn admits plain text through the
  existing same-turn steering API. Accepted guidance appears in a bounded
  `Queued guidance` surface below the working line, with `↳` previews, and is
  not inserted into transcript history yet.
- A runtime receipt promotes guidance into ordered user history only after the
  message has crossed canonical persistence, live provider-context insertion,
  and queue-head commit. Admission or persistence failure leaves the draft
  available instead of falsely presenting it as sent.
- Coding correlation IDs and receipt text are in-process presentation data.
  IDs are removed before provider construction, canonical session persistence,
  and turn-state retention; both receipt fields are excluded from event JSON
  and diagnostic traces.
- Attachments remain next-turn input while work is active because the runtime's
  same-turn media lifecycle is not yet a coding-frontend contract. The
  composer retains them and explains the constraint instead of dropping or
  mislabelling the payload.
- Existing structured rich-input markers remain unchanged:
  `[Pasted Content N chars]`, `[File: name]`, and `[Image #N]` still resolve to
  their private owned payloads at submission time.
- At terminal heights of one to four rows, the UI prioritizes a usable composer
  row and, while work is active, the live interrupt hint. Pending previews are
  bounded to five rows at normal heights and collapse into an overflow count.

## Causal and bounded state contract

`ThreadSnapshot.PendingInputs` is a cloned, byte-bounded presentation view.
Admission is scoped to the current active turn, duplicate IDs are idempotent,
and only the most recent `MaxSteersPerTurn` entries are retained. Completed,
failed, and interrupted turns clear their pending entries. A suspended turn may
retain already-admitted guidance until its continuation resolves it. The field
and its element fields use `json:"-"`, so neither an ordinary snapshot marshal
nor a direct pending-state marshal can produce the correlation ID or raw text.

The agent event bus continues to serialize only safe steering metadata such as
counts, lengths, roles, and hashes. Raw pending text is carried only by the
synchronous in-process coding adapter needed to render the user's own draft.

## Codex reference and deliberate differences

The implementation was checked against OpenAI Codex commit
`1a4096e273e80da30947e57fdfa45be92858ca91`, especially
`codex-rs/tui/src/bottom_pane/pending_input_preview.rs`,
`codex-rs/tui/src/chatwidget/input_flow.rs`, and the bottom-pane layout. MintClaw
adopts the terminal-native composer, restrained visual hierarchy, bounded
pending preview, explicit steer state, and separation between pending input and
committed history.

MintClaw does not reproduce Codex's full rejected-steer and editable follow-up
queues in this packet. Its current controller has one authoritative same-turn
guidance operation, so the UI names and projects that state directly. Transcript
search and copy/navigation remain the responsibility of TUI.14.

## Validation

The implementation is formatted with `make fmt` and covered by:

```text
go test -count=1 ./pkg/coding/frontend/... ./pkg/coding/tui \
  ./cmd/mintclaw/internal/coding
go test -count=1 ./pkg/agent \
  -run 'Test(PendingTurnInputReturnsCodingReceiptAfterDurableProviderInjection|ProviderPromptMessageForTurn_Wraps)'
go test -race -count=1 ./pkg/coding/frontend/... ./pkg/coding/tui \
  ./cmd/mintclaw/internal/coding
go test -race -count=1 ./pkg/agent \
  -run 'Test(PendingTurnInputReturnsCodingReceiptAfterDurableProviderInjection|ProviderPromptMessageForTurn_Wraps)'
scripts/pre-push-lint.sh --changed
```

Focused tests cover one-row and wrapped composers, light/dark theme selection,
terminal-color inheritance, active-turn admission, failure and attachment draft
retention, pending/injected transcript separation, duplicate receipts, count and
text bounds, JSON non-disclosure, structured paste/file/image markers, and the
two-row composer-plus-interrupt layout.

The full local `pkg/agent` suite retains the pre-existing macOS-only
path-canonicalization failure in
`TestNewCodingAgentLoopReadOnlyAuthorityOmitsMutationTools`: a temporary path
under `/var` is rejected after resolution through `/private/var`. TUI.12 proved
the same failure on clean pre-change `main`; all TUI.13-specific agent tests
pass with and without the race detector.

## Exit-gate decision

TUI.13's acceptance criteria are satisfied. Unified transcript navigation,
search, copy/accessibility, and the migration/performance parity closeout remain
TUI.14 and TUI.15.
