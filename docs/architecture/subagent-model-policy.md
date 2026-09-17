# Subagent Model Policy

MintClaw distinguishes three separate concerns for child-agent model selection:

1. The target agent's normal model configuration.
2. An optional subagent-specific model policy.
3. An optional propagated parent session model override.

This document defines the intended precedence and behavior for `spawn`,
`subagent`, and `delegate` child runs.

## Goals

- Keep normal agent specialization stable by default.
- Support a session-scoped emergency model switch such as `/model gemini-flash-lite`.
- Allow specific child agents to opt out of inherited overrides.
- Avoid coupling diagnostics or turn setup to hidden runtime magic.

## Config Surface

Two levels can configure child-run behavior:

- `agents.defaults.subagents`
- `agents.list[].subagents`

Supported fields:

- `allow_agents`
- `model`
- `session_model_override_mode`

`session_model_override_mode` accepts:

- `ignore`
- `inherit`
- `fallback_only`

## Resolution Order

Child-run model selection follows this order:

1. Explicit per-call model override.
   The optional `model` argument on `subagent`, `spawn`, and `delegate` accepts
   an exact enabled `model_list[].model_name`.
2. Target agent `subagents.model`
3. Global `agents.defaults.subagents.model`
4. Target agent normal `model`

Session model override propagation is then applied according to
`session_model_override_mode`, resolved with:

1. `agents.list[].subagents.session_model_override_mode`
2. `agents.defaults.subagents.session_model_override_mode`
3. implicit default: `ignore`

An explicit per-call model always wins over both the configured child model
and an inherited session override. Automatic light-model routing is disabled
for that child run so it cannot silently replace the requested model. Normal
provider fallback candidates remain available if the requested model fails.
The exact-model binding also ignores the parent route's sticky automatic
fallback and never updates it; the route key remains attached only for session
ownership and delivery. The same isolation is restored after a durable child
resumes from human input or crosses a configuration reload.

## Autonomous Per-Task Selection

The child-tool schemas expose the enabled, non-virtual model names as the
allowed values for `model`. This lets the parent model choose a stronger,
faster, or cheaper configured model for a bounded task when that materially
helps, or honor an explicit user request such as using `gpt-5.6-sol` for one
PDF operation.

This is delegation, not mutation of the parent session:

- `subagent` waits for the selected-model child and returns its result inline.
- `spawn` runs the selected-model child in the background.
- `delegate` combines a named target agent's workspace and tools with the
  explicit per-call model.
- The parent continues on its existing model without a restore call.
- A durable child that pauses for human input records its active model and
  resumes on that model when it is still available.

## Mode Semantics

### `ignore`

The parent session override does not affect the child run.

- Child primary model remains the resolved child base model.
- Child fallback chain remains the resolved child base fallbacks.

Use this for specialized agents that should keep their configured model policy
even when the parent conversation was manually switched to another model.

### `inherit`

The parent session override becomes the child run's effective primary model.

- Child primary model becomes the propagated override model.
- Child fallback chain remains the resolved child base fallbacks.

Use this for "panic switch" behavior where an operator changes the current
session model and expects all delegated work in that session tree to follow it.

### `fallback_only`

The parent session override is inserted at the front of the child fallback
chain without replacing the child's configured primary model.

- Child primary model remains the resolved child base model.
- Parent override becomes the first fallback, unless it duplicates the primary
  or an existing fallback.

Use this when child agents should preserve their specialization, but should
still prefer the parent session override when their own primary model fails.

## Propagation Across Nested Child Runs

When a parent session override is present, child runs carry that override
metadata forward in their effective model binding even if the mode is
`fallback_only`.

This ensures nested child runs can apply their own `session_model_override_mode`
consistently without requiring the override to be re-persisted in per-child
ephemeral sessions.

## Recommended Defaults

Backward-compatible default:

- `ignore`

Operationally useful default for mixed-cost deployments:

- `fallback_only`

Suggested specializations:

- `coding`: `ignore` or `fallback_only`
- `media`: `inherit` or `fallback_only`
- `reviewer`: `ignore`

## Non-Goals

This policy intentionally does not:

- rebuild the full parent runtime prompt for children
- implicitly rewrite every child tool or async runtime
- force all child agents to inherit a parent override unless configured
- change the parent conversation's session model override

The design is intentionally declarative: target child agents declare how much
parent session model state they want to inherit.
