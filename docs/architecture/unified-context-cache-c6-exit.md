# Unified Context And Prompt Cache C6 Exit Record

Status: complete on 2026-10-03

This closes C6 and the
[unified context and prompt-cache roadmap](unified-context-cache-roadmap.md).
It combines deterministic cross-runtime coverage with live-provider,
restart, compaction, rollback, deployment, and semantic-retention evidence.
All diagnostic evidence was captured in `metadata_only` mode; no prompt text,
credentials, raw session identifiers, or provider cache keys are recorded here.

## Merged delivery

| Slice | Pull request | Reviewed head | Merge commit |
| --- | --- | --- | --- |
| C6a shared cross-runtime corpus | [#1446](https://github.com/bogdanovich/mintclaw/pull/1446) | `7c882892944b050b2b5d068f14f6eec95e21777b` | `bb46f4cd0a5db384d6bcfe194f580b52ddf72d72` |
| C6d diagnostics and rollback | [#1449](https://github.com/bogdanovich/mintclaw/pull/1449) | `0dbddd96027c4077df93dd42bd4b4228a00c8f4e` | `10bacda4ef7e438f6d086a3d138efa28c4d73808` |
| C6b lifecycle compatibility | [#1450](https://github.com/bogdanovich/mintclaw/pull/1450) | `99e1a68c73998289160c2f096de99d2d85f0b43d` | `fa9a678ea02b0eedd184314c57918b5dbe2624a8` |
| C6a/C6b acceptance record | [#1452](https://github.com/bogdanovich/mintclaw/pull/1452) | `257cbe3a39831893abad06aadefee476488eb273` | `e907796e89e8c20aed06f3358c2fa6d7de40ddf3` |
| Short-lived coding trace settlement | [#1454](https://github.com/bogdanovich/mintclaw/pull/1454) | `ea692e028a3d1a73c2f97207fc57e0fb7037e961` | `07981e4c42f412e65ac7f31ea203fa740e019be6` |

The code qualification baseline is merge `07981e4c42f412e65ac7f31ea203fa740e019be6`.
All 16 exact-head checks for #1454 passed. Its final review reported no
high-confidence issue, and the owner approved it. The earlier C6 code heads
also passed their complete Linux/macOS check sets before merge.

## Deterministic acceptance

The bounded runner passed from the final code baseline:

```sh
bash scripts/test-context-cache-contracts.sh --all
```

It requires every named gate, builds fresh worker and companion binaries, and
runs the shared corpus through gateway, interactive coding, `code exec`,
resume, and native remote-worker composition roots. The eight lifecycle
categories cover tools, steering, human suspension, attachments, restart,
fallback, instruction refresh, and compaction. The complete map and local
reproduction commands are in the
[context/cache invariant suite](context-cache-invariant-suite.md).

The #1454 shutdown regression additionally passed 20 consecutive native
end-to-end runs, focused race coverage, changed-package lint, and the full C6
contract runner. The regression requires exactly one completed trace with
`turn.start`, `model.request`, `model.response`, and `turn.end` for a
short-lived native `code exec` process.

## Trace settlement defect closed

Initial Linux coding canaries returned correct model answers but sometimes
persisted no trace. The asynchronous trace writer accepted the final records,
then `Close` stopped the process before its queue drained. That made
short-lived coding diagnostics nondeterministic even though normal agent
execution was correct.

#1454 added an explicit `WaitIdle` settlement barrier after all trace producers
close, bounded to two seconds. The existing non-blocking close escape hatch is
retained for shutdown safety. Native and unit regressions prove successful
drain, timeout behavior, idempotence, and exactly-once persistence.

## Live provider evidence

The deployed gateway and Linux coding canaries used OpenAI `gpt-5.6-terra`.
The independent macOS coding canary used OpenAI `gpt-5.6-sol`. All responses
reported provider-attributed cache usage, and all traces used redacted hashes
and counts only.

### Gateway ordinary turns and restart

One gateway session retained session hash `34e4b420…` and stable-prefix hash
`ff3e6d18f394ee365138dc84…` across ordinary turns and a restart of only the
main gateway service:

| Observation | Trace | History messages | Cached input |
| --- | --- | ---: | ---: |
| First bounded turn | `trace-turn-5120a0e6331a82665f360506` | 0 | 3,328 |
| Ordinary second turn | `trace-turn-b560633e311ffe5a6bb14f4f` | 2 | 3,328 |
| Resume after process restart | `trace-turn-374a7d3e17abbd315177a208` | 4 | 3,328 |

The restarted process reset its in-memory root-turn counter, while the durable
session, historical request shape, and stable prefix remained continuous.

### Linux coding resume

Separate `code exec resume` processes retained session hash `31cb5915…` and
stable-prefix hash `48e48459042575acc98b7e69…`. Representative completed
traces were:

- `trace-turn-c586abf8a2cd841cf9d84a86`: 8 history messages and 12,544 cached
  input tokens;
- `trace-turn-2749aac49194fdc68b7b02ed`: 10 history messages and 3,328 cached
  input tokens;
- `trace-turn-b3a4dff90c418590f73ab6ce`: 12 history messages and 3,328 cached
  input tokens; and
- `trace-turn-d9f008b432229f6a242f5f39`: 14 history messages and 13,568 cached
  input tokens.

This proves durable resume and deterministic trace settlement across fresh
coding processes, rather than only within one long-running TUI.

### macOS coding path

An isolated Darwin 25.6.0 x86_64 canary built exact source `07981e4c4` with Go
1.26.6. Its local binary hash was
`4b350f6c95246f7400d0ef50420ef20851adc248cf39b89c4952ff9f3fb24b67`.
Two separate commands created and resumed one thread:

| Observation | Trace | History messages | Cached input |
| --- | --- | ---: | ---: |
| New coding thread | `trace-turn-e7a277ebc29bdc9bf1c53d2a` | 0 | 3,712 |
| Resumed coding thread | `trace-turn-99c7e97e1183c437a559c388` | 2 | 3,712 |

Both used session hash `c1592571…`, stable-prefix hash
`e7ffac2ae923eac8…`, `prompt_cache_mode: enabled`, and completed with the
expected exact canary answer.

## Compaction and context quality

A real coding TUI compacted the Linux canary from 3.8k to 1.3k tokens in 5.3
seconds, saving 2.4k tokens. Immediately before compaction, trace
`trace-turn-d9f008b432229f6a242f5f39` reported 13,568 cached input tokens. The
first post-compaction trace, `trace-turn-2dea4335618c82ed780f0781`, reported
3,328. A later ordinary request reported renewed reuse of 12,544 tokens in
`trace-turn-5926a9b75eae58e6b1da64e4`.

This is the expected historical-prefix discontinuity followed by reuse of the
new checkpoint. OpenAI continued to reuse the unchanged leading system/tool
prefix, so its aggregate response remained a cache `hit` with 3,328 cached
tokens on the first post-compaction request. C6 does not relabel that truthful
partial reuse as a zero-token miss.

Semantic retention was checked independently so a cache hit could not stand in
for answer quality. A second live thread placed these critical constraints only
in its oldest turn:

- release codename `MINT-FERN-4821`;
- target file `README.md`; and
- database migrations are prohibited.

After five filler turns, the original turn was outside the protected recent
tail. Seahorse compacted 2.9k to 1.3k tokens, saving 1.6k. The first
post-compaction answer reproduced all three values exactly. Trace
`trace-turn-7b75ca2a28b581af27b7be17` recorded the completed request and 10,496
cached input tokens without exposing the prompt.

## Rollback and deployed health

The main gateway's prompt-cache configuration was backed up, switched to
`disabled`, restarted, observed, then restored byte-for-byte and restarted
again. Trace `trace-turn-9df827012f86aac4ee50ff72` confirmed
`prompt_cache_mode: disabled`. The provider still reported 3,328 cached input
tokens through implicit caching, which is permitted: the rollback removes
MintClaw's explicit cache plan but cannot disable provider-owned implicit
caching. Restored traces `trace-turn-d9ab8b175fdeadbaa2956be3` and
`trace-turn-531f439c0a328f2673759abf` confirmed enabled mode and positive reuse;
the latter reported 21,760 cached input tokens.

The final runtime deployment used version
`mintclaw v0.1.0-p8a.2-2590-g07981e4c4`. The deployed core binary SHA-256 was
`7de6f8183b9367973318868913cc24f3bad86406175411aad5ae870a1886d655`.
The pre-deploy core, node, launcher, unit definitions, unit states, repository
heads, and build hashes are retained in the timestamped deployment backup.
Only the six affected gateway services were restarted. Post-deploy checks
reported 10 of 10 services active, no failed units, no recent service errors,
healthy launcher/reviewer endpoints, and no legacy runtime artifacts.

## C6 conclusion

C6 is accepted because:

- deterministic prefix and lifecycle contracts pass through every supported
  gateway and coding composition root;
- supporting live OpenAI routes report positive cached input on ordinary
  second turns;
- gateway and coding restart/resume preserve their compatible stable prefixes;
- compaction produces one observable historical-prefix break and subsequent
  checkpoint-prefix reuse without losing critical constraints;
- hidden carriers remain absent from user-facing transcripts and traces remain
  metadata-only;
- Linux and macOS coding paths pass with real providers; and
- the deployed gateway passed an exercised disable/restore rollback and final
  health audit.

Provider cache retention remains external and may vary over time. That affects
cost and latency, not context correctness. Future regressions should first run
the deterministic suite, then follow the bounded canary and rollback procedure
in the [prompt-cache operations guide](../operations/prompt-cache.md).
