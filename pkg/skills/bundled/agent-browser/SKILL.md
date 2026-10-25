---
name: agent-browser
description: "Browser automation through MintClaw's first-party broker tools. Use for navigation, forms, authenticated pages, and only the capture, download, or human-handoff features advertised by the admitted target."
---

# Agent Browser

Use the first-party `browser_targets`, `browser_session`, `browser_observe`, and
`browser_act` tools. Do not search for or call raw Playwright MCP tools and do
not invoke an external `agent-browser` CLI. The broker owns the Playwright
driver, persistent profile, policy, and lifecycle for both gateway and
companion targets. The same workflow serves gateway and coding runtimes, but
the target catalog and current tool schemas are the authority for optional
features; never assume that handoff, delivery, upload, capture, or download is
present merely because this skill was selected.

## Core workflow

1. Call `browser_targets` and select the requested target, or its
   `default_target` when the current user request did not name one.
2. If the current task has a live broker-issued `browser_session_id` and the
   user explicitly requests continuity, inspect or resume that session.
   Otherwise open a new broker session and omit `profile` so the target's
   `default_profile` is selected. Choose a non-default profile for an explicit
   identity selection, or a same-target local headed ephemeral profile when a
   visual challenge requires human interaction. That rendering choice does not
   authorize a persistent, attached personal, or cloud identity.
3. Observe before acting. Copy the session, tab, snapshot, and context authority
   exactly from the fresh observation.
4. Act through `browser_act`, then observe again after each major page change.
5. Close the session at a terminal state unless it is suspended for approval or
   human control, or the user explicitly asked to keep it open.

Never infer a target from array order. Treat reported `dry_run`, approval
policy, and advertised features as authoritative. Do not bypass a missing
feature with raw MCP or arbitrary page code.

For a Steel target, `steel.use_proxy` in `browser_targets` says whether a new
session starts with Steel's paid proxy. When false, open the configured session
normally to start without that proxy. `steel.proxy_on_block` permits automatic
escalation only after a confirmed anti-bot block. `network_boundary` and
`readiness.proxy` describe network-policy enforcement; they do not indicate
that Steel's paid proxy is enabled. These are configured policies, not
per-call proxy toggles.

Treat live-resource state from conversation history as unverified. In
particular, an instruction to keep or leave the browser open after returning a
result is a cleanup/lifecycle requirement; it is not evidence that a session or
visible tab exists at the start and does not select an `attached_user` profile.
Do not choose an attached profile merely because a site may require login or an
earlier assistant said that a browser was open.

## Authentication recovery

A `navigation_failed` result keeps the browser session alive. It is not proof
that the site is down or that the user is logged out.

For a failed read-only navigation to a protected deep link:

1. Check `browser_session` status and keep using the same session.
2. Observe the current page. If it is blank or does not explain the failure,
   navigate once to the site's origin, such as `https://example.com/`, and
   observe again. Do not repeat the failed deep link first.
3. If the fresh observation shows sign-in, sign-up, an authentication challenge,
   or another clear logged-out state, report `authentication_required` rather
   than a generic navigation error.
4. For CAPTCHA/security challenges, follow the image-first assistance flow
   below. When `browser_targets` advertises both `headed_view` and `handoff`
   for the selected profile, call `browser_session` with `operation=handoff`
   for a manual step. Tell the user where that window is visible and what to
   do. Keep the session open while waiting.
5. After a handoff reply, call `browser_session` with `operation=resume` on the
   same session. Observe fresh state, verify that authentication succeeded, and
   retry the original read-only navigation once with fresh authority.
6. If neither a protected text question nor a visible temporary profile is
   available, report the exact missing capability and target/profile. Close
   the session before returning unless the user asked to preserve it.

Do not invent an authentication requirement without observing evidence. Do not
use origin recovery to retry an accepted or outcome-unknown external commit.

## CAPTCHA and security challenges

First show the obstacle with `browser_capture` and `delivery=interaction`
when capture is allowed. Confirm successful delivery before asking a question;
normal delegated captures are retained for the final report. Do not assume a Security Measure
page contains a text CAPTCHA. Do not solve it through an external solver.

For an observed text answer field, use `browser_input` to ask the human for
the answer. This retains the exact live session and binds the protected answer
to that field. Ask only after the image delivery succeeds. An ordinary `request_user_input` question does not retain
this browser. Submit separately after the protected answer is filled. If the
challenge or binding expires, observe and ask again; never reuse an old answer.

For a puzzle requiring mouse interaction in a headless session, first send the
available image, close that session, and reopen the same site once in a
configured same-target local ephemeral profile whose `headed_view` and
`handoff` are true. Then hand off the new visible browser. Cookies, login
progress and solved challenges do not transfer. Do not silently select a
persistent, attached personal, node or cloud profile instead. If no visible
temporary profile exists, say so rather than claiming a window opened.

After private `browser_login` starts, screenshots and ordinary protected field
input remain denied. Do not bypass these protections with another capture or
fill tool. Use visible handoff, or a fresh visible session for a new challenge
capture before private login. Do not retry an unknown private submission. If
phone approval retires an idle browser, reopen the selected rendering profile
and observe fresh state rather than assuming the previous challenge survived.

## Stale snapshots

On a pre-execution `stale_snapshot`, observe the same session and tab again,
reacquire semantic references, and retry the intended action once. Never retry
an accepted or outcome-unknown external commit.

## Cleanup

Before returning `completed`, `blocked`, or `failed`, close every session opened
by the workflow. Keep it open only during an active approval or human handoff,
or when the user explicitly requests preservation. If status or close fails,
return the original outcome plus a precise `cleanup_required` error.
