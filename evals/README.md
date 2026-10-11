# Manual Agent Evals

This versioned corpus is for occasional real-agent evaluation, not required live CI.
The [design](../docs/architecture/agent-evals-design.md),
[roadmap](../docs/architecture/agent-evals-roadmap.md), and
[goal](../docs/architecture/agent-evals-goal.md) define the program.

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
aliases use the isolated profile's native credential configuration. Domain suites, protected fact answers, repeats,
and baseline comparison remain later implementation packets; stub tests are not live scenario qualification.
The two general cases now require a configured judge for their quality rubric, in addition to mandatory hard checks.

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
