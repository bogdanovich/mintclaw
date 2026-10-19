# Website Login discovery: rollout evidence

Status: implementation and deployment qualified; the latest correction was
verified on 2026-10-19 UTC. The owner's
confirmed Facebook login and stored-code use remain pending. The first
owner-approved attempt ended unknown; the follow-up is recorded below. This
records D3 of the [replacement roadmap](credential-discovery-redesign.md);
it does not turn synthetic tests or metadata lookup into evidence of an
authenticated account.

## Reviewed implementation

- D1 merged in [PR #1735](https://github.com/bogdanovich/mintclaw/pull/1735).
- D2 merged in [PR #1739](https://github.com/bogdanovich/mintclaw/pull/1739),
  merge `71929b4aa773bebe598598a7fd059c16fcdf2146`.
- The final implementation head passed all fourteen CI checks and exact-head
  re-review, including the required Codex advisory. The verified visibility
  finding was fixed: controls hidden after observation or preparation cannot
  certify login. The final twenty real Chromium private-form/protocol scenarios
  passed, including hidden ancestors, separate forms and TOTP.
- The credential-focused race matrix and actual OpenAI-compatible/Codex request
  fixtures qualified canary absence from provider requests, canonical history,
  local logs, traces and both export modes. The implementation PR records the
  broader-suite results and the unrelated over-broad local race-run limits.

The advisory initially could not launch because its command contained shell
command substitution. Reading the worktree path first and passing its literal
value allowed the existing helper to run. Neither the exec guard nor the
advisory's read-only/no-tools policy was weakened.

## Deployment and configuration

The operator backed up the existing binaries, systemd units and stopped main
configuration in a private timestamped directory with a SHA-256 manifest, then
fast-forwarded the clean deployed repository to merged main. Core, node and
launcher builds completed, and CLI binaries were installed from those builds.
Only the main gateway was stopped and started for this feature.

The old main process exceeded its existing thirty-second systemd stop limit.
The operator verified zero main/control PIDs and an empty service cgroup before
clearing the failed unit state and copying configuration. An early migration
attempt was rejected while shutdown was still in progress and changed nothing.
The migration then ran only while main was inactive.

The resulting configuration contains one `vault-logins` discovery rule and no
fixed website binding. Its vault, permitted principals, private state directory,
approval origin and remaining settings were preserved. The optional GitHub
connector remains disabled. Protected token and owner-key file identities
(inode, size, modification time, permissions, ownership and link count) were
unchanged; no re-enrollment or token entry was required.

All five active profile configurations loaded in the new binary. Existing
doctor policy findings remained at their baseline counts; this is not a claim
that every existing policy finding is clean. At the initial D3 rollout, the
running main executable's digest matched the built core for the merge above.
Other gateways were not restarted or claimed to be running that new core.

## Live checks

- All ten expected services were active, with no failed units or legacy
  processes. Error-level journals were empty in the checked ten-minute window.
- The existing public HTTPS credentials page returned HTML with HTTP 200.
  The operator enrollment route remained externally denied with HTTP 403.
  A direct plaintext loopback request was correctly denied; it is not the
  public phone-page readiness check.
- The new CLI found exactly one website-matching Login for
  `https://www.facebook.com`: title `facebook.com`, domain `facebook.com`.
  This used overview metadata only. It did not retrieve a full item, password,
  TOTP seed or current code. No UUID was configured for that site.
- A stateless no-tool agent smoke returned its expected marker. Its new
  `mintclaw.diagnostic_trace.v1` trace was
  `trace-turn-1dfde4ea7475bebcf293552c`, source commit matching the merge,
  completed with six records, zero tool calls and no trace truncation. The
  exact smoke input hash was verified privately. The initial raw session-hash
  query did not match the canonical routed session; that query's unrelated
  store scan bounds were not interpreted as evidence of a missing trace.

Rollback material remains in the operator's private timestamped backup. Restore
only the affected binary/config layer while main is stopped; do not restore
mutable browser, grant or conversation state automatically after new writes.

## First owner-approved Facebook attempt

The owner initiated the real login from normal authenticated MintClaw chat and
confirmed the fresh phone passkey approval. Website discovery returned exactly
one matching Login. The private credentials step returned `outcome_unknown`;
subsequent observation could not use the retired worker, and native cleanup
confirmed the context was already closed. No verified-login receipt was issued.
The attempt was not replayed automatically. Its real submission outcome remains
unknown; the synthetic reproduction below cannot prove that no real POST occurred.

Public Facebook-page diagnostics used only synthetic input with network writes
blocked. Its username input/change handlers clear the password. The original
helper filled both controls before dispatching username events, so the password
was cleared before the final value check. Filling one field and finishing its
events before the next field fixed this reproduction. The full synthetic driver
then returned `submitted`; its GraphQL write was intercepted and blocked. This
proves private fill/dispatch behavior, not Facebook authentication.

A second regression added a public cookie flag `1` to the provider fixture.
Registering it as a process-wide secret substring corrupted browser references
and structural numbers. Both real provider adapters failed continuation before
the fix. The cookie guard now excludes only the known boolean literals `0`, `1`,
`true`, and `false`; inventories remain source-suppressed in the private envelope.
Other cookies, including numeric tokens, and every vault credential/code retain
their protection. A credential equal to `1` is still registered normally.

These fixes merged in [PR #1756](https://github.com/bogdanovich/mintclaw/pull/1756),
merge `24243825c82cc916ef3a15c83d3fcb50654633b2`, after all fourteen CI checks
settled green or skipped, clean exact-head review, no unresolved threads and an
owner PR-level rocket. Local qualification passed twenty-two Chromium
form/protocol/navigation tests, full browser and secret-value tests, both
provider adapters under the race detector, Go formatting and full lint. The
provider fixtures exercised private TOTP and verification after the dependent
password reset and public flag cookie.

## Follow-up deployment

The clean deployed main repository was fast-forwarded to the follow-up merge
above. Core, node and launcher were built as candidates and their VCS provenance
verified before installation. All five profile configurations loaded, with
unchanged doctor finding identities/statuses. A private timestamped backup
preserves the previous binaries and systemd units with verified checksums.
Only main was stopped and started. All configuration hashes and protected token
and owner-key file identities were preserved; no credential migration or new
enrollment was needed.

After installation, all ten expected services were active with no failed units,
legacy processes or error-level journal entries in the checked ten-minute
window. The public phone page returned HTML/HTTP 200 and the external operator
enrollment route remained denied/HTTP 403. The running main binary digest
matched the target candidate.

The stateless no-tool smoke produced the exact requested reply and new trace
`trace-turn-d9b8e77113b72f42930d7612`: schema `mintclaw.diagnostic_trace.v1`,
source commit matching the follow-up merge, completed, six records, zero tool
calls and no truncation. Its exact input and final reply hashes were verified.
These are deployment checks, not an authenticated Facebook session.

## Fresh owner pilot still required

Start the real login from the owner's normal authenticated MintClaw chat,
delegating to the browser agent on `gateway/ephemeral`. Discover by the live
Facebook website, submit privately, approve the newly displayed vault scope
with the existing Android passkey if requested, and use the same selected
Login's computed TOTP if Facebook asks for an authenticator code. Observe fresh
account evidence and close the ephemeral browser explicitly.

No operator CLI principal is substituted and the unknown real attempt is not
automatically replayed. Phone approval is confirmed; actual password acceptance,
TOTP presence/acceptance and Facebook authentication are not yet claimed. A
gateway restart invalidates the earlier grant, so the fresh attempt needs a new
phone approval if requested. The owner permits SMS codes in ordinary chat with
provider/history visibility; private stored TOTP remains unchanged. Email,
CAPTCHA and device challenges still need an explicit handoff.

## Two-hour approval and automatic continuation

The external-wait foundation merged in
[PR #1762](https://github.com/bogdanovich/mintclaw/pull/1762), merge
`b7288d335bb60e465bf65bddcaf2304d371bda25`. Browser credential wiring merged in
[PR #1764](https://github.com/bogdanovich/mintclaw/pull/1764), merge
`80f00e37a0a915444fe1a8d65af7f65a402d0adf`. Both passed fourteen CI checks,
clean current-head review, no unresolved threads and owner PR-level rocket
approval before normal merge.

A phone request now lasts two hours. The browser closes with a native cleanup
receipt and the existing interaction registry suspends the task. Verified
passkey approval releases that same task through the existing recovery loop.
Continuation opens a fresh browser, repeats Website discovery and observes new
controls before private filling; it never replays the pre-wait credential call.
The twenty-hour grant starts at approval. Individual passkey ceremonies and
private setup remain five minutes; the page refreshes an expired unattempted
ceremony on click without extending the request deadline.

Qualification covered both actual provider adapters, native Chromium and
virtual WebAuthn: approval later than the old forty-five-second wait, cleanup
before suspension, one continuation with fresh controls, cancellation/expiry,
and a phone page idle beyond five minutes. A failed-notification regression
exercised 130 failures, including caller cancellation, followed by a successful
private approval/login/reuse/revoke flow. Only the original principal's
unattempted request is discarded; an attempted approval or verified grant is
preserved. Focused race tests, formatting and full lint passed; exact-head CI
also passed the Linux suites.

The composed merge above was deployed on 2026-10-18. Core, node and launcher
candidates had verified source provenance. All five profile configurations
loaded with unchanged doctor findings. A timestamped private backup preserves
the previous binaries, units and rollback instructions; all 33 recorded
checksums were verified. Only main was stopped and started. Configuration hashes
and protected token/owner-key identities were unchanged, with no migration or
new enrollment.

All ten services were active after installation, with no failed units, legacy
processes or error-level journal entries in the checked ten-minute window.
The public phone page returned HTML/HTTP 200, its script matched the deployed
source exactly, and external operator enrollment remained denied/HTTP 403.
The stateless no-tool smoke produced the exact requested reply and trace
`trace-turn-67c75fc89de4e5a1c28f6823`: schema
`mintclaw.diagnostic_trace.v1`, source matching the merge, completed, six
records, zero tools and no truncation. Input and final reply hashes were
verified privately.

The owner started a fresh Facebook pilot from normal authenticated chat: task
`delegate-1792364552099-1`, generation
`dc5b8dc1-3397-43b8-a24d-c871bb0695ad`. Origin trace
`trace-turn-3bb467a1c5824ffb603d6d11` suspended. The external wait retained a
native closed-browser receipt and became ready after phone approval. Its
authoritative interaction recorded exactly one resume. Continuation trace
`trace-turn-8d3d1540ce48c1a8e6f0a0af` opened a fresh browser and returned
`submitted` for the credentials step (record 57). Facebook then presented its
authenticator-code challenge. The TOTP step returned `denied` (record 107), and
fresh observation did not confirm authentication. Native cleanup closed the
browser (record 128). The task ended failed; the wait itself resolved ready.

Both traces have source `80f00e37a`, schema `mintclaw.diagnostic_trace.v1`,
redacted-content capture with `mintclaw.config_filter.v1` and no truncation.
This qualifies the physical approval/continuation path, not TOTP acceptance or
Facebook authentication. The denial is being investigated without replaying
the real submission. The
updated [private explainer](https://mintclaw-credentials-explained.freecedar10.chatgpt.site)
shows the two-hour wait with rendered diagrams and preserves owner-only access.

## Submit controls enabled by private input

The pilot denial was investigated without replaying its real submission.
The generic readiness check required an enabled submit control before any
private input, while the observed Facebook challenge's Continue control was
disabled with an empty code field. A native synthetic regression reproduced
that rejection. This is evidence for a compatibility defect, not a more
specific private error code from the real attempt.

[PR #1776](https://github.com/bogdanovich/mintclaw/pull/1776) now permits
preparation of a visible initially disabled submit control when the selected
inputs, native elements and destination are already valid. Private filling
waits within its bounded operation for the site's own validation to enable
that same control. It never removes the site's disabled state. Input values,
visibility, enabled inputs, document and destination remain checked. Expiry
is checked before accepting enablement; an invalid or expired attempted step
closes the session without automatic replay.

Review found a late-enable deadline-ordering defect. A real Chromium regression
reproduced submission after a delayed site callback exceeded the deadline.
The correction requires zero posts and context closure. After resolving a
concurrent main refactor, both private login paths retain one shared sequential
field/event/submit helper. All 28 pinned Chromium form/protocol/navigation
tests passed. Current-head Linux CI also passed every credential qualification
step, including actual provider output paths, native alias/discovery/TOTP and
delayed phone continuation. The merged-base macOS gateway race executable
stalled in `_dyld_start` before Go startup and was stopped; that attempt is not
counted as passing. Formatting, full lint with unchanged rules and documentation
validation passed.

The final head `2f166acaa32b39ebb4a28039e5590a48eb3c98c2` had thirteen
successful CI checks and one skipped check, clean current-head review, no
unresolved threads and owner PR-level rocket approval. Normal merge produced
`51426f6ac5e6ad9ee6cc977b1012834ee572af66`.

Deployment fast-forwarded the existing clean main checkout, preserving another
operator's intervening deployment. Core, node and launcher candidates had
verified provenance. All five configurations loaded with unchanged doctor
findings; configuration hashes and protected token/owner-key identities were
preserved. Only main was stopped and started. The private timestamped backup
contains rollback instructions and 33 verified checksum entries. Do not restore
mutable browser, grant, interaction or conversation state automatically after
new writes.

All ten services were active, with zero failed units, legacy processes and
error-level journal entries in the checked ten-minute window. The public phone
page returned HTML/HTTP 200 with the expected script; external operator
enrollment remained denied/HTTP 403. The stateless no-tool smoke produced its
exact reply and trace `trace-turn-b4b6787cee74f65dd59f5e1b`: schema
`mintclaw.diagnostic_trace.v1`, source matching the merge, completed, six
records, zero tools, no truncation and verified input/reply hashes.

A fresh owner-initiated Facebook pilot is still required. Deployment and
synthetic TOTP acceptance do not certify the real account. The gateway restart
invalidated earlier grants; the new attempt may require a fresh phone approval
within the two-hour window. A code provided after the previous browser closed
cannot complete that retired session. SMS remains an explicitly permitted
ordinary-chat exception, separately unqualified; private stored TOTP handling
is unchanged.


## Live continuation context after approval

The next owner pilot on `51426f6a`, task `delegate-1792370674148-1`, generation
`c7788a31-a1b5-4b5b-a22d-2be5f8cbc8dd`, again received phone approval and
resumed exactly once. The originating trace
`trace-turn-24717f53c6c3d5bdf54134db` suspended after closing its idle browser.
The continuation `trace-turn-8530ccbf90b2930a58765d7b` only checked that old
session (records 5/7: status/closed) and closed it again (12/15: close/already
closed). It opened no new browser and read or submitted no new credential
values. The task failed before a new form or second-factor challenge.

Both traces used `mintclaw.diagnostic_trace.v1`, the matching source commit,
`redacted_content` / `config_filter.v1`, and no truncation or dropped records.
The originating and continuation traces had 65 and 20 records respectively.
Prompt assembly discarded non-stable overlays when reusing the frozen root
context, which removed the current ready/fresh-resource guidance. The frozen
regression reproduced that loss; the actual provider requests had empty dynamic
system/tail fingerprints.

[PR #1792](https://github.com/bogdanovich/mintclaw/pull/1792), merge
`77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda`, separates current execution facts
from root-owned context. Ready/decision guidance and verified receipt IDs
extend the transcript as uncached runtime context; every previously sent
message, the original envelope, goal and admission time remain unchanged. The
live facts do not enter canonical history or create credential authority.
There is no automatic replay of the paused browser call: the agent must open
fresh resources, observe the current page and use the ordinary protected tools.

The reviewed head `6f3e05c88b7a244147a48b619bb44615423832b2` had ten successful
CI checks, two intentionally skipped checks, clean exact-head review, no
unresolved threads and owner PR-level rocket approval. All five private
credential gates passed in integration job `117862280188`, including actual
OpenAI-compatible/Codex requests, native Chromium/TOTP and delayed approval
with fresh continuation. Native coding and remote handoff retain their strict
historical-prefix assertions. Real companion command, file-transfer and durable
job scenarios also passed locally. Formatting, unchanged-rule full lint and
documentation validation passed. Local pre-Go macOS loader stalls were not
counted as executed tests; current-head CI completed the required suites.

The merged correction was installed and verified on 2026-10-19 UTC. Core,
node and launcher candidates matched the merged source and had clean VCS
provenance. All five configurations loaded with unchanged doctor findings;
configuration hashes and the protected token/owner-key identities were
preserved. The previous runtime was `cf28ed1a071c797fc03aee110ab7adb837cf4937`,
including another operator's updates. Only main was stopped and started.

The first attempt restored the previous binaries after an early process
verification failed. The retry used bounded verification of the exact running
candidate, waited for registered owner tasks to finish, and succeeded. Both
attempt backups remain; no mutable task, conversation, browser, grant or
credential data was restored. The verified backup has 33 matching checksum
entries and recorded instructions for rollback of binaries only.

All ten services were active, with zero failed units, legacy processes and
error-level journal entries in the checked ten-minute window. The public
approval root returned HTML/HTTP 200 and its script matched the exact deployed
source; external operator enrollment remained denied/HTTP 403. The no-tool
stateless smoke produced trace `trace-turn-a4aea5b2f313b6338eacbc5d`: schema
`mintclaw.diagnostic_trace.v1`, matching source, `redacted_content` /
`mintclaw.config_filter.v1`, completed, six records, zero tools, no truncation
and exact input/reply hashes.

The real Facebook authentication gate remains open. A fresh command must come
through the owner's normal MintClaw chat; the operator does not impersonate
that principal or replay the failed attempt. Restart invalidated previous
grants, so a new phone approval may be needed within the two-hour window. The
twenty-hour grant still begins at approval. Deployment and synthetic TOTP
acceptance do not certify the real account.

## Confirmed Facebook login and its public receipt

The next owner command on `77c79cd5` ran in the normal authenticated chat,
task `subagent-50c5d148-8239-4a73-86b4-e3d0e0155ecb`, generation
`fd6e57ce-4a39-47fd-9218-7581e395582a`. Origin trace
`trace-turn-8b45afd537769111c1d0b877` suspended after closing the idle browser.
Phone approval resolved the wait, with exactly one continuation. Trace
`trace-turn-90dbe1f3de5128398f244476` opened a fresh browser, found the Login
by Website, privately submitted credentials and the requested stored TOTP,
then verified `logged_in` against a fresh authenticated control. Facebook
showed its logged-in page and Log out. Native browser closure returned closed.

The traces contain 65 and 120 records, matching source `77c79cd5`, schema
`mintclaw.diagnostic_trace.v1`, `redacted_content` /
`mintclaw.config_filter.v1`, and no truncation. In the continuation, protected
credentials returned submitted at record 43, TOTP submitted at 85, fresh
verification logged_in at 106, and native closure closed at 113. This is real
authentication and stored-TOTP evidence; it is not evidence for SMS handoff.

The delivered task nevertheless ended failed with a partial objective report:
the model explicitly reported that `browser_login` had not supplied the
confirmation receipt ID. The tool already created a successful trusted login
audit, but its public result hid that audit's opaque ID. Runtime recovery
deliberately preserves an honest partial report instead of promoting it.
The historical task and its report remain unchanged; no real submission is
replayed to repair that report.

The correction adds `receipt_id` to confirmed `logged_in` tool results, using
the same runtime-generated ID as the existing trusted audit. It conveys no
credential authority or secret values. All other outcomes remain receipt-free;
the model cannot supply its own receipt ID, and submission alone still cannot
certify authentication. Objective validation and partial/blocked handling are
unchanged. Tool regressions require exact public/audit ID equality, and the
actual OpenAI-compatible/Codex website fixtures require a visible ID after
fresh verification, including delayed phone continuation.

At this stage the receipt correction still requires reviewed merge, deployment
and a fresh normal-chat task that completes its formal report. The successful
real authentication above does not by itself qualify that reporting fix.
