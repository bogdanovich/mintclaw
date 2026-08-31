# Architecture Simplification Z1 Session Cutover

Date: 2026-08-30

Outcome: passed. The deployed MintClaw session corpus now uses only the strict
current metadata, scope, and flat tool-call contracts. The operation did not
add a runtime reader, normalizer, dual write, or startup migration.

## Scope and authority

The owner authorized backup, stopped-state migration, installation, restart,
verification, matched rollback, reapply, and cleanup. The operation ran on the
five active profiles and all 20 deduplicated normal-session roots while the
running gateway binary reported source `edd8759b`.

The affected writers were the five gateways, main Web service, review queue,
and reviewer webhook. They were stopped before the authoritative snapshot and
remained stopped while the converter read the source and while the candidate
was installed. Configuration load checks passed for all five profiles. Doctor
exit code 2 contained existing policy findings only; no load or schema error
was present.

## Recovery set

The compact private recovery set is:

```text
/home/server/mintclaw-z1-recovery-20260830T232121Z
```

It contains the five active configurations and run scripts, reviewer service
environment, affected units and drop-ins, pre-operation service state, one
copy of each distinct effective binary, manifests, canary evidence, and the
targeted stopped-state session archive. It does not contain an unnecessary
copy of the complete MintClaw home.

Key digests:

| Artifact | SHA-256 |
| --- | --- |
| Running gateway binary | `8abc849dffb3a53bcba02be5f21e1b77533db7432853ac5845cf13dceff46b53` |
| Launcher binary | `9aec6c7bb413d2c36e9a9d02d9a0badbe402cd943f19b6eb11d80817d6c00bac` |
| Stopped-state session archive | `d5e8c944c1c752456a4345053d6c05047eec64af721e256194e790d88c086aa0` |
| Authoritative stopped conversion manifest | `4173f1edc1beac14620e3642b3287b00c9c51052f76383f16d8abbabb3b02182` |
| Final post-observation manifest | `3abd30cfdcae71dcce9949204d014f76184990621aeb701057d8e6a4b844ee78` |

The 1.38 GiB source archive compressed to 357 MiB. `zstd -t`, a complete tar
listing, and its SHA-256 all passed before mutation and again before cleanup.
The retained monthly R1 full-state baseline and the preceding compact recovery
set remain separate recovery points.

## Authoritative conversion and installation

The stopped-state converter produced this authoritative cohort:

| Measure | Result |
| --- | ---: |
| Current metadata retained | 819 |
| Current histories retained | 640 |
| Non-current metadata archived | 2,828 |
| Non-current histories archived | 636 |
| Root `aliases` members removed | 329 |
| Nested tool calls flattened | 3,721 |
| Google-specific tool-call cases flattened | 2 |
| Current messages validated | 10,232 |
| Archived count divergences preserved | 1 |

The single archived divergence was the already audited 1,297 metadata count
versus 1,299 physical history records. The archive was preserved byte-for-byte;
the converter did not decode or repair its historical payloads.

Across all roots, 1,459 retained files matched their recorded SHA-256, 3,464
archived files were absent from the install trees, and every non-contract tree
matched its source. The candidate replaced the stopped live session domains by
same-filesystem atomic rename. An installed-state verification then found 819
current metadata documents, 640 histories, 10,232 messages, and no remaining
archive or conversion work.

## Runtime verification

After the first start, background Seahorse reconciliation completed. A retained
pre-cutover service session then resumed and returned the exact marker
`Z1_RETAINED_SESSION_OK`; trace
`trace-turn-07ce60d7851d1a6462c3fdbb` completed with eight records and no
truncation.

The remaining canaries established:

- VPN terminal open, attach, dimensions, UID, `MINTCLAW_PTY_OK`, close, and
  final `state=closed`;
- exactly `p5a-canary`, `ab-local-test`, and `vpn` connected on protocol 1;
- companion browser open, observe, and close in child trace
  `trace-turn-73b9407878dd9c29515ac13d`, with no tool error and a closed final
  state; and
- all expected services active, expected HTTP 302/404/401 probes, no failed
  unit, no legacy product process, and no error-level journal entry.

Tool traces, rather than model-provided marker text, are the browser evidence.
One later model response claimed a browser marker after using node tools, so it
was rejected as proof. The final reapply browser evidence is child trace
`trace-turn-72f673d8cd9ab5f7c76eff1a`, which records the exact
browser-session/open, browser-observe, browser-session/close sequence.

## Matched rollback and reapply

The services were stopped after initial canaries. The post-cutover candidate
was preserved, the targeted archive and all original source files were
checksum-verified, and the untouched pre-cutover session trees were restored
together with the exact same binary and configuration.

The restored baseline became healthy and returned `Z1_ROLLBACK_BASELINE_OK` in
completed, untruncated trace `trace-turn-5dc58a6918476adb7feda574`. Services
were stopped again, the rollback-observed trees were quarantined, and the
candidate was restored. Device and inode identity passed for all 20 roots and
all 1,463 candidate contract files matched their pre-rollback digests.

After final restart, the retained session returned `Z1_FINAL_REAPPLY_OK` in
completed trace `trace-turn-1ae487d1e036b7159f1d3b46`; VPN terminal and
browser child canaries passed again. The post-observation verifier, run after
additional live writes, reported 828 metadata documents, 649 histories, 10,400
valid messages, and zero archived documents, aliases, nested tool calls, or
Google cases to transform.

## Observation findings and cleanup

The session compatibility reset passed, but the exercise exposed three
separate runtime ownership defects:

1. `mintclaw-main.service` exceeded its 30-second stop budget and systemd sent
   `SIGKILL` during all five stops observed in the cutover and observation window.
2. Seahorse provenance mutation collided with another SQLite writer and
   returned `SQLITE_BUSY`, including one recurrence after final reapply.
3. The live-agent client did not terminate after the gateway delivered an
   error final through the outbox; it waited for its outer timeout.

These are registered as O1-O3 in the architecture simplification roadmap.
They did not cause a strict-state validation failure, archive mismatch, or
rollback mismatch, but they require focused fixes before the program is
declared complete.

Trace lookup also required time, profile, and session correlation because a
`root_turn_id` can recur across restarts. The evidence above uses the matched
trace instance and records whether it completed and was truncated.

After the observation window, the final manifest was copied into the retained
recovery set. Five rollback-observed trees, the converter staging tree, and the
preflight copy were deleted after confirming no process held them. Free space
increased from 132 GiB to 133 GiB. The verified targeted session archive is
retained as the rollback artifact; the temporary converter source and tests
are deleted by this closeout.
