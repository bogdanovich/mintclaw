# Local Coding Agent P5.4: Rebuild and Resume Recovery

Status: implementation contract for roadmap item P5.4.

## Authority and recovery boundary

The per-thread JSONL transcript remains the complete authority. The thread's
Seahorse SQLite database, reconciliation watermark, summaries, and compaction
checkpoint are disposable derived state. Resume acquires the thread lease and
repairs dangling coding-tool lifecycle records before it exposes a controller.
It then reconciles the complete canonical transcript at one stable logical
revision.

A resumed controller is not returned until this reconciliation succeeds. The
recovery attempt has a 30-second budget. Large or contended histories that
cannot finish inside that budget fail closed with a reconciliation error; they
do not accept a turn using a partial tail or mark a partial derived database as
current. This is the bounded degraded-startup policy for P5.4. A future
read-only recent-tail UI may improve availability, but it must keep the
composer disabled until full reconciliation completes.

## Missing and corrupt derived state

A missing database is recreated at its admitted per-thread context path and
bootstrapped from canonical JSONL. Coding-only construction also recognizes
SQLite's explicit corruption diagnostics. It removes only the admitted
derived database and its WAL/SHM companions, opens a fresh database, and then
uses the ordinary stable-revision reconciliation path.

The recovery is deliberately narrow:

- personal-agent Seahorse construction retains its existing failure behavior;
- configuration, permission, path, schema, and arbitrary factory errors are
  not mislabeled as corruption;
- coding runtime path validation and the thread lease protect the database
  replacement boundary; and
- the reconciliation watermark is written only after a complete canonical
  bootstrap succeeds.

## Compaction checkpoint ordering

Every completed coding compaction carries the stable source transcript
revision established by reconciliation. The synchronous terminal lifecycle
observer atomically records that revision and completion time in
`thread.meta.json`. No-progress, interrupted, and failed attempts do not
advance `last_compaction`.

Metadata mutation and normal post-turn preview/model updates share one
in-process coordinator. Background compaction therefore cannot overwrite a
newer preview, and a turn update cannot erase a concurrent checkpoint. A
pre-commit metadata error leaves the prior descriptor authoritative; a
post-rename durability error retains the committed descriptor while still
surfacing the error during controller shutdown.

Summary creation remains safe if the process exits before the metadata update:
the summary is derived, canonical JSONL remains complete, and the next resume
reconciles against the canonical revision. Conversely, a metadata checkpoint
never authorizes use of an outdated derived database.

## Interrupted tools

Coding startup retains the existing durable tool-start repair contract. A
dangling call without a start marker receives an `interrupted` terminal result;
a call that crossed its durable start boundary receives `unknown`. Neither is
re-executed. Resume transcript hydration now renders those states explicitly
without exposing stored call IDs, arguments, or tool output.

## Verification

Regression coverage establishes that:

- the same canonical objective survives initial open, derived-DB deletion, and
  corrupt-DB replacement;
- only recognized coding SQLite corruption triggers derived-file replacement;
- resume reconciliation completes before a controller accepts work;
- only successful compaction progress advances durable metadata;
- metadata checkpoint write failure does not make an uncommitted descriptor
  authoritative;
- the synchronous frontend adapter delivers the correlated terminal lifecycle
  to the checkpoint observer; and
- recovered `interrupted` and `unknown` tools are visible without replay or
  sensitive output hydration.
