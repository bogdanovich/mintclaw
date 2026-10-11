# Manual Agent Evals

## Current Scope

`mintclaw eval list` and `validate` remain offline. `run` attaches to an already prepared isolated loopback gateway
on Linux or macOS and executes bounded scripted or adaptive conversation/navigation cases. It does not create a profile,
bootstrap skills, deploy a binary, restart a gateway, grant approval, or retry uncertain task admission.

This packet does not yet implement domain oracles, protected fact answers, artifact downloads, repeats, or comparison.
Missing capabilities/drivers/domain evidence produce explicit non-passing coverage. A text response never proves
that a PDF was filled, a browser action happened, or Telegram delivery worked. See the
[roadmap](../architecture/agent-evals-roadmap.md) for the remaining packets.

The E2 integration adds tools-disabled user/judge roles to `run`, a judge-only `grade` command, versioned prompts,
strict typed actions/findings, and [six manually labelled calibration examples](../../evals/calibration/judge-v1.json).
Model context excludes runtime authority, hidden hard-check expectations, simulator
persona from judge input, and private fact values; fact handles alone are available to the user role. Admission of
protected wire answers and domain evidence remains a later packet. Private values observed in raw events, even an
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
The macOS portability job runs these deterministic harness tests, never the manual paid scenario suite.

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
provider is a shape example, not a model recommendation. Use native credential configuration; do not put keys in
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
prompt and refuses grants. Facts/fixtures still require later domain admission; merely adding those fields does
not make protected intake or artifacts available. Optional judge findings never hide transport or mandatory-check
failures; private judge output fails even an optional rubric and becomes ineligible for regrading.

## Regrade And Calibrate

```sh
MINTCLAW_HOME=/absolute/PRIVATE_HOME mintclaw eval grade /absolute/PRIVATE_HOME/reports/SOURCE_RUN \
  --root ./evals --preset /absolute/preset.json --case general.context.followup \
  --out /absolute/PRIVATE_HOME/reports --json
MINTCLAW_HOME=/absolute/PRIVATE_HOME mintclaw eval grade \
  --calibration ./evals/calibration/judge-v1.json --preset /absolute/preset.json \
  --out /absolute/PRIVATE_HOME/reports --suite-timeout 10m --json
```

`grade` reads complete, eligible private run/trial files and verifies case digests and route/evidence metadata
before calling the judge. It requires a matching preset ID and current cases; changed, incomplete, unsafe, or legacy
ineligible reports are refused. Operator-owned evidence is trusted input, not cryptographically signed evidence.
Mandatory checks are recomputed, not replaced with a previous judge verdict. A new private report is created;
source evidence is never overwritten. No gateway connection, PID file, task input, or tool execution is involved.
Source runtime identity is retained as historical evidence, not claimed as a newly observed running target.

Calibration uses `grade --calibration` without a source directory or corpus selection flags. It persists the labelled
agreement and private judge findings, and exposes only summary metrics through the CLI. Its limit is ten minutes,
or a shorter explicit suite limit; regrading defaults to one hour (maximum four hours). A canceled/failed judge call
is non-passing, with available call usage retained. `calibration.json` with `complete: true` marks report completion,
not target qualification. Stub tests and successful regrading are not real-model scenario evidence.
