# Manual Agent Evals: PDF Output V1 Pilot

## Outcome

On 2026-10-18, exactly one fresh-profile `pdf.loan.fill` attempt on merged
[PR #1741](https://github.com/bogdanovich/mintclaw/pull/1741) completed with **inconclusive**, CLI exit 1,
`timeout_remote_unknown`, in **182,138 ms**. The first ordinary question received a user answer, but its native
continuation recorded HTTP 400 with a missing Gemini `thought_signature`. No target task was retried.

The new finalized output assessment was not reached. There was no observed protected form job, write operation,
delivered output PDF or independent artifact result. Read/inspection-worker activity is not independently qualified
by absence of a form job. This run does not qualify filled/private-value PDFs or complete the
[E0-E5 goal](../architecture/agent-evals-goal.md).

## Code And Preparation

Reviewed head `a33cf27b9543c8f365de1bb1c701cac5e8f47bee` merged as
`3a959e65082c63063dc5d5b8a9a034f1529383a4` at `2026-10-18T10:06:50Z`.
Exact-head CI run `39116206251` passed ten checks with two policy-selected skips. Review comment `6248487491`
reported no high-confidence issues on that head. Owner PR-description rocket `577208571`, clean mergeability,
current green checks and no unresolved/new actionable feedback were verified before matching-head merge.

The isolated Linux amd64 binary was prebuilt from the identified PR source, then used only after approved merge.
Go 1.26.9, CGO enabled, `goolm,stdjson`; version `eval-pdf-output-v1-a33cf27b9`, build time
`2026-10-18T09:42:12Z`. With `-buildvcs=false`, SHA/version/time were injected explicitly, not VCS-attested.
This identifies the now-merged PR head, not every concurrent main change.

- Binary SHA-256: `98b5f071c7ef6a0da7eae86916760f5440a882981c352d10f547537eb06e9537`.
- Source archive SHA-256: `ba51fa9d2d81b78b456afb0596f9a3e400cbfa5f65aeeaf184c6d92884862bf9`.
- Public prepared-config SHA-256: `8504c44e6f05a4ba04932f021dce5d44ebce2314080060c5d5d74fc9fa36fc29`.
- Admitted runtime-config SHA-256: `121035a795386637dfc6036438fbdceb9f73bd75c9d951262a9b6027fc7f15b6`.
- Case SHA-256: `1e036844dc5047e71c0474f54a4cf629393221bf5cde2294ca53f27c455f4e5c`.
- Source PDF SHA-256: `f9100e47a0d3346cf2e7edebce927ffcaa43b3ee4c164186f781b08b93fc94ec`.

Offline validation admitted all six PDF manifests. The selected task/persona/expected fields were unchanged.
A new isolated seed reused one previously authorized API credential and selected public model settings from deployed
main. Native `eval prepare` then created a new profile/channel token, copied one allowlisted synthetic `AGENTS.md`
and prepared an empty private native write journal; it returned `prepared / runtime_run: false` before execution.
No production sessions, memory, ledgers, OAuth or browser state were copied. No production binary, service or
configuration was changed by this eval operation. The five owned credential copies were later removed as described below.

Read-only production health checks before/after found all ten expected units active and no error-level journal
entries in their ten-minute windows. A separate concurrent core advance from `95c58f41b` to `71929b4aa` was observed;
it did not replace the isolated executable/profile identified above. This pilot does not establish whether the
same target defect exists on that later deployed revision.

Requested models were `gemini-3.5-flash-lite` for target/user and `gemini-3.6-flash` for audit/judge. These are configured
request identities, not independently confirmed effective models. The profile admitted only document/question tools
under deny-default policy. No broad or preexisting protected-write approval was granted.

## Observed Execution

Run `c1bd838c-0c48-4770-95eb-d1e7c9af67f7`, trial `5f805529-5822-4903-864d-c6771b86b135`:
complete report, one inconclusive trial, started `2026-10-18T10:07:48.744163838Z`. Trial duration **181,966 ms**;
run duration excludes cold build/preparation and gateway startup before admission. The unchanged case budget was
ten minutes with three-minute turn waits; the separately declared suite bound was twelve minutes.

| Boundary | Observation |
| --- | --- |
| Conversation | Initial input, ordinary question, one follow-up input; no subsequent observed turn outcome |
| LLM user | One returned call, 805 ms; `721` input / `24` output / `745` total tokens |
| User privacy boundary | Actual typed input/output observed; `run_retained_facts` scope |
| Ordinary native record | Answer present, `resuming`, one resume try, a 370-byte retained resume error |
| Resume error classification | HTTP 400 and missing `thought_signature`; rate-limit/auth/model-missing predicates false |
| Protected form/write | No protected form-job snapshot observed; no settlement or output artifact evidence |
| Native census | Zero writes/media intents/document deliveries/pending operations/foreign changes; source unchanged |
| Client media window | Complete, zero observed/matching media messages; no positive delivery binding |
| Owned child | PID `2481086`, admitted, stopped/reaped exit 0, not forced, log not truncated |
| Post-stop | Logical settlement unavailable; owned source retained, no output assessment scope |
| Hard checks/judge | All four required PDF checks inconclusive; judge not called, evidence ineligible |

The native census counts cover write-journal/outbox state, not the question registry. Zero pending operations there
does not settle the still-resuming ordinary question or imply cancellation. Independent PID and ownership-marker
absence checks passed. The original source and one owned trial directory remain private because logical settlement
was not established; nothing was recursively deleted to manufacture cleanup success.

Target/native-audit all-call usage and provider-internal attempts remain unknown. One user call is not the total
runtime/model-call count. User prompt version was `eval-pdf-user-v1`; no model labels were present in the observed
reply events. No total cost is inferred. Offline regrade and comparison were not attempted; private/output
regrade remains intentionally unadmitted, and no task was restarted to obtain another result.

## Target Defect Triage

The follow-up was accepted into the native ordinary-question record, so this is not evidence of an API-key failure
or a user simulator failing to answer. Its retained native resume error classified as HTTP 400 with a missing
`thought_signature`. No raw provider error body, provider signature, question text or private answer is published.
The exact point at which required metadata was lost or omitted is **not established** by these observations.

Triage this separately in native ordinary-question continuation/provider history handling, rather than changing eval
expectations, lowering timeouts, bypassing confirmation or replaying the failed session. A focused deterministic
regression should suspend a tool call carrying an opaque provider signature, accept its prompt-bound answer, and
inspect the actual resumed provider request. Preserve required provider metadata without exposing it through
diagnostics or evaluator/model projections. Check whether a non-retryable provider rejection can surface promptly
instead of leaving the client waiting; log-line counts alone do not prove provider call/retry counts.

GitHub issues are disabled for this repository, so this report preserves the target-defect triage. Fixing all target
behavior is not an open-ended extension of the eval goal. A later provider-compatible experiment needs a fresh
profile and a separately declared attempt; this retained used home must not be resumed or upgraded into a pass.

## Privacy And Retention

The native `owned_profile_files_v1` observation inspected **50 files / 576,129 bytes**, excluded two authorized
credential/key roots, recognized one pinned source PDF and 26 exact compiled public system files. It found no known
leak but retained **one opaque file**, so coverage stayed `unavailable`, not pass. The captured gateway log was
109,459 bytes of Unicode/long-line text; attribution of the opaque receipt to a specific surface is not established
here. No unknown encoding or opaque file was silently exempted.

The scoped native SQLite witness was observed: **36 rows / 222 text values / 15 index values / 17,603 value bytes**
under `seahorse_text_and_raw_json_escapes_v1`. This does not broaden the other file/encoding bounds, prove all runtime
surfaces private, or certify private-field containment on a completed PDF path.

Before credential-copy removal, a separate bounded operational literal guard inspected **70 files / 597,812 bytes**
across the seed, prepared profile and captured seed/prepare/run outputs, allowing exactly five copy paths and finding
no known credential literals elsewhere. Its entry/file/aggregate limits and symlink/special-file refusals remained
in effect. This is credential-literal evidence only, not encoded/derived-value, fact-canary or universal privacy proof.
Run stderr was 54 bytes; seed/prepare stderr were empty. Private captures are retained outside Git.

After shutdown/absence checks and a second unchanged guard receipt, exactly the seed API/channel copies and the
prepared profile's two model-key copies/channel-token copy were removed. All five absence checks passed; production
originals were untouched. Retained homes now have unresolved credential references and are not fresh/runnable targets.
Native state, raw logs, canonical/pending reports, fixture and build/archive remain private; no secure erasure or
whole-profile cleanup is claimed.

Workspace file observations were unchanged before/after: instructions
`a7eb032ae98bd58278d1661a0fb83d07ef02bdf8f74520729b1d624ff87b74a0`, skills
`17ebc71f2342352898acf9fe6b097d4874d6045c9c4e1d065623602e86b87541`. The pinned `AGENTS.md` SHA-256 was
`d13fd1223b97a0b346da8f639ff6804631ff08f8474d91debb9aa3a94eb01e38`. These are file samples, not proof of effective
loaded prompts/catalogs or all per-trial inputs.

## Validation And Remaining Work

Root formatting, affected lint (zero issues), canonical pre-push validation, documentation lint and whitespace checks
passed. Final-head focused macOS race tests passed in 13.363 s; Linux race controls passed in 26.462 s with all six
changed Go file hashes matching the commit. Broader macOS race tests passed eval 349.413 s, client 2.303 s, eval CLI
13.860 s and agent CLI 11.784 s; its only later production change was an equivalent variable declaration accepted by
lint. The published head was separately covered by focused tests and full CI.

The real pinned Python oracle passed nine controls in 15.294 s. The Go authenticated-stub collector/retention plus
real oracle control passed under Linux race in 3.066 s. Capability declarations admitted pdfcpu v0.15.0 fields and
fill/verify with PDFium WASM 8044, wazero v1.12.0 and Poppler 24.02.0. These controls/availability declarations do not
replace the missing live filled-PDF/worker evidence. No macOS live or Windows qualification is claimed.

The goal remains active. Filled/private-value PDF evidence, remaining actual PDF cases, native browser execution/all
six browser cases, effective per-trial inputs, available all-call usage and final exit evidence remain. The earlier
passing no-write path and all earlier failed/inconclusive reports remain unchanged.
