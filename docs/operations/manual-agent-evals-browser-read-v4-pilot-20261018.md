# Manual Agent Evals: Browser Read V4 Pilot

## Outcome

On 2026-10-18, one fresh isolated `browser.fixture.read` trial on merged
[PR #1754](https://github.com/bogdanovich/mintclaw/pull/1754) returned **pass**, CLI exit 0, in **15,943 ms**
after admission. Both scripted turns completed. Independent fixture state, native authority, no-write, scoped raw
privacy and cleanup checks passed before a separate tools-disabled judge returned pass. One subsequent judge-only
regrade passed in **1,343 ms** without target/tool work. This qualifies one Linux local-owned read path, not E4.

The [v1](manual-agent-evals-browser-read-v1-pilot-20261018.md),
[v2](manual-agent-evals-browser-read-v2-pilot-20261018.md) and
[v3](manual-agent-evals-browser-read-v3-pilot-20261018.md) inconclusive experiments remain unchanged. This was one
declared attempt on newly merged code, with no task retry. The [full goal](../architecture/agent-evals-goal.md)
remains active: the other five browser cases, five PDF cases, adaptive qualification and remaining E5 work are required.

## Code And Admission

Reviewed head `533c8f06e4c9b24813742723354000d289e61257` merged as
`fd77753f00a0eba9b2d52a68f730d08140aceccd` at `2026-10-18T18:42:29Z`. Exact-head CI run `39154137927`
passed ten checks; frontend/PDFium jobs were policy-skipped. SHA-bound reviewer writeback `6253671766` at
`18:41:56Z` confirmed the prior finding resolved and no new must-fix issues. Owner PR-description rocket `577831390`
at `18:41:58Z`, green CI, clean mergeability and no open/new actionable feedback were checked before matching-head merge.

The first review found that native/wire final IDs could match while their contents contradicted one another.
Both added negative controls reproduced the false admission before the fix, including a judge call. Final text now
must exactly match the existing configured native footer projection; the channel manager and observer share one pure
formatter. No suffix stripping, text normalization or independent formatting algorithm was added. Controls reject
contradictory text and missing/forged footers, accept exact enabled/disabled projections, and prevent judge calls on
mismatch. One substantive review-fix cycle added no new authority or runtime state machine.

The identified Linux amd64 binary used Go 1.26.9, CGO enabled, `goolm,stdjson`, version `eval-browser-read-v4`, build
time `2026-10-18T18:26:00Z`. The full source SHA was injected, not independently VCS-attested. It was built before
merge and executed only after confirmation. The source archive and changed Go file hashes matched the reviewed tree.

- Binary SHA-256: `697987037e0196321d6b096d7f1513a0e53a8d9e969a510c367b832b680dea75`.
- Sidecar SHA-256: `9809546285a191ce0e64501486b9df47efd86907bd364ff483bc9d5cdd52802a`.
- Browser seed template SHA-256: `19348a11765e4e81f4aeaf13a0150489773c2537cf79d2e81b89b63495f2f681`.
- Prepared public-config SHA-256: `1b57dda381119ca32ab43501c5005febe6bdef8a5554a7ccf4c9d0375bbb6c9e`.
- Unchanged case SHA-256: `79de150ac381ae3ee5953a4c8c0b7c5c1e61e42c3dbc7cd2efb48a527c6c5f25`.

All 14 manifests validated offline. Native `eval prepare` created a new private home with one allowlisted synthetic
`AGENTS.md`, empty native browser roots, rebased fixture origin and fresh channel token. Only already authorized
deployed-main model credentials/public settings were narrowly reused. No production sessions, memory, ledger,
OAuth or browser state was cloned. Exact fixture navigation/read policy remained unchanged. The run used Chrome
155.0.8059.39, Node 22.23.3 and pinned Playwright 1.63.0, a six-minute suite budget and unchanged task/turn bounds.

## Execution And Evidence

Run `51baeb44-c32d-4d5c-b3ce-e0b0637711d5`, trial `abb1bc6d-8c89-4bba-9db9-fd7508b39ad5`,
started `2026-10-18T18:42:51.034745765Z`; complete report, trial duration **15,689 ms**. Reported run time excludes
cold build, preparation and owned gateway startup before admission.

| Boundary | Observation |
| --- | --- |
| Conversation | Two inputs/two final replies in one session; zero approval prompts |
| Target model | Final events identify `gemini-3.5-flash-lite`; not all provider attempts |
| Independent fixture | One page visit, fresh reference matched, zero downloads/submissions/removals |
| Native read | One closed session, one prepared navigation, one succeeded invocation, two bound final deliveries |
| Native binding | Actor/agent/route/execution/action/profile/policy and exact configured final content matched |
| Settlement | `owned_post_stop_browser_read_v1`, logically settled, no forbidden action accepted, temporary roots empty |
| Hard checks | State, authority, no-write, scoped privacy and cleanup all pass |
| Judge | One tools-disabled `gemini-3.6-flash` request; pass; 960 ms, 669 input/85 output tokens |
| Owned lifecycle | PID `2969330`, native exit 0, not forced, JSON capture not truncated; PID independently absent |
| HTTP host | Closed/joined with one trial and matching origin digest |

The original broad `remote_state: unknown` and `domain_resources_unknown` labels remain: the new receipt is scoped
read evidence, not a claim about every runtime capability/resource. The browser ephemeral and gateway temp roots
were independently empty. No harness deletion manufactured privacy or domain cleanup evidence.

Run `797c74d5-3f40-499d-bef7-235dd8149d6d` regraded the retained eligible trial once with a separate judge request:
pass, CLI exit 0, 669 input/93 output tokens. Credential file references were temporarily resolved only for grading,
then their five owned copies were removed again. There was no new gateway/browser/task execution. Comparison of
run/regrade retained both passes and explicit grading-mode drift. Comparison against v3 retained one inconclusive
and one pass with the same case digest, but runtime/configuration/role drift and unrecorded effective inputs prevent
causal quality attribution. Neither comparison used a model or target.

Target all-call usage remains unknown in the eval report. Separate read-only native final metadata exposes
**7.4k input / 35 output** and **64.8k input / 647 output** tokens for the two turns; these available counters are not
complete provider-attempt accounting and are not silently promoted into an all-call total. Integer values remain in
private native evidence. Actual loaded prompt/skill/catalog identities are still unqualified; unchanged workspace
file samples do not establish them.

## Privacy, Cleanup And Validation

The bounded `owned_profile_files_v1` raw observer scanned **51 files / 395,990 bytes**, with zero opaque files,
26 exact compiled public-system-file witnesses and one excluded authorized credential directory. No known leak
was observed. Native SQLite evidence covered 161 rows, 504 text values, 58 index values and 54,069 value bytes under
`seahorse_text_and_raw_json_escapes_v1`. These scopes retain their encoding/transport/provider-internal limits.

Before deleting credentials, the separate operational literal-copy guard scanned **68 files / 423,949 bytes** for
the live phase and **75 files / 427,632 bytes** for regrade, allowing exactly five owned credential-copy paths each
time and finding no known literal elsewhere. All five copies were removed after each phase. Production config and
security hashes remained unchanged. This supplemental guard is not a replacement for native raw-profile or typed
model-role privacy evidence. Raw captures/state remain private outside Git; unresolved profiles must not be reused
for live work. No production binary/config/service was deployed, changed or restarted.

Before/after production checks showed all ten expected units active, zero failed units and zero error-level journal
entries in ten-minute windows. Deployed core stayed `71929b4aa773bebe598598a7fd059c16fcdf2146`.

Before the review fix, full related eval/client/CLI race suites passed on Linux (448.077/1.604/24.046 s) and macOS
(414.287/3.050/11.158 s); Linux browser/tools ordinary suites passed (66.656/263.616 s). Final content-binding race
controls passed on Linux (eval 62.945 s, channels 1.213 s) and macOS (eval 41.006 s, channels 1.943 s). Full macOS
channels passed (29.144 s). Root formatting, affected lint, canonical four-package pre-push checks and exact-head CI
passed. Ordinary tests use stubs; no paid eval runs in CI. No macOS live, Windows, companion/cloud, adaptive browser,
artifact/protected-input/denial/context or complete browser/PDF-suite qualification is claimed.
