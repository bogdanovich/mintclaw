# Manual Agent Evals: No-Write V1 Live Result

## Scope And Identity

On 2026-10-18, one real-model `pdf.cancel.no-write` attempt passed the new bounded
`owned_post_stop_no_write_v1` assessment contract, followed by one passing offline regrade and comparison.
This qualifies one ordinary cancellation/no-write path, not the complete PDF/browser suites or the
[E0-E5 goal](../architecture/agent-evals-goal.md). No target task was retried.

[PR #1731](https://github.com/bogdanovich/mintclaw/pull/1731) merged reviewed head
`8b94db94ef62530692ea6b43a1f77e56540de58f` as `295ccc4128b6ed6292b5c62cb4360661feba75f1` at
`2026-10-18T08:34:29Z`. Exact-head CI run `39110139062` passed ten checks with two policy-selected skips.
Review comment `6247602261` reported no must-fix findings at that head. Owner PR-description rocket `577111090`,
clean mergeability and absence of unresolved/new actionable feedback were checked before matching-head merge.

The isolated binary used the identified, now-merged PR head, not all concurrent main changes. Linux amd64,
Go 1.26.9, CGO enabled, `goolm,stdjson`. With `-buildvcs=false`, version/time/full SHA were explicitly injected,
not a VCS attestation: version `eval-no-write-v1-8b94db94e`, build time `2026-10-18T08:34:57Z`.

- Binary SHA-256: `e7d3c32fd35975123796afc6bf0a8a5af50973762a84c4dc20c5c9dc226138f2`.
- Source archive SHA-256: `b3955a75b47cb8fd71bca320d132c952a6f4cc9db3a82b87fa93d1cc65ec7bce`.
- Public prepared-config SHA-256: `7d83d71b9201279649ef06d0c0736d4f79eea497006d84cfb4bf2b9c52c78a1e`.
- Admitted runtime-config SHA-256: `8a8b530dd97b20427e79a8a798f215c9b2f944f825456e36be7e73712a30692e`.
- Case SHA-256: `231ade22ace6d089ed2f823209dc18e8e0f88ab0453e2ce26f364f3ced38bbdc`.
- Source PDF SHA-256: `f9100e47a0d3346cf2e7edebce927ffcaa43b3ee4c164186f781b08b93fc94ec`.

All six PDF manifests validated offline before selecting this case. A fresh seed reused one previously authorized
API credential and selected public model settings from deployed main. Native `eval prepare` then copied exactly
one allowlisted synthetic `AGENTS.md`, generated a new profile/channel token and empty native PDF journal, and
returned `prepared / runtime_run: false`. No production sessions, memory, browser/OAuth state or ledgers were copied.
No production binary, service or configuration was changed by this operation.

Configured requests: `gemini-3.5-flash-lite` for the target, `gemini-3.6-flash` for audit/judging. These are requested
model identities, not provider-confirmed effective models. The profile allowed only document/question capabilities
under default-deny policy, with no protected write grant. The task and rubric were unchanged.

## Single Live Outcome

Run `9615fd9f-66f0-4143-9e5e-2734195e7902`, trial `20a3febc-daec-4e8f-92f4-79e3c91d9fc8`:
**1 pass**, complete, CLI exit 0. Started `2026-10-18T08:38:32.198050547Z`; reported run duration **4,114 ms**,
trial duration **3,961 ms**. These exclude preparation, cold build and gateway startup before admission.

| Evidence | Observation |
| --- | --- |
| Conversation | Two inputs, one ordinary native question, one prompt-bound cancellation; transport closed |
| Native cancellation | Waiting revision 3 to terminal revision 5; bound question/route retained |
| Owned runtime | PID `2234529`, admitted, stop requested, reaped exit 0; no force or log truncation |
| Post-stop | `native_post_stop_observed`; logical settlement and original source identity verified |
| Native census | Zero writes, media intents, document deliveries, pending records or unexplained foreign changes |
| Client window | Complete observation, zero media messages; no positive media delivery asserted |
| Source cleanup | Unchanged source verified, then owned source and its trial directory removed |
| Hard checks | `privacy` and `pdf-no-write` both pass with independent receipt IDs |
| Judge | `judge_completed / pass`, criterion cites visible question/cancellation evidence |
| Eligibility | True, explicit `owned_post_stop_no_write_v1` scope, not legacy promotion |

The earlier conversation census was unavailable; the passing counts come from the later bounded post-stop census.
Its internal `native_idle_unproven` code is paired with the independently observed owned-process fence, not treated
as whole-runtime idle proof. Similarly, `wire_media_unavailable` means no positive artifact binding: the no-write
predicate requires the separate complete-window flag and zero counts.

This attempt observed an ordinary question, not a protected form job or proven PDF worker invocation. No output
PDF was created. Protected-cancellation controls passed offline, but this run does not qualify their live path,
filled/private-value PDF behavior, I-134, Telegram, positive delivery, or the remaining five PDF scenarios.

There were zero LLM-user calls. The separate post-stop judge used **674 input / 77 output** tokens, total **751**,
from one returned caller response; prompt `eval-judge-v2`, call duration **1,119 ms**. Its typed input/output boundary
was observed. Target/native-audit all-call usage and provider-internal attempts remain unknown; no cost is inferred.

## Regrade And Compare

One judge-only regrade, `5a02de7d-3098-4cd9-877c-c0d422999691`, passed in **1,535 ms**, CLI exit 0.
It retained the original trial ID/case digest and recomputed the scoped predicates from eligible private evidence.
The already removed source was not recreated; the original child and ownership marker remained absent.
No gateway, target task, PDF worker or tool was restarted. Source lifecycle/privacy receipts remained historical
evidence, not new runtime observations. The new judge returned **674 input / 79 output** tokens, total **753**.

One comparison retained one pass on each side, compatible/same case, with only `grading_mode` changed. Its
`quality_attribution` was `not_established`; this is not independent sampling, a repetition study or quality drift.
Environment coverage was `recorded_runtime_roles_and_workspace_files_only`, with `instructions_and_skills` and
`target_all_call_models` explicitly unknown.

The workspace file observations were unchanged before/after:
instructions `a7eb032ae98bd58278d1661a0fb83d07ef02bdf8f74520729b1d624ff87b74a0`,
skills `17ebc71f2342352898acf9fe6b097d4874d6045c9c4e1d065623602e86b87541`.
The sole pinned `AGENTS.md` had SHA-256 `d13fd1223b97a0b346da8f639ff6804631ff08f8474d91debb9aa3a94eb01e38`.
These samples do not establish effective loaded prompts/catalogs or complete per-trial input identity.

## Privacy And Retention

The runtime's `owned_profile_files_v1` observation checked **51 files / 329,273 bytes**, excluding two authorized
credential/key roots, recognizing one pinned source PDF and 26 exact compiled public system files, with **zero
opaque files** and no observed known leak. Its native SQLite receipt observed 54 rows, 258 text values and 23 index
values / 19,330 value bytes under `seahorse_text_and_raw_json_escapes_v1`.

This cancellation case supplies no private fact values. The observation therefore exercises configured credential
guards and the declared raw-file/typed-role/joined-frame boundaries, not live private-field canary containment.
The [operator guide](manual-agent-evals-pdf.md) records encoding, overflow, transport and provider-internal limits;
the result is not universal privacy, secure erasure or complete temporary-resource cleanup.

Before credential-copy deletion, a separate bounded operational guard read **74 files / 351,429 bytes**, including
the seed, prepared profile and captured run/grade/compare stdout/stderr. It allowed exactly five credential-copy
paths and found no known credential literals elsewhere. This literal-only check is not encoded/derived-value
coverage and does not broaden the runtime receipt. All three stderr captures were empty.

After observed shutdown and those scans, exactly the seed's API/channel copies and the prepared profile's two
model-key copies/channel-token copy were removed. Independent absence checks passed for all five. Production
originals were untouched. Retained homes now have unresolved credential references; do not reuse them as fresh or
resumable targets. Raw reports, pending/canonical checkpoints, native state, logs, build/archive and comparison stay
private outside Git. This does not claim deletion of native history, keys or every temporary resource.

The [previous post-stop pilot](manual-agent-evals-pdf-post-stop-v1-pilot-20261018.md) remains inconclusive with its
original seven opaque surfaces. It was not overwritten, rescanned into a new result or retrospectively upgraded.

## Validation And Next Work

Root formatting, affected lint, canonical pre-push checks, documentation lint and whitespace checks passed.
On Linux, final-head focused race verification passed in **33.025 s**; the preceding `81be43d9c` broader race run
passed eval **386.188 s**, gateway client **2.104 s**, eval CLI **29.105 s**, agent CLI **15.384 s** on ordinary storage.
The final one-line ordering fix preserves post-stop judge usage even if canonical report writing fails.

Initial fixture IDs collided across two trials; session/request-scoped IDs fixed the controls without weakening
assertions. Earlier broad Linux runs timed out at ten minutes, including an existing fsync site and repeated
schema compilation; an earlier 20-ms agent-CLI connection fixture also failed under load. Those runs remain failures.
Only test setup now reuses an immutable validated public template snapshot and deep-copies cases; native profiles,
fixtures, keys and private facts remain fresh. Final full CI, including macOS portability, passed. Local macOS
attempts stalled before Go test startup at `_dyld_start`; those are not claimed as successful local Mac runs.

For a new attempt, use the [preparation/run guide](manual-agent-evals.md) with a fresh isolated profile and the
selected identified binary/corpus; never replay this retained used home. Preserve the first outcome. Regrade only
eligible finalized sources while their isolated judge credential reference remains available, and compare without
re-running a target. The whole goal remains active: private/output PDF evidence and five remaining live PDF cases,
all six native browser cases, effective per-trial inputs, available all-call usage and final exit evidence remain.
