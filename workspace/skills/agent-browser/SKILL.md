---
name: agent-browser
description: "Browser automation through MintClaw's first-party broker tools. Use for navigation, forms, authenticated pages, screenshots, downloads, uploads, and resumable human-assisted browser workflows."
---

# Agent Browser

Use the first-party `browser_targets`, `browser_session`, `browser_observe`, and
`browser_act` tools. Do not search for or call raw Playwright MCP tools and do
not invoke an external `agent-browser` CLI. The broker owns the Playwright
driver, persistent profile, policy, and lifecycle for both gateway and
companion targets.

## Core workflow

1. Call `browser_targets` and select the requested target, or its
   `default_target` when the user did not name one.
2. Open the target's managed profile with `browser_session`.
3. Observe before acting. Copy the session, tab, snapshot, and context authority
   exactly from the fresh observation.
4. Act through `browser_act`, then observe again after each major page change.
5. Close the session at a terminal state unless it is suspended for approval or
   human control, or the user explicitly asked to keep it open.

Never infer a target from array order. Treat reported `dry_run`, approval
policy, and advertised features as authoritative. Do not bypass a missing
feature with raw MCP or arbitrary page code.

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
4. When `browser_targets` advertises both `headed_view` and `handoff`, call
   `browser_session` with `operation=handoff`. Tell the user to complete sign-in,
   2FA, CAPTCHA, or the required manual step in the visible local window. Do not
   close the session while waiting.
5. After the user replies, call `browser_session` with `operation=resume` on the
   same session. Observe fresh state, verify that authentication succeeded, and
   retry the original read-only navigation once with fresh authority.
6. If handoff is unavailable, clearly say that authentication is required and
   name the target/profile that must be logged in. Then close the session before
   returning unless the user asked to preserve it.

Do not invent an authentication requirement without observing evidence. Do not
use origin recovery to retry an accepted or outcome-unknown external commit.

## Stale snapshots

On a pre-execution `stale_snapshot`, observe the same session and tab again,
reacquire semantic references, and retry the intended action once. Never retry
an accepted or outcome-unknown external commit.

## Cleanup

Before returning `completed`, `blocked`, or `failed`, close every session opened
by the workflow. Keep it open only during an active approval or human handoff,
or when the user explicitly requests preservation. If status or close fails,
return the original outcome plus a precise `cleanup_required` error.
