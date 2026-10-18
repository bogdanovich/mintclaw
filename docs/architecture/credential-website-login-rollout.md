# Website Login discovery: rollout evidence

Status: implementation and deployment qualified on 2026-10-18. The owner's
physical Facebook login and stored-code use remain pending. This records D3 of
the [replacement roadmap](credential-discovery-redesign.md); it does not turn
synthetic tests or metadata lookup into evidence of an authenticated account.

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
that every existing policy finding is clean. The running main executable's
digest matches the built core for the merge above. Other gateways were not
restarted or claimed to be running that new core.

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

## Owner pilot still required

Start the real login from the owner's normal authenticated MintClaw chat,
delegating to the browser agent on `gateway/ephemeral`. Discover by the live
Facebook website, submit privately, approve the newly displayed vault scope
with the existing Android passkey if requested, and use the same selected
Login's computed TOTP if Facebook asks for an authenticator code. Observe fresh
account evidence and close the ephemeral browser explicitly.

The pilot command has been supplied to the owner. No operator CLI principal was
substituted and no real login was replayed. A physical approval, actual password
acceptance, TOTP presence/acceptance and Facebook authentication are not yet
claimed. SMS/email, CAPTCHA and device challenges still need a safe handoff.
