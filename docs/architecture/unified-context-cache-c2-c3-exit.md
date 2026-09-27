# Unified Context And Prompt Cache C2/C3 Exit Record

Status: implementation complete in the C2/C3 merge sequence; C4 and later
phases remain explicitly deferred.

This record closes only the durable turn-envelope and stable-prefix phases of
the [unified context and prompt cache roadmap](unified-context-cache-roadmap.md).
It does not claim completion of chronological compaction, provider-specific
cache planners, live cache evidence, or rollout.

## Merged sequence

| Phase | Pull request | Merge | Result |
| --- | --- | --- | --- |
| C2 sidecar | [#1346](https://github.com/bogdanovich/mintclaw/pull/1346) | `18a2195fc` | Added the versioned provider-neutral turn-envelope sidecar while keeping canonical JSONL as the transcript authority. |
| C2 reconciliation | [#1355](https://github.com/bogdanovich/mintclaw/pull/1355) | `8fea51a32` | Round-tripped envelopes through JSONL and derived Seahorse reconciliation without exposing hidden context as visible/search text. |
| C2 admission | [#1363](https://github.com/bogdanovich/mintclaw/pull/1363) | `d5d870209` | Froze root-turn context at admission and replayed it across tool loops, interaction continuation, restart, and legacy rows. |
| C3 snapshot | [#1371](https://github.com/bogdanovich/mintclaw/pull/1371) | `8d5dddf87` | Added stable-system/tool snapshots, canonical tool ordering, explicit lineage rotation, and hash-only diagnostics across gateway, coding, fallback, streaming, side-question, final-render, vision, and Seahorse paths. |
| C3 hook guard | This closeout change | This change | Makes the completed transcript prefix immutable to `before_llm` hooks while retaining bounded current-tail append/rewrite behavior. |

No PR introduced a second transcript, context manager, session database, or
provider-owned conversation authority.

## C2 acceptance evidence

- `TestFreezeTurnEnvelopeMakesDynamicContextReplayStable` proves that wall-clock
  and other mutable runtime inputs do not rewrite an admitted root turn.
- `TestSetupTurnPersistsAndReusesFrozenTurnEnvelope` and
  `TestFrozenTurnEnvelopeSeahorseRestartKeepsProviderBytes` cover durable replay
  and restart/reconciliation byte stability.
- `TestFrozenTurnEnvelopePreservesHistoricalMultiSenderContext` preserves
  sender attribution without rebuilding it on later turns.
- `TestCodingPromptPublishesCatalogAndInjectsFrozenSelectedSkill` and coding
  workspace/instruction regressions retain the coding trust and refresh
  boundary while freezing admitted turn context.
- `TestJSONLTurnEnvelopeRoundTripsWithoutChangingVisibleContent` keeps hidden
  envelope context separate from visible/searchable transcript text.
- `TestEstimateMessageTokensIncludesFrozenTurnEnvelope` accounts for the
  provider-visible carrier in token budgets.
- `TestJSONLLegacyMessageWithoutTurnEnvelopeRemainsLossless` and
  `TestSetupTurnLegacyInteractionContinuationKeepsDynamicContext` preserve
  lossless legacy resume without inventing frozen historical state.

## C3 acceptance evidence

- `TestPromptCacheFingerprintCharacterizesGatewayAndCodingRequests` and
  `TestPromptCacheLineageIsSharedAcrossGatewayAndCodingProfiles` exercise one
  provider-neutral fingerprint/lineage contract in both products.
- `TestPromptCacheLineageRotatesOnlyForStablePrefixChanges` proves that dynamic
  current-turn context does not rotate the stable lineage while stable system
  changes do.
- `TestPromptCacheLineageRotatesForWorkspaceInstructionChanges` covers explicit
  lineage resets for gateway memory/workspace and coding `AGENTS.md` changes.
- `TestPromptCacheLineageToolSchemaIsCanonical` and
  `TestCanonicalProviderToolDefinitionsSortsWithoutMutatingInput` make tool
  ordering deterministic; a changed tool profile receives a different lineage.
- selected skill instructions live in the frozen current-turn envelope rather
  than rewriting stable bytes ahead of completed history.
- `TestHookManager_BeforeLLMRejectsCompletedTranscriptMutation` and
  `TestLLMHookCompletedPrefixUnchanged` reject completed-prefix rewrite,
  removal, reordering, and insertion while allowing current-tail changes and
  append-only context.
- passive diagnostics contain only snapshot versions and hashes. They contain
  no prompt bodies, tool arguments, raw session identifiers, or cache keys.

## Deferred boundary

C4 must still replace summary injection with ordered Seahorse checkpoints and
adopt pressure/hysteresis-based compaction. C5 must add provider-specific cache
placement. C6 must supply cross-runtime and live cache-reuse evidence. Nothing
in the C2/C3 sequence marks those phases complete or changes their acceptance
criteria.
