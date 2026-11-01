# Human assistance for browser challenges

Use the existing browser, screenshot delivery and durable question mechanisms.
No CAPTCHA solver, extra credential service or new browser-control endpoint is
introduced. A Security Measure page is not necessarily an image-code CAPTCHA;
show the available evidence rather than inventing an answer field.

For account login, discover the saved Login on the current official website and
complete `browser_login authorize` before the owner's manual challenge. This
operation needs no login form or private values. Automatic access returns
`authorized` for a configured principal; phone mode can suspend and close the
idle browser before any human effort is invested. After manual challenge work,
resume that same browser through protected login and verification.

Start from a working public homepage and follow its observed sign-in link.
Check the page itself, since HTTP 200 can still be a security challenge. A
failed public GET, including a plain 403/Error Page, follows this default
sequence before manual help: deliver the obstacle image when permitted, allow
about three seconds for settling, observe, repeat the exact public URL once in
the same session if still blocked, then observe the result. Preserve response
cookies, tab, profile and network identity. If the page is available, continue;
otherwise show the remaining obstacle and hand off that same visible browser.
An explicit immediate-handoff/no-retry request takes precedence. Screenshot-first
alone does not skip the bounded recovery. A failed protected deep link instead
uses the existing origin/authentication recovery; do not combine both budgets.
Never retry a private form submission, POST or unknown external effect, or run
this recovery after private login starts. Another entry URL does not defeat a CAPTCHA.

Public-only Chrome probes on 2026-10-26 UTC reproduced the owner's observation:
an initial eBay GET returned 403 and response cookies; another automatic GET in
the same browser could load its homepage. Removing those response cookies kept
the 403, while restoring the original cookies allowed the homepage to load.
The admitted production URL was the correct `https://www.ebay.com/`; changing
CDP navigation to a typed transition or ordinary Playwright `goto` did not
prevent the first 403. This demonstrates the cookie dependency in the isolated
control, not every future eBay failure or a successful account login. No login
credentials, private browser state or CAPTCHA solver were used. The native
navigation fixture models the cookie-gated 403/200 behavior on a local server.

## Text answer

Before private login starts, observe the current challenge and send its image
with `browser_capture` and `delivery=interaction`. This uses the authenticated
owner's existing durable outbound route and waits for confirmed delivery even
inside a delegated task. Normal delegated screenshots remain attached to the
final report; an interaction image must be sent before the native question,
without waiting for that report. Confirm capture success before asking.

An interaction continuation started by a phone-ready event has no inbound
message spool. The runtime supplies a stable interaction-scoped outbound
transaction for tool-time images, preserving an existing inbound transaction
when present. Final report delivery uses its separate transaction. Resumed
images keep the same owner route, durable media claim and confirmed-delivery
requirements as the initial delegated screenshot.

Use `browser_input ask` for the exact observed answer field. It retains the
same live session while awaiting the answer, without transferring DOM control.
The existing protected-answer sink fills once using its opaque continuation;
the value need not enter model context, durable arguments or ordinary traces.
Submission is a separate action. Do not use an ordinary `request_user_input`
question as a substitute for this resource-bound browser question.

The question is bounded by the existing prepared-input window (at most five
minutes) and session expiry. The site may expire its challenge sooner. Stale
bindings must fail without retargeting or replaying the old answer; obtain a
new observation/image and ask again when needed.

## Visible operator control

For a puzzle requiring mouse interaction, prefer a configured local headed
ephemeral profile on the same target. Capture the obstacle first when allowed,
close the headless session and open the same site once in the visible temporary
profile. This starts a fresh session: cookies, authentication progress and a
previous challenge solution are not transferred. Never silently choose a
personal attached profile, a persistent identity or a cloud target instead.

`browser_targets` advertises `headed_view` and `handoff` per profile. The
gateway admits human control for a headed ephemeral profile as well as the
existing managed profiles. Headless ephemeral and node-target handoff remain
unsupported. The visible profile uses its own temporary root/lock and retains
normal actor, agent, network, approval and cleanup policy.

Call `browser_session handoff` with a question in the user's language explaining
where the window is visible and what the user should do. Agent DOM control is
paused while the owner operates that same browser. After the reply, resume the
same session and observe fresh state. Cancellation/expiry closes the temporary
runtime; ordinary completion still needs a confirmed close receipt.

Private `browser_login` state survives human control, including its live grant
and echo protections. Fresh verification can follow resumption without reading
credentials again. Private-session screenshots and ordinary protected-field
input remain denied; do not use other capture/fill APIs to bypass that boundary.
If an image cannot be safely sent, explain that limitation and use the visible
window, or capture a fresh challenge before private login in a new session.
External phone approval can also retire an idle browser: reopen using the
selected rendering profile and do not assume its old challenge survived.

## Implementation and rollout

- Add an explicit interaction-image delivery intent to the existing outbox;
  do not suppress it with ordinary child output or resend it in the final report.
- Extend the existing local headed-profile admission to temporary sessions;
  keep headless, node and private-data exclusions.
- Verify human/agent exclusivity, fresh observation after resume, temporary
  cleanup, truthful capability discovery and private-login protections.
- After reviewed merge, configure one visible temporary profile on the
  existing gateway desktop, preserving default selection, the headless profile
  and actor grants. Verify the actual window and native screenshot/input route before
  requesting an owner-driven website pilot.
- Test the site's real challenge through normal chat. A headed browser does
  not guarantee the site will remove its bot block; report the actual outcome.


## Installed qualification, 2026-10-25 UTC

[PR #1871](https://github.com/bogdanovich/mintclaw/pull/1871) merged as
`ea704efc47c8a7a9d77ad22eff50792a8d124d62` after thirteen successful CI
checks, one skipped check, clean review of the final head, no unresolved
threads and owner merge approval. That exact merged source is installed on the
existing Linux gateway with Go 1.26.10.

The deployed `gateway/ephemeral-visible` profile clones the existing temporary
profile's actor, agent, network and approval policy, with its own root/lock and
headed rendering. Default profile selection and other browser identities are
preserved. The private browser agent policy names the existing RustDesk desktop
and explicitly requests `browser_capture delivery=interaction` before questions.

Qualification completed:

- The new delegated transport-order test first failed with the question already
  delivered, zero images and zero source claims. It then passed with confirmed
  image delivery and exactly one claim before the native question. Ordinary
  final-report media and unconfirmed-receipt handling also passed.
- Full browser/tools/skills suites, the delivery/interaction agent regression
  set, targeted gateway checks and the relevant race checks passed. All seven
  opt-in Chromium OpenAI-compatible/Codex credential scenarios passed without
  skips, including delayed continuation and provider/history/log/trace checks.
- On the installed server, all 29 native credential/form/protocol scenarios
  passed in its actual Chrome, without skips. A separate synthetic headed
  fixture proved an X11 window was mapped, captured a PNG, filled a field,
  started without cookies and removed its window/runtime after close. The
  probe waits for the window manager's mapped state, rather than only window
  creation. No real account or vault value was used.
- Stateless smoke trace `trace-turn-080ae1f034511ca4465b7e62` completed on the
  deployed source with six records, zero tools, matching input/reply hashes,
  the expected redaction policy and no truncation.
- The phone page returned HTTPS 200 with JavaScript matching source; external
  operator enrollment remained denied with 403. All ten expected services were
  active with no failed units, legacy processes or error-level journal entries
  in the checked ten-minute window. The isolated nutrition installation was
  preserved.

Deployment guards paused safely when a staged configuration lacked its sibling
security file. Validation was repeated in the original profile directory so the
existing security document loaded normally; no security setting was weakened.
A later config guard detected only an added empty `coding.defaults` object from
the current schema. It was verified and preserved, without changing model,
credential, browser or other config semantics. The token and phone-key file
identities remained unchanged. Checksums verified all 38 retained rollback files;
restore only the affected binary/static-config layers while stopped, without
rolling back mutable tasks, interactions, grants or conversation state.

The successful Facebook/TOTP pilot is recorded separately on source `1013693a`.
A real eBay challenge with image delivery or RustDesk control remains an
owner-driven normal-chat pilot; these checks do not prove that eBay will remove
its bot block. A restart can require fresh phone approval, without re-enrollment.
