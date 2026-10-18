# Manual Agent Evals: Browser Read V2 Pilot

## Outcome

On 2026-10-18, one explicitly declared fresh-profile `browser.fixture.read` experiment on merged
[PR #1748](https://github.com/bogdanovich/mintclaw/pull/1748) returned **inconclusive**, CLI exit 1,
`domain_evidence_unavailable`, in **9,234 ms** after admission. Native browser startup succeeded, but navigation
required approval. The harness refused an unbound approval and sent no `allow_once`. The fixture recorded **zero
page visits**, downloads, submissions or removals. There was no final answer or browser pass.

This is a new identified-code experiment, not a retry/replay or replacement of the
[v1 startup failure](manual-agent-evals-browser-read-v1-pilot-20261018.md). Both outcomes remain retained. The
[E0-E5 goal](../architecture/agent-evals-goal.md) is active and all six browser cases remain unqualified.

## Code And Admission

Reviewed head `e602a8c78e8ee507498c35dcfd716c0ba85c5ec1` merged as
`2378818f1c936098090dc75e308308819b3da496` at `2026-10-18T14:33:57Z`. Exact-head CI run `39134451963`
passed ten checks with two policy-selected skips. Actual reviewer writeback `6251025269` reported no high-confidence
issues on that head; two documentation files were excluded. Owner PR-description rocket `577505104`, green current
checks, clean mergeability and no unresolved/new actionable feedback were verified before matching-head merge.

The isolated production source came from that PR head and matched the merged tree. The binary was built before
merge and executed only after merge confirmation, with explicitly injected rather than independently VCS-attested
identity: Linux amd64, Go 1.26.9, CGO enabled, `goolm,stdjson`, version `eval-browser-read-v2`, build time
`2026-10-18T14:06:00Z`. Operational helpers were built separately in the private directory.

- Binary SHA-256: `bd442fa9eb74a1b3c65be2c4eeab61b8a889cb01e4a31c3cf14dc2dcaf31b11a`.
- Sidecar SHA-256: `cf628d3907ed1cdd2e94af77d66faf03a5f1130105af7f52dd47e0e86da78d78`.
- Admitted runtime-config SHA-256: `52caf71577df9bce3ddea1100506227798dec0a3c3d807a6782b6c283134abb8`.
- Unchanged case SHA-256: `79de150ac381ae3ee5953a4c8c0b7c5c1e61e42c3dbc7cd2efb48a527c6c5f25`.

All 14 manifests validated offline. A new isolated seed narrowly reused the already authorized deployed-main API
credential/public model settings. Native `eval prepare` created a fresh home/channel token, one allowlisted synthetic
`AGENTS.md`, empty browser runtime/lock roots and a rebased fixture origin, returning `prepared`, `runtime_run: false`,
`browser_prepared: true`. No production sessions, memory, OAuth, ledger or browser state were copied. Task, expectations
and three-minute turn/ten-minute case bounds were unchanged; suite limit six minutes, one attempt, no retries.

Requested target/user model was `gemini-3.5-flash-lite`; configured judge `gemini-3.6-flash`. No reply-event model
label was observed because the trial ended at approval, so these are requested identities only.

## Execution And Authority

Run `f3e29f46-40f9-452c-9f82-da0dd5401965`, trial `2c0b57eb-b504-4684-9e04-58a5eac8178d`,
started `2026-10-18T14:34:19.267043288Z`; complete report, trial duration **8,819 ms**. Run duration excludes cold
build, preparation and gateway startup before admission. Complete report publication is not completed task execution.

| Boundary | Observation |
| --- | --- |
| Conversation | One input and one approval prompt; no approval reply or final response |
| Native persisted projection | One closed `gateway / eval-browser` session, one prepared action, one invocation |
| Prepared action | `navigate`, native effect/policy effect `navigation`, policy approval mode, local/restricted decision `ask` |
| Invocation projection | `canceled` after shutdown; not a whole-task or intended-operation cancellation proof |
| Independent fixture | Zero visits/downloads/submissions/removals; healthy sealed local witness |
| Mandatory checks | State, authority, no-write, privacy and cleanup remain inconclusive |
| User/judge | Zero calls; evidence ineligible for judging and offline browser regrade |
| Owned child | PID `2709284`, admitted, stopped/reaped exit 0, not forced, log not truncated |
| HTTP host | Joined/closed after child stop; one retained trial, listener-only receipt |

The current eval policy permits explicit `read`/`local_edit` effects on its one fixture origin. Native navigation is
a separate `navigation` effect, so its ask rule was exercised rather than silently treated as read. No harness code
converted the prompt into an automatic grant. Next, reconcile exact fixture-navigation admission with this native
effect and complete native action/approval correlation. Commits/unknown effects remain excluded; any actual
`allow_once` still needs exact owner/session/execution/profile/action/interaction evidence and declared scope.

These post-stop persisted projections are diagnostic observations, not an integrated native authority grader.
Neither a closed session nor a canceled record proves the intended action was correctly authorized, that all
logical work settled, or that all private resources were covered. The ephemeral directory was empty; independent
checks found the admitted PID absent and no process command naming this profile. Retained lock files are not live
process authority, and no foreign process was adopted or killed.

Comparison against v1 completed with exit 0, identical case digest, one inconclusive trial per version, and explicit
runtime/configuration drift. It preserved both failures/unknowns and did not attribute quality improvement or promote
startup repair to a page-read pass. Target all-call usage/provider attempts remain unknown; no total cost is inferred.

## Privacy And Retention

Post-stop `owned_profile_files_v1` inspected **84 files / 1,523,194 bytes**, excluded one authorized credential
directory and recognized 26 exact compiled public system files. No known leak was observed, but **32 opaque files**
retained unavailable coverage. The SQLite witness observed 112 rows, 420 text values, 36 index values and 45,852 value
bytes under `seahorse_text_and_raw_json_escapes_v1`; this does not remove other file/encoding limits.

Before credential-copy removal, the separate bounded operational literal guard inspected **101 files / 1,548,911
bytes**, allowing exactly five owned paths and finding no known credential literals elsewhere. All five seed/profile
copy paths were then verified absent; production config/security source hashes stayed unchanged. This is scoped
literal evidence, not universal or encoded-value privacy proof. Seed/prepare stderr were empty; run stderr was 54
bytes. Retained profiles have unresolved credential references and must not be reused. Raw captures/state remain
private outside Git; no recursive cleanup manufactured a passing privacy/cleanup result.

Production health before/after showed all ten expected units active, no failed units and no error-level journal
entries in ten-minute windows. Deployed core remained `71929b4aa773bebe598598a7fd059c16fcdf2146`; production was not
deployed or changed. Workspace instruction/skill file samples were unchanged, not effective loaded-input proof.

## Regression Evidence And Remaining Work

The final native long-root regression failed with the old sidecar in 1.873 s. With the fix, normal/long-root Linux
real-library controls passed under race in 4.191 s; the macOS long-root race control passed in 4.143 s. The full
sidecar suite passed 44 Linux tests with one non-Linux skip (26.837 s), and 32 macOS tests with 13 Linux-only skips
(18.432 s). Source-file hashes matched the isolated Linux copy. Broader macOS browser/MCP/isolation tests,
formatting, affected lint, canonical pre-push checks, docs lint and whitespace checks passed; published-head CI
passed separately. No paid/live eval runs in CI and no macOS live or Windows browser qualification is claimed.

Native fixture-navigation/approval admission, authority/context/delivery correlation, protected browser input,
artifact/privacy/cleanup qualification and all six actual browser cases remain. Remaining PDF/effective-input/usage
and exit evidence also remain mandatory; neither this report nor the startup repair completes the full goal.
