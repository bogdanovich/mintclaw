# Manual Agent Evals: Browser Read V1 Pilot

## Outcome

On 2026-10-18, exactly one fresh-profile `browser.fixture.read` attempt on merged
[PR #1746](https://github.com/bogdanovich/mintclaw/pull/1746) returned **inconclusive**, CLI exit 1,
`required_check_unknown`, in **9,138 ms** after admission. The two-turn script completed, but the native browser
session failed to open with `driver_unavailable`. The independent fixture recorded **zero page visits**. Neither
plausible response text nor script completion became a browser pass or a judge call. No task was retried.

A separate model-free startup diagnosis reproduced Chrome's **Socket path too long** failure with a long private
temporary path. The same Chrome/sidecar started and closed with a short private temporary directory. This identifies
a native temporary-path portability defect, not successful browser-eval qualification. The
[E0-E5 goal](../architecture/agent-evals-goal.md) remains active.

## Code And Preparation

Reviewed head `f9d49f46aecc4a8b3393e88f56003b2d2e268722` merged as
`f5491003b9b6bc9adedf98af1f339362db86d679` at `2026-10-18T13:36:00Z`. Exact-head CI run `39130268982`
passed all 14 checks. Actual reviewer writeback `6250460270` reported no actionable findings on that SHA; three
documentation files were excluded from deep review. Owner PR-description rocket `577436557`, green current-head
checks, clean mergeability and no unresolved/new actionable feedback were verified before matching-head merge.

The isolated binary was built before merge and executed only after merge confirmation. Its source tree matched
the merge commit; version/SHA/time were injected explicitly, not independently VCS-attested. Platform: Linux amd64,
Go 1.26.9, CGO enabled, tags `goolm,stdjson`; version `eval-browser-owned-v1`, build time `2026-10-18T13:08:00Z`.

- Binary SHA-256: `930de766be83890ca8fd90a04ae10caf2e346deba5aa73fa3bc5248221465e0a`.
- Sidecar SHA-256: `323e6459b6ba051dd2769a46c9253807674a0968e8ac0bc1b609b958f4adb5f1`.
- Sidecar lockfile SHA-256: `038af1b8eb44fff43b762cbe28714475a674aa1abc033e55820478621b0db7ee`.
- Admitted runtime-config SHA-256: `764b5dec576aa39bd347fdbc96f471de8e45f97a38cc043d83f41bcea023a40c`.
- Case SHA-256: `79de150ac381ae3ee5953a4c8c0b7c5c1e61e42c3dbc7cd2efb48a527c6c5f25`.

The binary validated all 14 manifests offline. A new isolated seed narrowly reused the previously authorized
deployed-main API credential/public model settings; it copied no production sessions, memory, ledgers, OAuth or
browser state. `eval prepare` returned `prepared`, `runtime_run: false`, `browser_prepared: true`, with a new profile,
channel token, one allowlisted synthetic `AGENTS.md`, empty ephemeral/lock directories and rebased fixture origin.
The task, expectations and three-minute turn/ten-minute case bounds were unchanged; the suite limit was six minutes.

Requested target/user model: `gemini-3.5-flash-lite`; configured judge: `gemini-3.6-flash`. The two observed final
events carried the target model label; these are observed metadata/request identities, not provider attestation.
Only the narrow browser/question tool policy was admitted. No `allow_once` or mutating operation was authorized.

## Observed Execution

Run `6f311420-e924-484d-b2c8-2d5ef5a08433`, trial `cc59e82f-8b87-416e-bb38-1a333105496b`,
started `2026-10-18T13:36:35.465417744Z`; complete report, one inconclusive trial, trial duration **9,004 ms**.
Run timing excludes cold build, preparation and gateway startup before admission.

| Boundary | Observation |
| --- | --- |
| Conversation | Two inputs and two final responses in one fresh session; `script_completed`, transport closed |
| Native tool log | One recorded `browser_session` failure, `driver_unavailable`, 4,511 ms |
| Sidecar | Owned process started, then exited 1; its private stderr was not retained by the normal runtime |
| Independent fixture | Zero visits, downloads, submissions or removals; sealed healthy local witness |
| Local predicates | No fixture effects observed; read inconclusive, `read_evidence_missing` |
| Native hard checks | State, authority, no-write, privacy and cleanup all inconclusive |
| Judge/user | Zero calls; browser evidence ineligible for judging or offline regrade |
| Gateway child | PID `2642410`, admitted, stopped/reaped exit 0, not forced, log not truncated |
| Fixture listener | Joined and closed after gateway stop; one retained trial, HTTP-host-only receipt |

The agent truthfully reported it could not retrieve the reference. A local no-effect predicate does not establish
native no-write/cleanup authority. Independent post-stop checks found the admitted PID absent and no process whose
command line named this profile. Empty ephemeral scratch and retained lock files are not whole-runtime cleanup proof.

Self-comparison completed with exit 0, compatible cases, one retained inconclusive trial and no environment changes.
This checks report readability/count preservation, not an independent baseline or quality improvement. Browser
regrading remains intentionally unadmitted. Target all-call tokens/provider attempts remain unknown; token-like
footer text was not parsed into authoritative usage or a total cost.

## Startup Defect Triage

The existing real-library loopback transport/cleanup test also failed at native factory open on this Linux host
in **1.808 s**, without an LLM or the eval-runner. No earlier result was replaced by this diagnostic.

A separate blank-profile `Driver.start` probe used the same sidecar, Node `v22.23.3` and installed Google Chrome
`155.0.8059.39`, a credential-free environment and a dead loopback proxy. Its private error identified Chrome's
process-singleton Unix socket path as too long under the long private `TMPDIR`. A second explicit diagnostic changed
only the temporary-path allocation to a short fresh private directory: startup/close succeeded and that directory
was removed. Neither diagnostic visited the eval fixture or ran a model/task.

The native ephemeral driver sets `TMPDIR` and `PWTEST_SOCKETS_DIR` to its session lease path. Deep eval homes make
that path still longer. The original sidecar stderr was deliberately discarded, so the exact original Chrome error
cannot be recovered; the configuration/code path and independent long/short control support this root-cause
attribution. Do not label the failure as missing credentials, a provider timeout or a successful page read.

Next prerequisite: bounded native temporary-path handling that supports Unix socket limits while retaining private
session ownership, process-tree stop, crash recovery and cleanup. Keep the failed report unchanged. A later live
experiment needs newly identified merged code, a fresh profile and an explicit attempt, not this used session.

## Privacy, Cleanup And Limits

Post-stop `owned_profile_files_v1` scanned **54 files / 413,690 bytes**, excluded one authorized credential directory
and recognized 26 exact compiled public system files. No known leak was observed, but **four opaque files** retained
`unavailable` coverage. The native SQLite witness observed 101 rows, 324 text values, 48 index values and 61,638 value
bytes under `seahorse_text_and_raw_json_escapes_v1`; it does not remove the other opaque/encoding limits.

Before credential removal, the separate bounded operational literal guard inspected **71 files / 439,390 bytes**
across the seed, prepared profile and captured seed/prepare/run outputs, allowing exactly five owned credential-copy
paths and finding no known credential literals elsewhere. This is scoped literal evidence, not encoded/derived-value
or whole-runtime privacy proof. The two seed copies and three prepared-profile copies were then removed; all five
absence checks passed. Production config/security source hashes were unchanged. Retained profiles now have unresolved
credential references and are not fresh/runnable targets. Raw state, logs and reports remain private outside Git.

Production health before/after showed all ten expected units active, no failed units and no error-level journal
entries in the ten-minute windows. Deployed core remained `71929b4aa773bebe598598a7fd059c16fcdf2146`; no production
binary, service or configuration was changed. Workspace instruction/skill file samples were unchanged; these do not
prove effective loaded prompts/catalogs.

Local macOS race suites passed eval/client/CLI in 426.355 s / 2.347 s / 13.840 s; the final narrow diagnostic-check
separation was then covered by focused race tests in 26.946 s. Root formatting, affected lint, canonical pre-push lint,
docs lint and whitespace checks passed. Full published-head CI passed separately. No macOS live or Windows browser
qualification is claimed. All six browser cases, remaining PDF qualification, effective inputs/all-call usage and
the final exit evidence remain mandatory unfinished goal work; earlier passing/failed/inconclusive reports remain.
