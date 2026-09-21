# Model And Reasoning Unification Roadmap

Status: P0 foundation in progress; P1-P6 admitted but not yet implemented.

## Objective

MintClaw should expose one truthful model-and-reasoning contract across the
local coding agent, the always-running gateway, direct agent calls, resumed
sessions, scheduled work, and future remote coding-task handoff.

The user-facing word `high` is not itself a capability. It is meaningful only
for a concrete provider/model route whose adapter knows how to encode it. A
reasoning model may have no controllable effort surface; another may require
reasoning; `off` may mean an explicit wire field, an omitted field, a sibling
model, or an invalid request. The architecture must preserve those facts
instead of presenting one global list.

## Evidence reviewed

The audit used the latest available source revisions on 2026-09-20:

| Project | Revision | Useful design | Limitation to avoid |
| --- | --- | --- | --- |
| [OpenAI Codex](https://github.com/openai/codex) | `a86631502d49274cb47208925c7d3dcece032029` | Model presets own `default_reasoning_effort` and ordered `supported_reasoning_efforts`; the TUI selects model and effort together and persists both. | `max` and especially `ultra` include Codex-specific behavior. `ultra` also changes multi-agent orchestration, so it is not a portable provider effort. |
| [Pi](https://github.com/earendil-works/pi) | `890f920884f6d21fc7617d236ef9e1cc5d7a0ef8` | A small canonical ladder, per-model maps, supported-level discovery, deterministic clamping, and durable model/thinking change records. | Its inferred base ladder can claim values that a custom endpoint has not explicitly declared. |
| [oh-my-pi](https://github.com/can1357/oh-my-pi) | `d716bcf60ab0a2e7ece1fdf382c0d143fef1f307` | Rich `ThinkingConfig`: ordered efforts, control mode, default, wire map, budgets, sibling-model routing, mandatory reasoning, and explicit off suppression. Capability metadata is baked before request time. | The full generated catalog and compatibility engine are too large to transplant into MintClaw's first slice. |
| [OpenCode](https://github.com/anomalyco/opencode) | `c10134729dd2ce00beb18604ec91f10319f59a78` | Converts external model capability metadata into per-model variants; invalid saved variants disappear when the catalog changes. | Generic variants blur reasoning semantics with arbitrary provider options, and some provider logic remains heuristic. |
| [OpenClaw](https://github.com/openclaw/openclaw) | `ab3a3d14f0a70ea561dfdbccfec788ab4dd054c0` | Provider plugins return a `ThinkingProfile`; gateway pickers consume that exact profile; explicit invalid selections are rejected; stored stale values are rank-remapped. | Display-of-reasoning and reasoning-effort controls share command vocabulary and should remain separate in MintClaw. |
| [Hermes Agent](https://github.com/NousResearch/hermes-agent) | `845fee6cf4b22f9456218bf32baaa85934b2c005` | Session, per-model, global, CLI, gateway, TUI, cron, and fallback paths share a resolver; status can distinguish requested from clamped wire effort. | A wide internal ladder plus downstream clamping can make two visible choices produce the same provider request. |

Relevant source entry points:

- Codex: `codex-rs/protocol/src/openai_models.rs`,
  `codex-rs/tui/src/chatwidget/model_popups.rs`, and
  `codex-rs/core/src/session/turn_context.rs`.
- Pi: `packages/ai/src/types.ts`, `packages/ai/src/models.ts`,
  `packages/coding-agent/src/core/agent-session.ts`, and
  `packages/coding-agent/src/core/session-manager.ts`.
- oh-my-pi: `packages/catalog/src/types.ts`,
  `packages/catalog/src/model-thinking.ts`, and
  `docs/provider-compat-reference.md`.
- OpenCode: `packages/opencode/src/provider/transform.ts` and
  `packages/opencode/src/cli/cmd/run/variant.shared.ts`.
- OpenClaw: `src/plugins/provider-thinking.types.ts`,
  `src/auto-reply/thinking.ts`, `src/gateway/sessions-patch.ts`, and
  `docs/tools/thinking.md`.
- Hermes Agent: `agent/reasoning_effort.py`,
  `gateway/run_config_loaders.py`, `gateway/slash_commands_model.py`, and
  `tui_gateway/model_switch.py`.

## Current MintClaw gap

Before P0, MintClaw has three partially independent concepts:

1. `model_list[].thinking_level` is a static default with no declared
   supported-level set.
2. `pkg/agent` recognizes a global list and passes `thinking_level` only when
   a provider advertises the coarse boolean `Capabilities.Thinking`.
3. Provider adapters independently map, collapse, ignore, or reject values.

Examples of the resulting ambiguity:

- OpenAI OAuth maps `adaptive` to `medium` and previously mapped `max` to
  `xhigh`, despite the current Codex catalog exposing exact values.
- DeepSeek maps `low`, `medium`, and `high` to the same wire effort.
- Gemini model families differ on whether reasoning can be disabled and which
  named levels exist.
- Anthropic `adaptive` is model-generation-specific while token-budget levels
  use a different transport.
- Gateway session overrides persist only the model alias. Coding-thread
  metadata can persist a model/provider pair but previously had no shared
  capability source for reasoning.

Consequently a global picker can offer a value that is ignored, collapsed, or
rejected, and coding and gateway can disagree about the same configured model.

## Admitted architecture

### 1. Canonical vocabulary is not the capability list

`pkg/reasoning` owns normalized identifiers and presentation labels:

`off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, and `adaptive`.

This is a parsing vocabulary only. A UI must never render all values merely
because the core recognizes them. `ultra` is withheld: Codex uses it for both
wire effort and proactive multi-agent behavior, so exposing it before MintClaw
implements the associated orchestration policy would be false parity.

### 2. Every concrete route has a profile

A reasoning profile contains:

- ordered supported options with stable IDs, labels, and descriptions;
- an optional default;
- whether reasoning is mandatory;
- provenance such as explicit config, provider catalog, authenticated
  discovery, or conservative intersection.

An empty profile means "no verified control surface", not "this model does not
reason". Unknown capability data must fail closed in pickers.

Provider adapters continue to own wire concerns: effort remapping, token
budgets, explicit-off fields, model-ID routing, and request incompatibilities.
These do not belong in TUI or gateway packages.

### 3. Alias capabilities are conservative

MintClaw model aliases may contain several configured provider routes. While
selection remains alias-based, the advertised profile is the intersection of
all eligible routes. This guarantees that automatic fallback cannot turn an
accepted setting into an invalid request. A later route-aware picker may show
provider-specific supersets after the provider is explicitly pinned.

### 4. Selection is one atomic object

The shared command/API shape is conceptually:

```json
{
  "model": "gpt-5.6-sol",
  "provider": "openai",
  "reasoning": {
    "mode": "explicit",
    "effort": "xhigh"
  }
}
```

`reasoning.mode` is either `explicit` or `inherit`. Absence in old data is
interpreted as `inherit` during migration. The mutation is validated in full,
then persisted once, then installed between turns. Provider construction or
validation failure leaves the prior selection untouched.

P0 uses an empty effort internally for `inherit`; P1 introduces the explicit
tagged representation before gateway adoption.

### 5. Resolution precedence

The effective setting is resolved in this order:

1. one-turn/task-scoped explicit override;
2. coding-thread or gateway-session explicit override;
3. per-model configured default;
4. agent-wide configured default;
5. provider/model catalog default;
6. provider-owned default with the wire control omitted.

The resolver returns structured provenance:

```text
requested: xhigh
effective: high
source: session_override
profile_source: authenticated_catalog
wire_behavior: mapped
```

Explicit unsupported input is rejected without mutation. Implicit defaults
may fall back to a provider default, but status must say so. During one-time
migration only, a stale persisted effort may be rank-remapped downward with a
diagnostic and immediately rewritten; normal commands never silently clamp.

### 6. Reasoning visibility is independent

Whether hidden provider reasoning is displayed is a presentation/privacy
setting. It must not share storage or semantics with reasoning effort. A future
`/reasoning show|hide` command, if added, is a distinct display operation.

## Configuration direction

P0 admits an explicit custom-route declaration:

```json
{
  "model_name": "private-reasoner",
  "provider": "openai",
  "model": "private/reasoner-v2",
  "reasoning": {
    "supported_efforts": ["off", "low", "high"],
    "default_effort": "low",
    "required": false
  },
  "enabled": true
}
```

`thinking_level` remains the default override during P0. Because there are no
third-party deployments to preserve, P3 performs a one-time production config
migration to `reasoning.default_effort` and removes the legacy field rather
than maintaining two permanent configuration paths.

Dynamic or first-party catalogs override heuristics but never an explicit
administrator profile unless a validation mode is requested. Catalog cache
records need a schema version, fetch timestamp, source revision/ETag, and
bounded TTL. Last-known-good catalog data may be used for display; a value no
longer accepted by live validation cannot be newly selected.

## Delivery roadmap

### P0 — Shared profile foundation and coding selection

Scope:

- add provider-neutral effort/profile types;
- add optional per-model profile configuration and validation;
- extend the bundled Codex catalog with default and supported efforts;
- expose model-specific profiles through the coding frontend;
- make `/model` select model and reasoning atomically;
- persist reasoning with coding-thread metadata and restore it on resume;
- validate again in the runtime, never trusting frontend filtering;
- pin the exact effort on the next and subsequent turns.

Acceptance criteria:

- `gpt-5.6-sol` over OpenAI OAuth shows its verified catalog efforts, not a
  global MintClaw list;
- an unknown route shows no fabricated reasoning choices;
- explicit custom profiles are validated for duplicates, order, default
  membership, and mandatory/off contradictions;
- `/model NAME EFFORT` rejects unsupported pairs without changing metadata or
  the active provider;
- successful selection survives process restart and resume;
- aliases with heterogeneous routes expose only their effort intersection;
- provider initialization failure is atomic;
- focused tests and changed-file lint pass.

### P1 — Tagged selection and one resolver

Scope:

- replace empty-string inheritance with `ReasoningSelection{Mode, Effort}`;
- introduce `Resolve(modelRoute, overrideStack) -> ResolvedReasoning`;
- move coding status, direct-turn pins, and agent effective state to the
  resolver;
- record requested/effective/source/profile-source distinctly;
- define cache invalidation when model or reasoning changes;
- make fallback candidate admission profile-aware.

Acceptance criteria:

- explicit `off` and unset/inherit cannot be confused;
- all direct-turn and coding paths use the shared resolver;
- a fallback candidate incompatible with an explicit effort is skipped with a
  typed reason, not attempted optimistically;
- status and diagnostic events reveal mapping without exposing hidden
  reasoning content.

### P2 — Provider profile ownership

Scope:

- add an optional provider capability interface for model reasoning profiles;
- populate exact profiles for OpenAI OAuth, OpenAI Responses/API-key routes,
  Anthropic generations, Gemini families, DeepSeek, and supported
  OpenAI-compatible providers;
- add wire mapping/budget/off/required metadata inside provider packages;
- consume authenticated discovery where available;
- prevent two visible efforts from producing identical requests unless the UI
  labels the mapping explicitly.

Acceptance criteria:

- every built-in provider with `Capabilities.Thinking` has contract tests for
  supported/default/off behavior;
- model-specific exceptions live beside the adapter or catalog, not in TUI,
  gateway, or agent command code;
- unknown custom endpoints remain config-driven and conservative;
- catalog refresh cannot mutate an active turn.

### P3 — Gateway/session parity and migration

Scope:

- replace `SessionModelOverride` with one atomic model/reasoning selection;
- add `/reasoning`, `/reasoning reset`, and `/model NAME --reasoning EFFORT`;
- render channel-native pickers from the same profile returned to coding;
- use session scope by default and an explicit global/config flag for durable
  administrator changes;
- migrate deployed config/state once and remove `thinking_level` plus the old
  model-only session override schema.

Acceptance criteria:

- Telegram and other gateway surfaces expose the same options and validation
  as `mintclaw code` for the same route;
- a rejected pair changes neither model nor effort;
- restart preserves session selection, while `/new` clears session scope;
- global changes are explicit and use atomic config writes;
- deployment tooling moves/migrates the known single-user state and verifies
  that no legacy fields remain.

### P4 — Public control plane and automation

Scope:

- expose profile and resolved-state fields in status/model APIs;
- add model/reasoning options to `code exec`, worker handoff, cron, subagents,
  and remote coding tasks;
- allow the live gateway agent to submit an admitted coding task with an exact
  model/reasoning pair;
- propagate the contract through companion-node execution without granting
  new machine authority.

Acceptance criteria:

- one schema is used by TUI, gateway, headless JSONL, and task handoff;
- remote nodes validate against their local effective catalog before accepting
  work;
- task receipts contain requested and effective selections;
- resume never substitutes the gateway's current global model for the model
  stored with a coding thread.

### P5 — Catalog lifecycle and operational safety

Scope:

- version/cache authenticated catalogs;
- add stale, missing, and changed-capability diagnostics;
- define deterministic migration for removed efforts;
- add metrics for explicit rejection, inherited fallback, catalog staleness,
  and provider-side invalid-parameter errors;
- add rollout and rollback commands to deployed operations.

Acceptance criteria:

- a stale catalog is visibly marked and never expands authority;
- removed explicit efforts stop before a turn and offer valid alternatives;
- one rollback restores the previous binary and pre-migration state snapshot;
- traces contain no credentials, prompts, or reasoning content.

### P6 — Optional advanced controls

Scope:

- provider-specific advanced descriptions and cost/latency hints;
- separate token-budget entry for providers that expose a continuous budget;
- route-aware alias expansion when the user explicitly pins a provider;
- evaluate a MintClaw-native `ultra` orchestration policy only after subagent
  semantics, budgets, and user visibility are specified.

Acceptance criteria:

- advanced controls degrade to the same base profile contract;
- budget inputs are bounded and validated by the provider profile;
- `ultra`, if admitted, has an architecture decision describing orchestration,
  billing, fallback, cancellation, and resume behavior.

## Test strategy

The implementation matrix must cover:

- profile validation: duplicates, invalid values, order, required/off, and
  unsupported defaults;
- source precedence: explicit config, authenticated catalog, bundled catalog,
  conservative legacy value, and unknown route;
- exact provider/model families and alias intersections;
- explicit rejection versus migration-only remapping;
- model+reasoning atomicity when provider creation or persistence fails;
- restart/resume for coding and gateway sessions;
- fallback candidates with narrower, wider, and absent profiles;
- provider request snapshots for every supported visible option;
- TUI keyboard flow, typed slash flow, channel pickers, and headless JSONL;
- race tests for between-turn mutation and concurrent gateway sessions;
- redaction tests for diagnostics and persisted state.

Provider request tests are the final oracle: every picker option must cause the
documented distinct wire behavior or be clearly labelled as a mapping.

## Rollout order

1. Merge P0 with no gateway behavior change.
2. Deploy and exercise `/model` in a disposable coding thread, including
   restart/resume and an intentionally invalid effort.
3. Land P1 and P2 behind internal profile diagnostics until provider matrices
   are green.
4. Snapshot the deployed config/state, stop the gateway, run the P3 one-time
   migration, deploy, and verify no legacy keys remain.
5. Enable gateway commands first in a private test conversation, then normal
   conversations.
6. Add automation and remote-node propagation only after coding/gateway parity
   has production evidence.

## Completion criteria for the overall roadmap

The program is complete only when:

- all execution surfaces resolve reasoning through one contract;
- every displayed option is backed by a concrete model profile and provider
  request test;
- explicit unsupported choices fail before mutation or provider invocation;
- model and reasoning persist and resume atomically;
- coding, gateway, automation, and remote handoff report identical effective
  state for the same selection;
- the deployed configuration and state contain no legacy `thinking_level` or
  model-only session override representation;
- catalog failures and capability changes are observable, bounded, and
  rollback-tested;
- documentation, configuration examples, and operational runbooks describe
  the final schema and precedence.

## Explicit non-goals

- importing another project's entire model catalog generator;
- treating reasoning effort as permission or tool authority;
- exposing hidden chain-of-thought content;
- silently accepting an unsupported explicit value;
- making all providers share identical effort names;
- presenting `ultra` as a synonym for `max`.
