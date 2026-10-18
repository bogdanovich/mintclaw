# Manual Agent Evals: Browser Artifact V1 Pilot

## Outcome

One fresh isolated `browser.fixture.artifact` attempt on 2026-10-18 returned **inconclusive**, CLI exit 1,
in **10,852 ms** after admission. The agent prepared a `click` on the report link, not the scoped native
`download` with delivery. The runner refused to send `allow_once`. The fixture recorded one visit and zero
downloads, submissions or removals. No CSV, final reply or judge call was observed.

This is fail-closed evidence, not positive artifact qualification. The task was not retried and approval policy
was not widened. Earlier experiments, including the [qualified read](manual-agent-evals-browser-read-v4-pilot-20261018.md),
remain unchanged. The [full E0-E5 goal](../architecture/agent-evals-goal.md) remains active.

## Code And Admission

[PR #1761](https://github.com/bogdanovich/mintclaw/pull/1761), reviewed head
`9dfca28bce6b1a349e712024822dfb065561c5bf`, merged as `372b675933ed416762f84ba21b72cf16f73698c2`
at `2026-10-18T21:26:37Z`. All 14 exact-head CI checks passed in run `39165767243`, including full macOS
portability packages. SHA-bound reviewer writeback `6255242722` at `21:16:11Z` reported no remaining must-fix
findings. Owner PR-description rocket `578022872` at `21:16:12Z`, green CI, clean mergeability and no new
actionable feedback were checked before matching-head merge.

Review found an offline-grade nil dereference when browser evidence was absent. The guard and read/artifact
negative controls were added. A real raw-profile control also exposed an opaque native signer key: the fix
classifies only the exact owner-only 32-byte key that already verified a prepared-action HMAC. Home/path/digest
must match; literal/JSON-escape leak guards run first. Other opaque files and missing/changed/conflicting keys
remain unavailable. No generic binary whitelist or new authority was introduced.

The branch incorporated #1683's typed interaction-origin fix. The live binary used the final merge tree, including
#1759's final-answer repair, not an older candidate. Linux amd64, Go 1.26.9, CGO enabled, `goolm,stdjson`, version
`eval-browser-artifact-v1`, build time `2026-10-18T21:27:48Z`. The archive and injected revision identify the source;
they are not independent VCS build attestation.

- Binary SHA-256: `b7b5863899afde15ec2c29a161b5a7a4b936df9e42fc9fc683549f845bd24ff0`.
- Sidecar SHA-256: `9809546285a191ce0e64501486b9df47efd86907bd364ff483bc9d5cdd52802a`.
- Browser template SHA-256: `19348a11765e4e81f4aeaf13a0150489773c2537cf79d2e81b89b63495f2f681`.
- Public-config SHA-256: `da7c54090b7ce6250cd87069f7ccf2c0f983cdc201be020ad8ce709b36b80a18`.
- Case SHA-256: `62846259ef544aebbce30a447eeec821a465c497d7bc8c76027ea2ed851c246e`.

All 14 manifests validated offline. Native preparation created a new private profile, synthetic allowlisted
instruction file, empty browser roots and fresh channel token. Only authorized deployed-main model settings and
one scoped credential were reused; no production sessions/memory/browser/OAuth state was cloned. Chrome
155.0.8059.39, Node 22.23.3 and Playwright 1.63.0 were verified. Suite/case/turn budgets were 14/10/3 minutes;
computer capability stayed disabled.

## Execution

Run `c8341ec3-eeae-4e50-9aa1-72413aef9c8e`, trial `9b439993-947d-49ad-acfb-91a5c0b302fe`,
session `6664444b-a7a1-4574-81c5-a3f4c923de35`; CLI phase `21:30:06Z` to `21:30:18Z`.
The complete report retained trial duration **10,387 ms**, code `domain_evidence_unavailable`.
Duration excludes cold build, preparation and owned startup before admission.

| Boundary | Observation |
| --- | --- |
| Conversation | One input, one approval prompt, zero final replies; admitted route unchanged |
| Target configuration | `gemini-3.5-flash-lite`; not all provider attempts |
| Fixture | One visit, zero downloads/submissions/removals; sealed, host closed/joined |
| Action | `click`, declared effect `read`, policy effect `unknown`, decision `ask` |
| Public element | Link named `Download fixture report` |
| Approval | Waiting record, no consumed approval, no runner grant |
| Native work | Navigation succeeded; unaccepted click canceled with `session_closed` |
| Resources | One closed session; browser-ephemeral and gateway-temp roots independently empty |
| Artifact/checks | Missing CSV; all six required domain checks inconclusive |
| Other roles | Scripted user; configured `gemini-3.6-flash` judge; zero calls to either role |
| Usage | Target all-call usage unknown; no final-event usage or invented totals |
| Lifecycle | PID `3217388`, native exit 0, not forced, untruncated capture; independently absent |

The tool description names the dedicated download action. The observed click does not establish a native
download implementation failure or, without further exposure evidence, why the model chose that route.
A link name alone does not qualify a deliverable download. No alternate-model attempt or task replay ran.

Subsequent passive inspection of the retained native JSONL showed `browser_targets` call `call_2029771` explicitly
returned `download` in its action list and `features.download: true`. Final call `call_883870` selected click/read,
with no delivery field. All six persisted assistant call records named `gemini-3.5-flash-lite`; this is not backend
attempt enumeration. Protected tool results were omitted from durable JSONL, and trace capture was disabled, so no
retained provider-request schema proves every transmitted branch/field. Exposed capability is evidenced; the exact
reason for the model's choice is not. No policy or historical assessment changed after that inspection.

## Privacy And Cleanup

Raw-profile coverage stayed **unavailable**: 53 files, 387,549 bytes, one opaque file, 26 compiled public-system
files and one excluded credential directory; no known leak. SQLite qualified 94 rows, 384 text values, 28 index
values and 40,575 value bytes. No authorized signer-key count was issued because download admission failed.
A retained signer file was 32 bytes, mode 0600. This report does not reclassify that opaque receipt or claim full privacy.

The independent literal copy guard inspected 70 files/413,819 bytes, found exactly five authorized credential
copies and no occurrence elsewhere in its scope. All five copies were removed and the prepared secret directory
was independently empty. Production config/security hashes were unchanged. This guard does not replace raw-profile
privacy. No harness deletion manufactured native cleanup.

All ten production services stayed active, with zero failed units and zero error-level records in the observed
ten-minute windows. Production core stayed on `c497347b8227b8489725735aaca5fea8f837d0b1`; no deployment occurred.
Private reports remain under `/home/server/mintclaw-eval-browser-artifact-20261018.hzNEKgvr`, with unresolved
credential references by design. Do not restart that profile or retry its task. Ineligible evidence was not regraded;
no positive same-case baseline is claimed.

## Validation And Remaining Work

Artifact controls passed with race detection on Linux/macOS. Follow-up Linux artifact, both grades, privacy/key
and existing PDF-wire controls passed in 104.218 seconds. Rebased macOS controls and native lifecycle race controls
passed; formatting, affected lint, docs lint and final exact-head CI were green.

Broader local failures remain recorded in the PR: macOS eval and Linux eval/tools reached unchanged ten-minute
package deadlines; three hook race failures reproduced on clean main. Pre-main macOS loader diagnostics were
terminated, not counted as passes. No timeout, assertion or CI/approval gate was weakened.

Next: inspect supported-tool exposure and the click-versus-download choice, without replay or broader grants.
Any justified correction needs regression tests, review/merge and a separately identified experiment. Positive
artifact qualification, four other browser cases, five PDF cases, adaptive work, effective loaded inputs, usage
and remaining E5/exit work remain mandatory within the frozen goal.
