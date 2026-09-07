# MintClaw Roadmap

MintClaw is an independent, deployment-focused agent runtime. The roadmap
tracks work that matters for reliable personal automation across supported
platforms.

## 1. Runtime Reliability

- Keep async task delivery deterministic across restarts.
- Continue consolidating tool delivery through the shared delivery coordinator.
- Reduce duplicate final replies after media/file/message tools.
- Keep task-registry records bounded, queryable, and useful for debugging.
- Preserve topic/session routing across Telegram, cron, spawn, and delegate
  paths.

## 2. Context Management

- Keep Seahorse compaction bounded and observable.
- Prefer asynchronous compaction where it is safe.
- Fail closed when context still exceeds provider limits after compaction.
- Keep prompt assembly predictable by reserving budget for tools, non-history
  prompt sections, and required routing context.
- Add focused regression tests for long-session and post-compaction behavior.

## 3. Tooling And Workflow State

- Keep `task_status` as the primary user-facing progress/status command.
- Retire duplicated status surfaces when they no longer add value.
- Improve deterministic test coverage for tool loops, spawned work, and async
  completions.
- Extract a shared path-scope validation package only if non-`exec` tools need
  the same workspace/symlink/allowed-path rules currently enforced by
  `shellguard`.

## 4. Provider And MCP Behavior

- Keep OpenAI OAuth/Codex paths reliable and observable.
- Preserve streamed output and streamed tool-call behavior in provider adapters.
- Keep MCP transport failures explicit and fail-fast.
- Maintain deferred MCP/tool discovery behavior so large tool inventories do not
  pollute ordinary prompts.

## 5. Channels And Media

- Re-architect channel lifecycle so startup, retry, reload, inbound exposure,
  and readiness are supervised through explicit runtime state instead of being
  tightly coupled inside `channels.Manager`.
- Keep shared webhook/HTTP registration tied to active channel runtimes rather
  than configured channel presence.
- Make startup retry generation-aware and cancelable across reload/shutdown.
- Keep gateway readiness aligned with actual channel delivery capability.
- Keep Telegram forum-topic routing stable.
- Preserve media-group handling and forwardable media captions.
- Keep generated images and files deliverable without duplicate completion
  messages.
- Keep channel feedback throttling controlled by real edit intervals.
- Move reply / adjacent-followup / media-only interpretation toward an explicit
  inbound relation model so prompt assembly does not have to guess message
  boundaries from raw history.

## 6. Automation And Agent Workflows

- Keep core workflow primitives deployment-agnostic.
- Support durable queued work without assuming a specific domain or workspace.
- Make spawned/delegated work observable through shared task status surfaces.
- Keep webhook, cron, and manual trigger paths consistent.
- Let deployments layer domain-specific agents and policies outside the core
  runtime.

## 7. Project Coherence

- Keep one canonical MintClaw identity across binaries, modules, packages,
  configuration, release artifacts, and documentation.
- Prefer direct migrations over permanent compatibility aliases.
- Remove superseded behavior once its replacement is validated and deployed.
- Keep platform packaging and runtime conventions explicit and testable.

## 8. Model Selection Architecture

- Keep `/model` as the conversation-scoped model selector and `/switch` as the
  explicit workspace-wide operator path until `/switch` is deprecated.
- Replace ad hoc override-agent cloning with a clearer effective-model
  resolution path.
- Resolve model-selection state once per routed turn, then pass that binding
  through command/runtime and execution code instead of repeatedly re-reading
  per-session override state.
- Keep invalid session overrides self-healing and bounded to the routed
  conversation key.
- Preserve current provider/model selection semantics while reducing the amount
  of agent-instance mutation/cloning required to execute a session override.
- After the binding layer is stable, consider moving from "derived
  AgentInstance" materialization toward per-turn provider/model resolution so
  session overrides stop carrying copied tool/router/provider bookkeeping.

## 9. Reliable Document And PDF Workflows

Build PDF handling as a general document workflow rather than a collection of
form-specific prompts. Individual government, tax, legal, and business forms
may be retained as regression fixtures, but must not define the core
architecture.

### Delivery sequence

1. Preserve every inbound document as a first-class attachment with a stable
   local path, original filename, MIME type, byte size, and SHA-256 digest.
   Supply the original PDF directly to providers that support native PDF input
   instead of reducing it to an untyped path in prompt text.
2. Add deterministic inspection and rendering primitives that classify text
   PDFs, scans, AcroForms, hybrid or dynamic XFA, encryption, signatures, and
   unsupported features before the agent chooses an editing strategy.
3. Provide pinned, isolated document workers for text and table extraction,
   OCR, page rendering, AcroForm field discovery and filling, flattening,
   merging, splitting, and structural verification. Production tasks must not
   install or mutate global dependencies at runtime.
4. Maintain a durable field ledger for long document tasks. Each value should
   retain its field identity, source, confidence, confirmation state, and blank
   reason so compaction, retries, and provider fallback do not make the agent
   re-ask known facts or confuse document roles.
5. Treat XFA as an explicit capability boundary. Use a verified XFA-capable
   renderer or editor when one is configured; otherwise stop with a structured
   unsupported-capability result. Editing embedded datasets alone must never be
   reported as a completed visible form. A browser-based schema or data-entry
   view may assist the workflow, but it is not a correctness backend unless the
   exported PDF also passes the authoritative renderer and verification gates.
6. Route high-risk interpretation, cross-field reconciliation, and the final
   document audit through a deliberative high-capability model. Lightweight
   models may perform routine extraction, but a fallback to one must not
   silently authorize completion when the required audit model is unavailable.
7. Keep orchestration in a reusable PDF skill: acquire, inspect, select a
   supported strategy, collect only missing facts, edit, verify structurally,
   render, verify visually, and deliver one clear final result. Keep binary
   transformation and validation in native tools or isolated workers rather
   than relying on prompt compliance.
8. Protect sensitive documents with restrictive temporary storage, bounded
   retention, and redaction of field values and command arguments from normal
   logs and diagnostic traces.

### Completion criteria

This roadmap item is complete only when:

- a fixture suite covers text PDFs, scanned documents, AcroForms, hybrid and
  dynamic XFA, encrypted or signed inputs, provider-native PDF ingestion, and a
  long fact-collection conversation that crosses compaction;
- no document can be described or delivered as filled or ready until it exists,
  parses successfully, retains the expected identity and page count, round-trips
  expected field values, and renders those values visibly on every affected
  page;
- unsupported XFA and other unsupported features fail closed with an actionable
  status instead of producing a plausible but blank artifact;
- attachment handling never searches the workspace for a likely input file and
  never passes binary PDF content through a text-fetch interface;
- high-risk model routing and fallback behavior are observable and covered by
  tests;
- normal service logs and traces do not expose document field values; and
- an end-to-end channel test receives a PDF, completes or safely refuses the
  requested transformation, and delivers exactly one verified artifact or one
  truthful failure response.
