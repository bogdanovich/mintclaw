# Human assistance for browser challenges

Use the existing browser, screenshot delivery and durable question mechanisms.
No CAPTCHA solver, extra credential service or new browser-control endpoint is
introduced. A Security Measure page is not necessarily an image-code CAPTCHA;
show the available evidence rather than inventing an answer field.

## Text answer

Before private login starts, observe the current challenge and send its image
with `browser_capture` or `browser_observe` with `screenshot=true`. These tools
already deliver media through the authenticated owner's durable outbound route.
When field refs are known, capture and `browser_input ask` can be ordered in
one tool batch so the question follows the image promptly.

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
