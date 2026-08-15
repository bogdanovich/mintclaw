# Browser Functional Parity Phase 5 Deployment Evidence

## Status

Browser Functional Parity Phase 5, **Remaining Ordinary Interaction Parity**,
is merged, deployed, and complete. The production gateway and the configured
Darwin companion run merged `main` commit
`85d6c61e640dd0b9546608c7b4788aee6d7c92fd`.

This record closes the Phase 5 acceptance criteria in
[Browser Functional Parity Execution Goal](../architecture/browser-functional-parity-execution-goal.md)
and the stop conditions in
[Browser Capability BF1 Remaining Ordinary Interactions Admission](../architecture/browser-capability-bf1-ordinary-interactions-admission.md).
It does not close the six-phase execution goal. Phase 6 remains active.

## Merged implementation

| Slice | Pull request | Merge commit |
| --- | --- | --- |
| Admission authority | [#734](https://github.com/bogdanovich/mintclaw/pull/734) | `ba3eb9b01f30263d5d459de24878c8194a5a460d` |
| Shared contracts | [#735](https://github.com/bogdanovich/mintclaw/pull/735) | `eecc7225ceada04827be86d4a19c0c5c1d222943` |
| Protected dialog | [#736](https://github.com/bogdanovich/mintclaw/pull/736) | `01df836e12697cbaebbdf96a94cc51cdf96e92b4` |
| Check, uncheck, and hover | [#737](https://github.com/bogdanovich/mintclaw/pull/737) | `4d547ca8d88f8e047e3abe9f3ea9854d6743679f` |
| Drag-and-drop | [#738](https://github.com/bogdanovich/mintclaw/pull/738) | `b04dcb4837bcb6f4b17376c18a51eded713934ed` |
| Gateway file chooser | [#739](https://github.com/bogdanovich/mintclaw/pull/739) | `33a2363bb2d4f66ee1f266bb7be6aa855ee20f6f` |
| Companion file chooser | [#740](https://github.com/bogdanovich/mintclaw/pull/740) | `e9cc439f6ab13ffd6fd89646043484aba934ad31` |
| Drag atomicity and reference repair | [#745](https://github.com/bogdanovich/mintclaw/pull/745), [#749](https://github.com/bogdanovich/mintclaw/pull/749) | `22fcb015be028b263e7269631ea4b55ccd98e318`, `604ae4ed61c37f8506a98619b23f17a228066512` |
| Companion close recovery | [#746](https://github.com/bogdanovich/mintclaw/pull/746) | `ece0f32ab985500c3f91a9ad6a80c71f30ef7c0d` |
| Dialog authority and dispatch repair | [#751](https://github.com/bogdanovich/mintclaw/pull/751), [#752](https://github.com/bogdanovich/mintclaw/pull/752) | `2a6e5448df6ffe8fb90070261810d35c3f7bb309`, `d4c698129313802c8a068adcafdc53222dbe95a3` |
| Missing file MIME regression | [#753](https://github.com/bogdanovich/mintclaw/pull/753) | `85d6c61e640dd0b9546608c7b4788aee6d7c92fd` |

The implementation and repair pull requests passed the repository test,
race, lint, security, Darwin, Windows, integration, and browser jobs required
by their scopes before merge.

## Contract and action coverage

The shared first-party contract now advertises and dispatches `dialog`,
`check`, `uncheck`, `hover`, `drag`, and `file_chooser` on both supporting
placements. The gateway and companion use the same semantic references,
freshness checks, typed results, approval derivation, and safe-error model.

- dialog accept, dismiss, and protected prompt values preserve pending-dialog
  authority without placing plaintext in durable command state;
- check and uncheck validate semantic control type and requested final state;
- hover uses a fresh semantic target and no coordinates;
- drag binds and atomically revalidates fresh source and destination refs;
- file chooser accepts one owner-bound retained artifact and never accepts a
  model-provided host path; and
- companion dispatch uses the private typed browser command set rather than
  exposing raw Playwright MCP, selectors, CDP, or page code.

Focused contract, gateway, companion-host, production-WSS, race, lifecycle,
artifact-ownership, stale-authority, and real-driver tests cover the accepted
and fail-closed paths. The final Phase 5 repair specifically changed the
runtime path exercised by a real `nodes_download`: when its retained artifact
has no declared media type, trusted upload preparation supplies
`application/octet-stream` after ownership, route, size, and digest checks.

## Live file-chooser completion evidence

The final production canary used a regular 13-byte companion file named
`p8a-ab-live.txt` with SHA-256
`624cca85be93d8e9d711e567311251428d493df3bb54e13099290bf03b9c3978`.
It did not pass that path to a browser tool.

The companion browser path completed through trace
`trace-turn-cfe87d6a38b1a31815c6108b`:

```text
node file info -> retained download artifact -> companion browser open
-> navigate -> semantic file chooser -> observe -> close -> reopen -> close
```

The page reported exactly `file-selected:p8a-ab-live.txt:13`. The retained
artifact was committed, chooser state was `succeeded`, the observed status was
verified, and both close operations returned `closed` with the intervening
open returning `ready`.

The equivalent gateway browser path completed through trace
`trace-turn-f9e08e507d1fd83434e540ea` with the same filename, byte count,
digest, retained-artifact route, DOM result, and cleanup sequence.

Both traces use schema `mintclaw.diagnostic_trace.v1`, completed without
truncation, and contain zero browser-action argument previews with the
companion host path. The pre-fix trace
`trace-turn-c76e847252d6080df43b0cc0` records the focused regression: artifact
retention and navigation succeeded, but chooser admission failed because the
real node artifact omitted a media type.

## Specialist boundary and cleanup

Direct `main` browser access and the matching companion opaque identity were
enabled only for the owner-bound cross-tool canary. Both temporary grants were
removed after the gateway and companion paths passed. The normal production
boundary again exposes browser tools only to the `browser` specialist.

A fresh post-cleanup request delegated exactly once from `main` to `browser`.
The specialist opened the configured logical `companion` target, navigated to
the controlled canary, observed title `MintClaw Phase 5 Canary`, and completed
`close -> reopen -> close`. The companion Playwright process exited after the
final close, and no local Playwright MCP process retained the profile.

The node reconnected after the scoped restart and reported `state=connected`.
All expected MintClaw services were active, product failed-unit count was
zero, the ten-minute error journal count was zero, and the launcher and
reviewer probes returned their expected status codes. `mintclaw doctor`
loaded the active main configuration; exit code 2 contained the pre-existing
informational workspace-shadowing findings rather than a schema or load
failure.

Retained node-transfer records remain in the bounded P2 spool until their
configured expiry and cleanup policy. They are not browser profile leases or
running driver processes.

## Rollback

The pre-deployment gateway binary, active configuration, and run script are
retained under:

```text
/home/server/.mintclaw/deploy-backups/phase5-upload-85d6c61e-predeploy-20260815T1301Z
```

The pre-deployment Darwin companion binary and configuration are retained
under:

```text
/Users/ab/.mintclaw-node/local-test/backups/phase5-upload-85d6c61e-predeploy-20260815T1301Z
```

Rollback restores only the affected binary and configuration snapshot, then
restarts `mintclaw-main.service` and the named Darwin companion process.

## Next phase

The next dependency-ordered item is Phase 6, **BF2 Media, Transfer,
Diagnostics, And Snapshot Delivery**. It must begin with a current-state audit
and focused admission that distinguishes already shipped gateway B2 behavior
from missing shared gateway/companion parity.
