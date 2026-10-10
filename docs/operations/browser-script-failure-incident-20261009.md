# Browser Script Failure Incident: October 9, 2026

## Evidence And Scope

Read-only diagnosis used private production traces and current durable state.
The traces use `mintclaw.diagnostic_trace.v1`, protected content, and empty
truncation envelopes. No rich trace, account details, purchase identifiers,
or source code is included here. Historical state is not modified.

- `trace-turn-8fd6eb1fa82184f96baec795`, created at 04:51:48 UTC:
  `browser_execute` call/result at records 130/133; the next session status
  at record 139 is `lost` with reason `outcome_unknown`. The canonical
  invocation declares `read`, was accepted, and became unknown. The bounded
  workflow checkpoint records a driver rejection. Protected source prevents
  proving which particular expression caused that rejection.
- `trace-turn-4250ca9ead5ffdc4c081f129`, created at 05:19:38 UTC:
  call/result at records 134/137 lasted approximately 15 seconds. The
  canonical accepted read invocation became unknown, and record 143 reports
  the lost session. The correlated journal confirms the driver process was
  killed when the execution deadline expired.
- Both workflows separately observed an order-history error rendered by the
  website. Fixing the browser lifecycle does not establish or repair the
  website's underlying error.
- A later workflow has a successful external-action receipt and reports
  postcondition verification in a new session. It also encountered a
  post-commit `driver_incompatible` observation error. Protected observation
  content prevents establishing that parser failure's exact cause; do not
  claim it is reproduced or fixed by script settlement.
- The later approval expired before execution. The user's affirmative
  message entered the still-running continuation afterward. The old approval
  was not consumed or replayed. The final objective outcome is succeeded,
  but the task registry incorrectly labels the owning task timed out because
  it uses the earlier interaction outcome. This is a separate bookkeeping
  fix; the historical approval outcome must remain unchanged.

## Fix Units

1. Preserve a browser session after a host-confirmed settled script failure,
   identically on gateway and companion. Keep ambiguous mutations,
   cancellation, transport loss, and network violations non-replayable.
   Improve facade guidance and return actionable same-session recovery.
2. Separate an expired approval's outcome from a subsequently verified
   successful task outcome. Preserve genuine timeout behavior and one-time
   delivery semantics.

## Acceptance

- Ordinary broker tests first reproduce the erroneous lost-session behavior.
- Exact private failure markers are required; source-returned strings,
  malformed markers, additional content, and success responses cannot grant
  settlement authority.
- Script rejection, effect mismatch, missing-locator reads, and pure source
  timeout preserve a usable real browser when settlement is confirmed.
- A timeout after a mutating RPC remains unknown and retires the browser.
- Gateway-to-companion settlement preserves the failed invocation, clears
  document authority, permits fresh observation, and dispatches source once.
- Restart/recovery returns the same durable failure, never resubmits it.
- Model-facing failures are errors, contain the safe failure classification
  and recovery action, and cannot emit external-action success receipts.
- The privileged-execution smoke requires exactly one intentional
  `browser_execute` tool error for its read-only timeout, followed by fresh
  observation in the same session and confirmed close. Extra, missing,
  wrong-tool, cleanup, or ambiguously lost-session failures do not pass.
- A genuinely expired approval is never silently converted into an allowed
  approval. A later verified success must not be mislabeled as a timed-out
  task.
- Live acceptance uses local fixture pages and reversible fixture-only DOM
  changes, preserving existing profile cookies. It never places another real
  order, edits a cart, or mutates an account.

## Remaining Diagnostic Boundary

The website's order-history error and the later protected observation parser
failure are distinct from settled script execution. A safe read-only
production retry can test the website and observation path after deployment;
it must not repeat the purchase. If the parser failure recurs, collect a
bounded structural diagnostic rather than publishing the protected page or
adding a website-specific exception.
