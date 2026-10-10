# Recovering a blocked browser click

First-party Playwright clicks run a bounded actionability trial before sending
input. The trial uses at most five seconds and one quarter of the admitted
action budget. It does not force the click or change the configured access mode.

If the trial fails and the driver confirms the page and connection are still
usable, the action ends with `click_not_dispatched`. The same session remains
ready, but its old snapshot is invalidated. The agent must observe fresh state
and may choose another visible control, such as a gallery button instead of a
covered image. The failed invocation remains terminal and cannot be replayed.
The real click checks navigation identity again after the trial.

Cancellation, transport loss, malformed results, and failures after input do not
establish that nothing happened. They retain conservative `outcome_unknown`
handling. Neither a model-declared read effect nor exception text authorizes
recovery. This contract is shared by gateway and companion execution.

## Live smoke test

The `click-recovery` suite uses a safe fixture with a covered image and an
alternative button. It requires exactly one expected tool failure, no image
click, a successful alternative action in the same session, confirmed closure,
and independent immediate profile reuse. It does not visit or modify listings.

On the gateway host, using its installed binary:

```sh
MINTCLAW_BROWSER_SMOKE_BINARY=/home/server/.local/bin/mintclaw \
  scripts/browser-capability-smoke.sh --target gateway --profile managed \
  --suite click-recovery --json-output /tmp/gateway-click-recovery.json
```

For a companion, run the fixture on that companion and route the live client
through the gateway. Substitute a companion address reachable by the gateway:

```sh
scripts/browser-capability-smoke.sh --target companion --profile managed \
  --suite click-recovery --gateway-host server@oc \
  --fixture-host COMPANION_ADDRESS \
  --json-output /tmp/companion-click-recovery.json
```

A report passes only with `safe_error: null`, all checks true, verified execution
and process audits, and clean session closure. A natural-language success claim
alone is not sufficient evidence.
