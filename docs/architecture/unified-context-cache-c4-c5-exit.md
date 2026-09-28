# Unified Context And Prompt Cache C4/C5 Exit Record

Status: complete on 2026-09-28; C6 remains pending

This record closes only C4 (ordered compaction checkpoints and pressure policy)
and C5 (provider cache planners) from the
[unified context and prompt-cache roadmap](unified-context-cache-roadmap.md).
It does not claim the live, cross-runtime, or deployment evidence reserved for
C6.

## Merged delivery

| Stage | Pull request | Merge commit | Result |
| --- | --- | --- | --- |
| C4 | [#1388 — order compaction checkpoints in history](https://github.com/bogdanovich/mintclaw/pull/1388) | `8dc55962f7c66530ef33fb83d42e262fab357d01` | Checkpoints are chronological context items followed by exact retained transactions. |
| C4 | [#1396 — compact only under effective-window pressure](https://github.com/bogdanovich/mintclaw/pull/1396) | `68436c13d9b3c1c5821318157e6f75be95352d7b` | Effective windows, reserves, hysteresis, and stable no-op generation govern compaction. |
| C5 | [#1405 — compile cache plans for OpenAI](https://github.com/bogdanovich/mintclaw/pull/1405) | `281e84b8230c857fae5df62bea1bd116834bfd2a` | A validated provider-neutral plan compiles to exact OpenAI/Codex capabilities and fails closed. |
| C5 | [#1410 — compile cache plans for Anthropic](https://github.com/bogdanovich/mintclaw/pull/1410) | `787b4efac69e273bdc83340ed8fb901bdfc6be07` | Native SDK and Messages routes mark stable system/tools and legal completed transactions. |
| C5 | [#1413 — complete provider cache attribution](https://github.com/bogdanovich/mintclaw/pull/1413) | `afe0232d7f008cdd30a9121f1d828d7cd5523257` | Gemini implicit-cache usage, CLI observe-only behavior, retry/fallback lineage, and successful-attempt attribution are covered. |

Every code PR completed the normal ready-PR workflow with required GitHub
checks green, an exact-head clean review, a trusted pull-request-level rocket,
and merge-commit semantics.

## C4 acceptance evidence

The C4 criteria are executable rather than documentation-only assertions:

- `TestContextBuilderOrdersCheckpointBeforeRetainedHistory` and the Seahorse
  assembler fixtures prove that a checkpoint stays at its chronological
  boundary and that retained raw transactions follow it in exact order. The
  test runs the shared ordering contract in both gateway and coding profiles.
- `TestCodingLongSessionCompactionContinuity` covers protected coding state,
  raw recent turns, checkpoint continuity, restart, and rebuild.
  `TestAssemblerRecentTailPreservesToolPairing` prevents a tool result from
  being separated from its call.
- `TestContextCompactionWatermarksUseEffectiveWindowAndHysteresis`,
  `TestPostDeliveryCompactionRunsOnlyUnderPressure`, and the bounded-assembler
  watermark tests use the effective model window plus prompt, tool, output,
  and summary reserves instead of a fixed small-tail trigger.
- `TestSeahorseCanonicalCheckpointGenerationIsStableUntilTranscriptChanges`
  proves that a no-op does not rotate checkpoint generation. The lineage tests
  bind the cache key to that generation, so one real checkpoint change creates
  one intentional reset and later requests reuse it.
- The shared planner marks one-off Seahorse summary requests `no_write`, with
  `TestPromptCachePlanDisablesWritesForSeahorseSummaries` covering the
  provider-neutral contract.

Together these fixtures meet the C4 done criteria: no speculative compaction,
complete transaction retention, one reset per real checkpoint change, stable
no-op lineage, and preserved protected state in long sessions.

## C5 acceptance evidence

C5 has one typed boundary and endpoint-specific compilers:

- `PromptCachePlan` is versioned, detached from caller maps, validated, and
  fail-closed. `TestPromptCachePlanOptionsDetachAndValidate`,
  `TestPromptCachePlanOptionsFailClosed`, and
  `TestPromptCacheNoWriteDropsBreakpoints` cover that contract.
- OpenAI request fixtures assert exact lineage-key and explicit marker
  placement for supported GPT-5.6+ endpoints. Earlier OpenAI and Codex OAuth
  retain only supported lineage behavior, while compatible third-party
  endpoints receive no unknown OpenAI cache fields.
- Both Anthropic adapters compile the same neutral plan into stable-system,
  final-tool, and latest completed-transaction markers. Native endpoint,
  custom-endpoint, no-write, malformed-plan, and exact system-separator
  fixtures prevent marker leakage or semantic reordering.
- `TestGeminiProvider_PromptCachePlanPreservesImplicitCacheShape` proves that
  Gemini keeps stable system/tools plus the ordered dynamic tail without
  serializing MintClaw's internal plan. Streaming and non-streaming fixtures
  retain `cachedContentTokenCount`, including a provider-reported zero.
- `TestChat_PromptCachePlanIsObserveOnly` and
  `TestCodexCliProvider_PromptCachePlanIsObserveOnly` prove that CLI adapters
  forward no MintClaw cache control while preserving cache usage reported by
  their subprocesses.
- `TestFallbackAttemptUsesActualProviderAndModelLineage` proves compatible
  retries reuse a lineage and an actual provider/model fallback starts a new
  one. `TestActualModelCapabilityReplacesStaleDocumentRenderAuthority` binds
  the successful fallback's provider/model identity to its own cache usage.

These fixtures meet the C5 done criteria: exact supported markers, no unknown
fields on unsupported APIs, unchanged semantic message order, intentional
retry/fallback lineage, and cache usage attributed to the successful provider
attempt.

## Deferred boundary

C6 is deliberately still pending. Its shared scenario corpus, live cache-hit
evidence, restart/resume fingerprints, post-compaction live reuse, Linux and
macOS canaries, operator diagnostics, rollback evidence, and production deploy
must be delivered and accepted separately. Consequently the overall roadmap
remains active even though C0 through C5 are implementation-complete.
