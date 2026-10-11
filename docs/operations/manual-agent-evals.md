# Manual Agent Evals

## Current Scope

`mintclaw eval list` and `validate` remain offline. `run` attaches to an already prepared isolated loopback gateway
on Linux or macOS and executes bounded scripted or adaptive conversation/navigation cases. It does not create a profile,
bootstrap skills, deploy a binary, restart a gateway, grant broad approval, or retry uncertain task admission.

Native browser execution, baseline comparison and complete domain qualification remain later packets.
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

## Prepare The Target

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
