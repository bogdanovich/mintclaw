# Manual Agent Evals

This versioned corpus is for occasional real-agent evaluation, not required live CI.
The [design](../docs/architecture/agent-evals-design.md),
[roadmap](../docs/architecture/agent-evals-roadmap.md), and
[goal](../docs/architecture/agent-evals-goal.md) define the program.
The [core regression follow-up](../docs/architecture/agent-evals-core-roadmap.md) tracks the next general cases;
PDF/browser qualification is currently deferred, not passed.

## Current Interface

```sh
mintclaw eval list --root ./evals --json
mintclaw eval validate --root ./evals --suite general --json
mintclaw eval validate --root ./evals --tag multi-turn --json
mintclaw eval validate --root ./evals --case general.context.followup --json
mintclaw eval validate --root ./evals --preset ./evals/presets/local.template.json --json
```

These commands are offline. They validate every case in the corpus, select a nonempty set, check referenced fixture
digests, and optionally validate the preset manifest. They do not load MintClaw runtime credentials, contact models,
prove target isolation, or run a task. JSON explicitly reports `runtime_run: false`.
`run` executes bounded scripted or adaptive conversations against an admitted, prepared isolated profile. Setup, admission,
private report permissions, and limitations are in the [operator guide](../docs/operations/manual-agent-evals.md).
`grade` rejudges eligible private evidence or the pinned calibration corpus without target/tool work. Model user/judge
aliases use the isolated profile's native credential configuration. `run --repeat N` retains independent serial
trials (1-64 per case, at most 256 total), and `grade` rejudges all eligible source repetitions without target work.
Native PDF execution/artifact controls have domain-specific admission and unresolved qualification checks;
browser/owned-target execution and baseline comparison have their own evidence contracts. Stub tests are not
live scenario qualification.
General cases require a configured judge for their quality rubric, in addition to mandatory hard checks.

The corpus has 23 cases: six active/default General, pending isolation, tool recovery,
native stop, approval denial and final delivery,
six pending PDF and six pending browser. The fixed
[C2 campaign](../docs/operations/manual-agent-evals-core-c2-20261025.md) qualified
boundary and both order modes with 3/3 batches; it also retains one failed order
qualification and an inconclusive isolation batch caused by provider quota/unknown usage.
The pending [read recovery contract](../docs/reference/core-read-recovery-eval.md)
uses a separate read-only profile and does not widen the default active-tool profile.
Its [fixed C3a campaign](../docs/operations/manual-agent-evals-core-c3a-20261025.md)
retains a passing qualification and a batch of one pass/two judge provider errors;
all native hard checks passed, but it is not qualified or promoted.
The pending [native stop/reuse contract](../docs/reference/core-stop-eval.md)
uses the owned active-tool profile with an explicit one-turn pin. It joins a
genuine queued input, native abort, cancellation/history and final deliveries;
it is distinct from local driver stop and is not yet live-qualified.
The pending [approval denial contract](../docs/reference/core-approval-denied-eval.md)
uses a separate single-file protected-write profile, native prompt-bound denial,
terminal/history/log witnesses and an independent unchanged-target census.
Steering order records separate explicit
`one-at-a-time`/`all` profile variants; native same-sender inputs coalesce in FIFO
order in both modes. These live cases hold one read, not a parallel tool batch or
a deterministically paused finalization edge; native Go tests cover those races.
The [full baseline](../docs/operations/manual-agent-evals-baseline-20261024.md) remains nonpassing. Pending cases
remain inspectable offline, but no default live run includes them. Corpus presence or a valid manifest is not
domain qualification; the [browser guide](../docs/operations/manual-agent-evals-browser.md) retains its limits.
Correction and native ordinary answer/resume passed their fixed
[C1 qualification batches](../docs/operations/manual-agent-evals-core-c1-20261025.md). The latter requires delivered-prompt,
resolved-answer and ordered canonical history evidence; a judge's passing text alone cannot satisfy it. The
[execution ledger](../docs/architecture/agent-evals-core-execution.md#c1b-contract) describes bounds and frozen regrading.

## Active And Pending Sets

With no `--suite`, `--tag` or `--case` filters, `run` selects only catalog defaults (currently six general cases).
Explicit filters select their full matching set. Any selected pending case refuses the whole run before profile/model
work unless `--include-pending` is present; there is no silent partial execution. `--suite all` is not an opt-in.
`--include-pending` alone does not widen the default set. For example, with a separately prepared isolated profile:

```sh
MINTCLAW_HOME=/private/fresh-profile MINTCLAW_CONFIG=/private/fresh-profile/config.json \
  mintclaw eval run --start-target --preset /private/fresh-profile/reports/preset.json \
    --out /private/fresh-profile/reports
mintclaw eval run --suite pdf --include-pending --preset /private/preset.json --out /private/reports
```

The second command deliberately resumes pending tasks; it does not bypass capability, approval or isolation gates.
`list`/`validate` default to all cases and expose `status`, `default` and optional `pending_reason` per case, including
in `--json`. `grade`/`compare` still analyze frozen historical evidence without requiring pending execution permission.
Reports cover the selected trials, not invented passed/skipped records for excluded pending scenarios.
The expanded General defaults require a fresh owned active-tool profile in `one-at-a-time` mode with
`bounded_calls`, `target_call_budget`, metadata capture and a configured judge. See the
[steering guide](../docs/reference/core-steering-evals.md); templates show ceilings, not running configurations.
The original two case definitions/digests are unchanged. An older attach preset may explicitly select
`--case general.context.followup --case general.interaction.navigation` without enabling bounded C1 cases.
PDF/browser need their separate domain profiles; a bounded General preset does not admit them.

Optional root `catalog.json` is version 1 with exactly one entry per suite:

```json
{"version":1,"suites":[{"id":"general","status":"active","default":true}]}
```

For a pending suite use `"status":"pending","default":false,"reason":"<operator reason>"`.
Active suites may be non-default but have no reason; pending suites require a nonblank reason of at most 1,024 bytes.
The catalog is a bounded regular JSON file (at most 64 KiB); unknown/duplicate fields, suites, statuses, missing
suite policies and pending/default contradictions are rejected. It is operational selection metadata, not task
authority or part of a case digest. Changing a case still changes its identity. Private corpora without a catalog
retain their existing all-active/all-default behavior and arbitrary valid suite names.

Optional `cases` uses the same policy fields with full case IDs instead of suite IDs. Each entry must name a unique
existing case; unspecified cases inherit their suite. Overrides may narrow availability/default membership but cannot
activate a pending suite or enable default membership forbidden by the suite. For example:

```json
{"version":1,"suites":[{"id":"general","status":"active","default":true}],
 "cases":[{"id":"general.context.correction","status":"pending","default":false,"reason":"Awaiting qualification."}]}
```

This metadata does not change case digests or historical grading eligibility. The
[execution ledger](../docs/architecture/agent-evals-core-execution.md) tracks qualification separately from implementation.
`final_response_absent` checks only the last final reply; existing `response_absent` checks all retained assistant responses.

Preset paths resolve relative to the preset file, not the shell's working directory. The included preset is a
template, not a configured or safe running gateway. Runtime preflight must separately verify actual policy,
route/workspace, capability, and isolation. Do not point it at a production profile as a substitute for setup.

## Cases

Case IDs are unique, bounded lowercase identifiers prefixed with their suite, for example `general.context.followup`.
Suite, all requested tags, and requested IDs are intersected. Empty selection or an unmatched explicit ID is an error.
Use the same format under a private `--root` containing `cases/` and optional `fixtures/`.
Unknown/duplicate JSON members, missing required fields, unsafe paths, malformed limits, unsupported check/action
kinds, and unreferenced fact handles fail closed. Corpus case files may not be symlinks. Fixture access is anchored
by `os.Root`, checks regular files and byte bounds, and verifies SHA-256 before admission.

Facts are synthetic persona data, never production credentials. A `protected_answer` selects a declared fact handle
rather than putting a protected answer in its action text. Real privacy canaries will be generated privately by the
runner; corpus examples are not a privacy or protected-ingress qualification.

Keep run reports, private canaries, credential files, and delivered artifacts outside the corpus and git.
