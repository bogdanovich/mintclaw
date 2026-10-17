# Manual Agent Evals

## Current Scope

`mintclaw eval list` and `validate` remain offline. `run` attaches to an already prepared isolated loopback gateway
on Linux or macOS and executes bounded scripted or adaptive conversation/navigation cases. It does not create a profile,
bootstrap skills, deploy a binary, restart a gateway, grant broad approval, or retry uncertain task admission.

Native browser execution, preset provisioning and complete domain qualification remain later packets.
Offline `compare` reports all retained trial counts, case compatibility and observed environment changes; it does
not execute tasks, regrade evidence, or establish causal quality drift.
Missing capabilities/drivers/domain evidence produce explicit non-passing coverage. A text response never proves
that a PDF was filled, a browser action happened, or Telegram delivery worked. See the
[roadmap](../architecture/agent-evals-roadmap.md) for the remaining packets.

PDF preparation, protected answers, frozen write/approval binding, native settlement and bounded independent
artifact collection are described in the [PDF guide](manual-agent-evals-pdf.md). Required delivery uniqueness,
whole-runtime privacy and owned cleanup are still unproven; artifact predicates alone cannot qualify a PDF case.

The E2 integration adds tools-disabled user/judge roles to `run`, a judge-only `grade` command, versioned prompts,
strict typed actions/findings, and [six manually labelled calibration examples](../../evals/calibration/judge-v2.json).
Model context excludes runtime authority, hidden hard-check expectations, simulator
persona from judge input, and private fact values; fact handles alone are available to the user role. Protected
answers require PDF-specific native admission, not arbitrary case facts. Private values observed in raw events, even an
excluded streaming revision, refuse model projection. Known credentials from native configuration are checked before
wire/model input and report retention; unsafe output is withheld, not sanitized into a passing assessment.
JSON escaping is not treated as redaction. When private facts or a credential filter exist, escape-bearing content
must be one complete, bounded JSON value whose decoded tokens
can all be checked. Escaped opaque prose, trailing fragments, malformed input, and exhausted bounds are ineligible,
even when they look harmless. This conservative refusal avoids heuristic decoding of arbitrary prose; it is not
a guarantee against all possible encodings.

Role calls use the existing scoped HTTP providers with no tools/native search, at most 4,096 requested output
tokens, 16 KiB retained output, 64 KiB total prompt context, and a three-minute deadline. Only caller-mediated
`openai`, `anthropic`, and `gemini` providers are admitted; CLI agents and custom provider bodies are refused before
construction. Generation version identifies these settings; unspecified generation parameters use the native
provider defaults of the recorded code revision. The recorded model is the configured request model, not an
independently observed effective model. Returned usage is preserved as raw integers per call; missing usage and
unobservable provider-internal attempts remain unknown.

Calibration compares judge findings to labels only after the call; it does not expose labels to the judge. A
calibration pass means agreement on these examples, not a passing target task. Deterministic stub-provider tests
verify contracts, privacy boundaries, and reduction only. They are not live LLM calibration or domain qualification.
Judge prompt v2 requires positive evidence for pass and a visible contradiction or observed violation for fail;
missing artifact or runtime verification is inconclusive. The v2 corpus keeps all six v1 examples and labels
unchanged; the historical v1 corpus and live reports remain preserved. A v1 corpus cannot calibrate a v2 prompt.
The macOS portability job runs these deterministic harness tests, never the manual paid scenario suite.

Ordinary `request_user_input` questions support prompt-bound cancellation, not the protected PDF question's
Clarify/Back/Skip controls. The general interaction case therefore cancels the offered ordinary question. It checks
the fresh owner route, native waiting record and delivered prompt before sending the control, then requires the
same record's terminal cancellation before recognizing a `canceled` outcome. A cancellation reply is not an LLM
turn and has no invented trace scope. Retained cancellation identities and revisions are required for regrading;
conversation text alone is insufficient. This proves one question's terminal state, not whole-runtime quiescence,
PDF no-write behavior or owned-resource cleanup. Raw frames and inspected native files are checked for known
credentials before projection; observed leaks fail, while incomplete inspection remains non-passing uncertainty.

## Corpus Storage

Keep the runner, schema, deterministic harness tests and wholly synthetic shared regression cases in `mintclaw`.
Those cases should evolve with their feature contracts; this does not make live/paid eval runs part of CI.

Keep workspace-specific scenarios, internal workflows and non-public fixture documents in the existing private
workspace repository. A separate new evals repository is not needed unless ownership or access requirements diverge.
Private storage is not permission to run against production or real accounts: target and write-surface admission
still apply. Do not put API keys, OAuth stores, chat history, transcripts or run artifacts in either repository.

Use the same parser and commands for both locations by selecting an explicit `--root /absolute/corpus`.
Pin the corpus revision with the intended MintClaw version and retain case digests; avoid implicit latest-corpus
updates or a separate private runner fork. Credentials and result evidence stay in private runtime directories
outside Git. The [first live pilot](manual-agent-evals-pilot-20261011.md) and
[judge v2 calibration](manual-agent-evals-judge-v2-calibration-20261011.md) record actual results and limits.
The [general v2 pilot](manual-agent-evals-general-pilot-20261011.md) retains a passing context trial and the
interaction harness failure that identified the incorrect native outbox root.
The [general v3 pilot](manual-agent-evals-general-v3-pilot-20261011.md) records both required scripted general
cases passing on Linux after that reviewed fix, without claiming PDF/browser or full-goal qualification.

## Prepare The Target

### Owned Gateway Mode

`eval run --start-target` explicitly launches the current MintClaw executable's native `gateway` command and
stops that child before publishing a complete run report. Attachment to an already prepared running gateway
remains the default. The owned mode does not provision a profile or copy any production configuration or state.

Use a fresh private home with only `config.json`, optional native `.security.yml` and `.security` credential-file
directory, `workspace`, and `reports` at its root. The native repository's empty regular `0600`
`.security.yml.lock` file is permitted; it is not gateway/process ownership.
The workspace must be the physical `HOME/workspace` directory and may initially contain only reviewed
`AGENTS.md`, `SOUL.md`, `IDENTITY.md`, `USER.md`, `TOOLS.md`, and `skills`. Existing native state, sessions, memory,
PID files, ownership markers, logs, extra roots, and symlink entries are refused before launching. Existing
prepared-target tool/channel/network/approval admission still applies. Provider credentials should use native
file references in this private profile; the child does not inherit the parent's environment or secret stores.
Only the explicit home/config, PATH, fixed language and admitted warn/error/fatal logging environment are supplied.

Readiness must pass the authenticated native `/runtime` admission and identify the exact child PID. An existing
service's healthy response is not adopted. Startup is bounded to 30 seconds; cleanup has a separate 35-second
budget even when the scenario is interrupted. Only the owned process handle is signaled. A forced kill or
nonzero/uncertain exit is non-passing and never means a remote interaction was canceled. The ownership marker
is retained on abnormal exit. Never delete it to retry an uncertain operation; use a newly prepared profile.
In owned mode, SIGINT and SIGTERM cancel the eval command's run context while keeping the parent alive for its
independent cleanup budget. SIGKILL, a crash or host loss cannot execute that cleanup; retained process/domain
state must be treated as uncertain, not resumed or reported as canceled.

The private `target_lifecycle` receipt records admitted PID, stop/exit observations, exit code, forced exit and
log truncation. Captured child output is private and bounded to 1 MiB; native runtime logs remain separate
surfaces needing inspection. An exit-zero receipt does not grant PDF/browser idle, privacy, no-write, delivery,
or domain cleanup checks, and does not rewrite per-trial remote state. Reports remain outside git. Judge-only
regrading preserves source lifecycle uncertainty without starting a target; comparison distinguishes owned and
attached modes. Profile provisioning and post-settlement domain witnesses remain later E5/qualification work.

Use a new private profile, not the production profile or a copy of its sessions, memory, ledgers, or auth store.
Both runner and gateway must use the same explicit `MINTCLAW_HOME`. Keep configuration and profile files immutable
for the run. Admission is an operator-prepared profile check, not protection from a hostile concurrent filesystem
owner or sandbox certification.

Create these directories with mode `0700`, using canonical absolute paths (resolve symlinks first):

```text
PRIVATE_HOME/
  config.json
  .security.yml
  workspace/
  reports/
```

Use [conversation.template.json](../../evals/profiles/conversation.template.json) as the public configuration shape.
Replace its workspace/model placeholders, choose an unused loopback port, and configure a scoped HTTP provider
(`openai`, `anthropic`, or `gemini`). The alias and provider-native model ID are different fields. The included
provider is a shape example, not a model recommendation. Every configured gateway listener must bind only to
loopback; wildcard, unspecified, external, and mixed loopback/external listeners are refused before a runtime probe.
A loopback probe address alone does not prove a private bind. This eval restriction does not change `agent live`.
Use native credential configuration; do not put keys in
case manifests, presets, shell arguments, reports, or git. A native security overlay can provide file references:

```yaml
channel_list:
  mintclaw:
    settings:
      token: file://eval-channel-token
model_list:
  eval-target:
    api_keys:
      - file://eval-provider-key
```

The two secret files live inside the private home and have mode `0600`. Generate a new channel token and use an
operator-admitted provider credential. Do not copy a production OAuth refresh store. Existing MintClaw credential
resolution owns these references; evals do not implement another secret store.

Only `request_user_input` is allowed by the agent's deny-default tool policy; MCP is deny-default. External channels,
hooks, cron, exec, browser, document, nodes, remote coding, heartbeat, deploy, hot reload, trace capture, broad path
exceptions, subagents, dispatch/routing overrides, and custom provider bodies are refused in this first profile.
The active gateway log level must be `warn`, `error`, or `fatal`, not a debug override. Check inherited MintClaw
environment overrides in both processes. Adding a capability name to a preset does not enable that capability.

Enable exactly one MintClaw channel with `allow_from: ["mintclaw-user"]`, the native client's sender ID.
An empty list denies every sender and would otherwise leave the runner waiting for a response to discarded input.
The eval preflight rejects empty, foreign, wildcard, additional-sender and ambiguous enabled-channel policies
before contacting the target. This restriction applies only to prepared eval profiles, not ordinary `agent live`.

Start the isolated gateway with the same merged binary used by the runner:

```sh
MINTCLAW_HOME=/absolute/PRIVATE_HOME \
MINTCLAW_CONFIG=/absolute/PRIVATE_HOME/config.json mintclaw gateway
```

It must expose the authenticated `/runtime` observation. Admission verifies the PID-file service token, live PID,
public effective config fingerprint, physical home/config path, actual log level, and platform. This distinguishes
the prepared file from an unrelated or stale running gateway. It does not certify credential availability or hash
the executable's bytes. Actual build identity and unknown build fields are retained in reports. The channel token
authenticates WebSocket conversations separately; neither token is recorded in reports.

Create a non-secret preset outside git with the current schema:

```json
{
  "version": 1,
  "id": "private-conversation",
  "config_path": "/absolute/PRIVATE_HOME/config.json",
  "expected_agent": "main",
  "expected_workspace": "/absolute/PRIVATE_HOME/workspace",
  "isolation_root": "/absolute/PRIVATE_HOME",
  "capabilities": ["conversation", "interactions"]
}
```

Preset-relative paths resolve relative to the preset file. The workspace must use the same canonical spelling as
the native runtime route, not an alternate symlink alias. The existing private output parent must be inside the
profile and outside the workspace; it cannot be a workspace ancestor or descendant.

## Run And Interpret

```sh
mintclaw eval validate --root ./evals --preset /absolute/preset.json --json
MINTCLAW_HOME=/absolute/PRIVATE_HOME mintclaw eval run \
  --root ./evals --preset /absolute/preset.json \
  --suite general --out /absolute/PRIVATE_HOME/reports --json
```

Suite, all requested tags, and case IDs intersect. Each selected case gets a fresh session; follow-ups use only
new input in that same session. Runs are serial, with no reconnect/replay. Runtime identity is rechecked before
each task. The whole-suite timeout defaults to one hour, with a four-hour maximum; case/turn bounds remain active.

Use `--repeat 3` for three independent serial trials of every selected case. The default is one; admit at most
64 per case and 256 trials total before contacting the target. Case-major order, fresh session/trial IDs and fresh
domain preparation distinguish a repetition from retrying uncertain work. Each repetition gets a separate file
and one-based `repetition`; `run.json` records `repeat`. JSON retains every observed verdict, not just the best one.
Markdown and the CLI identify the attempt as `1/3`, `2/3`, etc. The suite deadline is shared, not multiplied.

Skipped or interrupted repetitions remain explicit coverage. Later passes cannot hide an earlier hard failure.
No reconnect/replay or automatic rerun after timeout/disconnect is added. Declared repetitions are not a guarantee
of native quiescence, successful cleanup, complete privacy, or model independence/statistical significance.

Every run gets a new `0700` directory with exclusive `0600` trial JSON, `summary.md`, and `run.json`. Trial files
contain private transcripts; they must not be published. The public CLI summary contains IDs, verdicts, static
codes, and the private report path, not task contents or credentials. JSON reports raw durations and observed
identity. User/judge call usage is summed from every available caller response, separately by role. Raw integers stay
in JSON; Markdown truncates compact counts, for example `10252` becomes `10.2k`. Missing returned usage and
provider-internal attempts remain explicitly unknown. Target usage remains unknown until all-call collection exists.

`pass`, `fail`, `error`, `inconclusive`, and `skipped` are distinct. Any required unknown or unavailable case prevents
a passing run. A hard failure remains a failure even if reporting also fails. A final response ends a turn, not
the entire case. Driver stop, lost route identity, changed runtime, limits, timeout, or disconnection cannot pass.

Only `run.json` with `complete: true` describes a completed report. A failed trial/summary write returns an error,
does not publish a completed run, and cannot return pass; retained partial evidence remains private. Completion of
the report is not task success. Non-passing runs exit nonzero and still emit their available summary.

Closing the owned WebSocket settles local transport workers, not remote cancellation. Reports retain
`remote_state: unknown` for attempted work; after a timeout/disconnect, inspect the isolated gateway rather than
automatically rerunning a possibly mutating operation. This first profile runs no fixture-mutating tools.

Stop only the gateway that the operator started for this profile. Evidence cleanup/retention is explicit operator
work; the runner does not delete profile sessions or infer that remote work stopped. Never remove a production
profile to clean up an eval run.

## Adaptive Users And Judging

Add `user_model` and `judge_model` aliases to the non-secret preset. They select enabled HTTP model configurations
in the same isolated native profile; their credentials are resolved by the native security overlay. A case with
`driver.mode: llm_user` also supplies a bounded persona, not tool or prompt bindings. The runner sends only new user
intent in the same target session. `finish` sends one last request and requires a final response; `stop` never passes.
An absent/unsupported user model skips the case without sending its task. Missing required judging cannot pass.

Each successfully parsed and privacy-checked user decision retains only its enum `action_kind` in the role-call
metadata, even if domain admission later refuses it or the driver chooses `stop`. It does not retain action text,
fact handles, raw model output or authority. Invalid/private outputs have no known kind. Older reports without
this optional field keep an unknown decision kind; historical decisions are never reconstructed or filled in.

Model roles are tools-disabled and separate from the target. The native runner binds navigation to the observed
prompt and refuses unbound grants. Facts/fixtures require domain-specific admission; merely adding those fields does
not make protected intake or artifacts available. Optional judge findings never hide transport or mandatory-check
failures; private judge output fails even an optional rubric and becomes ineligible for regrading.

## Regrade And Calibrate

```sh
MINTCLAW_HOME=/absolute/PRIVATE_HOME mintclaw eval grade /absolute/PRIVATE_HOME/reports/SOURCE_RUN \
  --root ./evals --preset /absolute/preset.json --case general.context.followup \
  --out /absolute/PRIVATE_HOME/reports --json
MINTCLAW_HOME=/absolute/PRIVATE_HOME mintclaw eval grade \
  --calibration ./evals/calibration/judge-v2.json --preset /absolute/preset.json \
  --out /absolute/PRIVATE_HOME/reports --suite-timeout 10m --json
```

`grade` reads complete, eligible private run/trial files and verifies case digests and route/evidence metadata
before calling the judge. It requires a matching preset ID and current cases; changed, incomplete, unsafe, or legacy
ineligible reports are refused. Operator-owned evidence is trusted input, not cryptographically signed evidence.
Mandatory checks are recomputed, not replaced with a previous judge verdict. A new private report is created;
source evidence is never overwritten. No gateway connection, PID file, task input, or tool execution is involved.
Source runtime identity is retained as historical evidence, not claimed as a newly observed running target.

For a repeated source, every repetition of every selected case must be present, eligible and consistent before any
judge call. Missing/duplicate trial or session IDs, mismatched ordinals, case hashes or evidence, and out-of-bound
repeat counts refuse the entire selected source. Regrade retains all original trial IDs and ordinals, aggregates
all returned judge usage, and never reruns target work. Older single-trial reports without repeat fields remain
accepted under their existing evidence eligibility rules. PDF evidence is not made regrade-eligible by this change.

Calibration uses `grade --calibration` without a source directory or corpus selection flags. It persists the labelled
agreement and private judge findings, and exposes only summary metrics through the CLI. Its limit is ten minutes,
or a shorter explicit suite limit; regrading defaults to one hour (maximum four hours). A canceled/failed judge call
is non-passing, with available call usage retained. `calibration.json` with `complete: true` marks report completion,
not target qualification. Stub tests and successful regrading are not real-model scenario evidence.

## Workspace File Observations

New runs retain optional `workspace_inputs` observations before the first trial and after all trials, before owned
gateway shutdown. Scope `workspace_files_v1` covers only `AGENTS.md`, `SOUL.md`, `USER.md`, and the recursive
workspace `skills` tree. Aggregate SHA-256 identities include relative names, absence, file contents and permission
bits, but reports contain no file names, paths, content, or individual-file hashes. Absolute profile placement and
mtimes do not affect these identities. Other workspace state is not read by this observer.

Reads are confined with `os.Root`, capped at 64 KiB per file, 8 MiB total content and 4096 skill-tree entries,
including the root. Symlinks, special files, unreadable/oversized sources and known credentials or private case
facts withhold the whole snapshot as `unavailable`, without partial hashes or value-bearing errors. An unavailable
diagnostic does not change a task verdict or claim privacy coverage. Runtime admission and hard checks still apply.
Cancellation is checked between bounded file reads; a canceled observation withholds all hashes.

These are two local filesystem samples, not an atomic snapshot or proof of files actually used by the target.
They do not cover memory, user/system skills, effective catalog compatibility/selection, cached prompts, dynamic
contributors or changes between samples. Prepared inputs remain operator-owned; hostile concurrent filesystem
mutation is not certified. Judge-only regrading validates and retains the source observations, never replaces them
with today's workspace files, and does not expose these observations to the judge. Old reports retain unknown
workspace inputs rather than gaining reconstructed hashes.

## Compare Retained Runs

```sh
mintclaw eval compare /absolute/private/baseline-report /absolute/private/current-report --json
```

Comparison needs neither a live profile nor credentials, a corpus checkout, a gateway, or a model. Both report
directories must be private; `run.json` and every referenced trial must be bounded regular `0600` files.
The reader admits complete run/grade reports, verifies trial identities, hashes, files, ordinals, unique sessions,
all repetitions, summary consistency, hard-failure dominance and aggregate verdict/coverage. It never modifies
reports, drops nonpassing trials, chooses the best repetition, or regrades the saved findings. This is consistency
checking of operator-owned evidence, not a signature or independent proof that the reported work happened.

Each case reports baseline/current trial counts and all five verdict counts. Added, removed or changed case
digests make the case cohort incompatible; the CLI still emits that comparison and exits nonzero. Different repeat
counts are shown as a sampling change, not paired retries or an automatic improvement/regression label.

Recorded preset, public configuration, runtime revision/platform, grading mode and model-role/prompt changes are
separate from case compatibility. Dynamic PID/instance tokens and per-call conversation prompt hashes are not
cohort identities. No raw transcript, prompt, path, model configuration or artifact contents enter comparison output.
When both runs have observed initial workspace samples, comparison separately lists instruction-file and skill-file
changes. Missing/unavailable samples or different before/after samples add explicit per-source unknowns. The
`instructions_and_skills` unknown still covers the effective runtime inputs excluded above, and complete target-call
model accounting remains unknown. `quality_attribution: not_established` is reported even when available metadata
matches. Complete preset/effective-input fingerprints and live domain qualification remain later E5/exit work, not
claims made by an offline comparison. Compare complete saved selections; use suite/tag/case selection when producing runs.
