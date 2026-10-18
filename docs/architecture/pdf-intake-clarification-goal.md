# PDF intake clarification repair

## Scope

This is a bounded follow-up to the negative continuation recorded in PR #1726,
not completion of PDFI4. In trace
`trace-turn-a9505093edbe00f8d902634c`, four new decisions were accepted while
older input exhausted its extraction budget. The final response incorrectly
said the new decisions had not been applied and asked about confirmed facts.

Repair the general intake mechanism, without document-specific rules:

1. A new message must not automatically repeat already completed, unrelated
   extraction windows on retained input. Unvisited windows and known blockers
   must remain eligible. Explicit retries retain their own trusted ingress
   identity and the original persisted extraction budget.
2. Distinguish accepted/acknowledged facts, the current message's accounting
   status, retained older input, actionable clarification and processing failure.
3. Do not ask again about confirmed facts merely because older interpretation
   advice remains recorded. Preserve genuine conflicting/invalid facts and all
   native accounting and output fences.

## Invariants and exclusions

- Keep owner, route, source, schema and revision binding, atomic native apply,
  protected values, review, independent verification and final approval.
- No budget increase/reset, input loss, retroactive editing of pass outcomes,
  new daemon/store/planner, form-specific behavior or new PDF capabilities.
- Historical I-134 jobs and retention remain unchanged. No new official I-134
  run is authorized by this goal. Exhausted-input recovery requiring new
  authority stays deferred rather than being disguised as a retry or fresh job.
- Conditional/calculated XFA and editable hybrid output are separate work.
  I-134 and PDFI4 stay unqualified until their independent acceptance gates pass.

## Done criteria

- A regression reproduces partial success alongside retained failure; tests
  distinguish stale advice from real conflicts and verify accepted values.
- Independent small/large generic schemas cover automatic continuation,
  explicit retry, replay, restart, exhaustion, privacy and owner rejection.
- All in-scope code PRs pass exact-head CI, substantive review and authorized
  rocket approval, and are merged through `mintclaw-autonomous-pr`.
- Merged code is deployed with verified backup/rollback and actual runtime
  source identity; a harmless synthetic real-agent continuation is verified.
  Existing historical jobs are not used as mutable test fixtures.
- Documentation records actual results, limitations and a short manual
  Telegram scenario. A headless gateway run is not claimed as Telegram proof;
  manual Telegram acceptance remains explicitly pending until the operator
  sends the messages and the resulting traces are checked.

The goal can finish after those automatic gates and the operator test guide.
It must not silently inherit the broader PDFI4 full-output exit criteria.
