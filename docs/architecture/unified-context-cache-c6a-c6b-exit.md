# Unified Context And Prompt Cache C6a/C6b Exit Record

Status: complete on 2026-10-03; live-provider and rollout qualification remain pending

This closes only the deterministic C6a/C6b slices of the
[unified context and prompt-cache roadmap](unified-context-cache-roadmap.md).
It records merged implementation and executable acceptance evidence, not a
documentation-only completion or a claim of live cache hits.

## Merged delivery

| Slice | Pull request | Reviewed head | Merge commit |
| --- | --- | --- | --- |
| C6a — shared cross-runtime corpus | [#1446](https://github.com/bogdanovich/mintclaw/pull/1446) | `7c882892944b050b2b5d068f14f6eec95e21777b` | `bb46f4cd0a5db384d6bcfe194f580b52ddf72d72` |
| C6b — lifecycle compatibility and regressions | [#1450](https://github.com/bogdanovich/mintclaw/pull/1450) | `99e1a68c73998289160c2f096de99d2d85f0b43d` | `fa9a678ea02b0eedd184314c57918b5dbe2624a8` |

Both exact heads passed all 16 GitHub checks, including Linux package and
integration tests, race checks, and macOS portability. Both had a trusted
owner PR-level rocket and no unresolved actionable thread at merge. The
[C6a re-review](https://github.com/bogdanovich/mintclaw/pull/1446#issuecomment-5973514137)
and [C6b final review](https://github.com/bogdanovich/mintclaw/pull/1450#issuecomment-5973775259)
reported no blocking findings.

C6b was retargeted from the C6a stack to main and validated after integrating
the remote-discovery fix from #1448 and the independently delivered
prompt-cache rollback controls from
[#1449](https://github.com/bogdanovich/mintclaw/pull/1449), merge
`10bacda4ef7e438f6d086a3d138efa28c4d73808`.

## C6a acceptance

One reusable read-only corpus runs through actual gateway, interactive coding,
headless coding exec, coding resume, and native remote-worker composition
roots. Model responses are scripted; production constructors, real `read_file`
execution, canonical JSONL, Seahorse, command dispatch, and worker subprocess
and HTTP transport remain in the path.

The independent oracle compares complete provider-visible message/tool
snapshots, rather than trusting the production fingerprint. Compatible
requests retain the entire prior prefix with a newly appended tail. Negative
controls must reject historical content, media, arguments, model, lineage,
tool-schema and order changes, missing/reordered tool evidence, and canonical
sidecar leakage. Every corpus request must still expose `read_file`, so a
consistently missing tool schema cannot produce a false pass. Captures are
detached from later runtime and test-reader mutation.

Gateway and coding restarts replay durable history. Presentation assertions
keep hidden carriers out of the user transcript. The five exact entry-point
tests and neutral-versus-HTTP capture boundaries are listed in the
[invariant suite](context-cache-invariant-suite.md#shared-corpus-c6a).

## C6b acceptance

The executable [eight-category coverage map](context-cache-invariant-suite.md#lifecycle-contracts-c6b)
covers tool loops and ordered results, steering/interruption, human suspension
and answer, attachments, durable restart/resume, provider fallback,
instruction refresh, and compaction/retained context. This is owner-level
lifecycle coverage plus the five-root corpus, not a claim that every category
is tested as a five-entry-point Cartesian product.

The strengthened real fixtures reproduced and fixed bounded shared-runtime
defects:

- Steering's provider-visible interpretation contract now survives durable
  replay in the existing frozen envelope, without changing raw user UI/search
  text or duplicating it in hidden context.
- Eager media becoming lazy historical media creates an intentional cache
  compatibility boundary. Ordinary non-media history and tool appends keep
  their prior lineage.
- One-shot tool-context presence and retirement create intentional boundaries
  derived from the existing live projection. No blob is reread for lineage and
  no protected payload, second journal or context manager is retained.

Instruction/model/tool/checkpoint incompatibilities intentionally reset
lineage; unchanged historical messages still require exact replay. Interrupted
terminal hints remain transient control, not durable history. Tool-call IDs
may be reused after a completed batch, but duplicate active calls and orphaned
or reordered results remain rejected by the oracle.

## Reproduce

From a checkout containing both merges:

```sh
bash scripts/test-context-cache-contracts.sh --all
```

The runner checks that every named gate exists, builds fresh worker/companion
binaries in a bounded temporary directory, and requires real subprocess and
WebSocket tests instead of accepting missing-binary skips. It needs no API key
or live model. `--unit-only` is a faster subset and explicitly does not prove
the subprocess/WebSocket gates.

The final post-main local run passed the oracle, agent, command, native-worker
and companion WebSocket groups. Additional validation passed related
steering/frozen/cache regressions, complete protocol/session/presentation and
Seahorse packages, focused race tests, and agent/config/protocol cache rollback
regressions. `make fmt`, changed-package lint (five packages, zero issues),
documentation lint and diff checks passed. Exact-head Linux/macOS CI supplies
the broader platform evidence.

## Remaining boundary

C6c still needs real OpenAI/Codex cache-read evidence for an ordinary second
turn, restart/resume fingerprint stability, and one expected post-compaction
miss followed by renewed reuse. Deterministic summaries protect retention
contracts; they do not establish live-model answer quality or cached usage.
Localhost wire captures deliberately omit unsupported cache controls and
cannot establish a provider cache hit. Unsupported usage stays `unknown`.

A non-blocking review follow-up remains: repeated historical tool-call IDs can
conservatively disable cache intent for a valid one-shot projection. Execution
and context correctness do not depend on that optimization; live qualification
must not misreport a disabled/unknown cache path as a hit.

C6d diagnostics/rollback have an implementation and
[operations baseline](../operations/prompt-cache.md) from #1449. Rollback-ready
deployment, bounded gateway/coding canaries, live context-quality evidence and
final user/operations qualification still need acceptance. Neither these two
code PRs nor this exit record deployed production, changed credentials, or
modified deployed configuration. The overall roadmap remains active.
