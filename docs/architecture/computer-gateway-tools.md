# Opt-in read-only computer tools

The gateway exposes three first-party tools only to explicitly granted agents:
`computer_targets`, `computer_session` and `computer_observe`. They use the
existing authenticated companion command and binary-transfer channels. They
cannot click, type, scroll, launch arbitrary applications or close applications.
Browser access, shell access and a connected companion are not computer grants.

## Operator configuration

First configure and approve the native companion profile as described in
[the companion boundary](computer-companion-boundary.md). All eight internal
computer commands must be approved. Obtain the exact normalized profile revision
from that companion's approved catalog; do not invent it or reuse a revision
after changing native scope, driver, owner policy or lifetimes.

Then merge an explicit gateway grant into the existing configuration:

```json
{
  "nodes": {"enabled": true},
  "execution": {
    "targets": {
      "native-companion": {"type": "node", "node": "operator-approved-node-alias"}
    }
  },
  "tools": {
    "computer": {
      "enabled": true,
      "targets": {
        "selected-desktop-window": {
          "node_target": "native-companion",
          "profile": "selected-window",
          "revision": "0000000000000000000000000000000000000000000000000000000000000000",
          "actors": ["telegram:owner"],
          "agents": ["browser"],
          "runtimes": ["gateway"],
          "lease_seconds": 300,
          "observation_seconds": 15
        }
      }
    }
  }
}
```

The zero revision is a placeholder, not a working grant. Replace it with the
approved profile's SHA-256 revision. Preserve existing gateway and companion
configuration. This configuration never installs a driver, starts a desktop,
chooses an ambient GUI or grants macOS permissions. Absent or disabled computer
configuration creates no computer runtime and exposes no computer tools.

Targets are bounded to 32 aliases. Each needs exact nonempty actor, agent and
runtime allowlists. Leases default to 300 seconds, with a 1,800-second maximum;
observation validity defaults to 15 seconds, with a 60-second maximum and never
outlives the lease. These gateway budgets can shorten, not extend, the native
host's limits.

## Tool and interaction lifecycle

1. `computer_targets` discovers configured, currently admitted window metadata
   without capturing pixels or starting a capture worker.
2. `computer_session` with `operation: "open"` accepts a configured gateway
   target alias. It does not accept a PID, display, window ID, native event,
   shell command or caller-authored principal.
3. `computer_observe` needs that session ID and current agent authority. Native
   PNGs travel through the protected binary channel, with byte, digest,
   invocation, workspace and full binding validation. Companion clock skew does
   not manufacture freshness: gateway deadlines start before dispatch and are
   bounded by the remaining remote validity and original local lease.
4. `computer_session` with `operation: "handoff"` needs the existing session ID,
   `interaction_language` from the root user command, and a self-contained
   `handoff_prompt` containing useful results and an open question. The existing
   live-resource suspension owns reply interpretation; there is no finish-word
   matcher in the computer tools.
5. A typed resume reclaims the same principal/session/worker/window incarnation.
   It does not capture implicitly. The resumed model must request a fresh
   `computer_observe` before describing current application state.
6. Release, typed abandon, turn cleanup and gateway shutdown end automation
   authority only. They do not close the native application. Reports explicitly
   say application state was not checked; they must not claim it remains open.

The full original actor, agent, routed session, runtime and execution identity
own a resource. Knowing a session ID is insufficient to use another turn's
session. The gateway policy revision includes the selected execution-target
definitions, so reassigning a node alias retires old tool authority too. A config
reload retires the old tool policy/workspace authority and
prepares the new computer source only after node reconciliation. Unconfirmed
release prevents discarding the old runtime. Unknown control outcomes fence
further mutations; status queries cannot clear that fence merely by returning
the unchanged pre-action state.

## Protected model context

Pixels and accessibility text are one-shot live model context. They do not enter
the media index, ordinary tool serialization, canonical history, outbound chat
attachments or deliverable artifacts. PNG context requires an explicitly
vision-capable model route; it cannot be silently removed to use a text-only
fallback. A successful model consumption removes that context from later
iterations. Current native Cua capture deliberately omits unqualified
accessibility text.

## Qualification

Run the real public gateway path with the same disposable fixture runner:

```sh
bash scripts/computer-companion-native-smoke.sh \
  --suite gateway \
  --driver /absolute/pinned/cua-driver \
  --node /absolute/mintclaw-node
```

Linux needs an explicit selected X11 environment. macOS additionally requires
`--app /absolute/Companion.app`, with the same binary and the actual app's OS
permissions. `--test-binary` accepts a cross-built integration test executable
containing `TestGatewayNativeComputerToolsOverWSS`. An absent fixture is a skip,
not native qualification; the runner requires explicit pinned inputs.

The gateway smoke exercises the public tools, trusted full principal, normal
gateway invocation persistence, real companion process, native window guard,
pinned capture worker and authenticated image transfer. It checks localized
handoff, denial of capture during human control, a visible fixture change,
different fresh resumed pixels with unchanged session/worker/window identity,
and release plus companion exit preserving both independently attested windows.
The companion suite separately exercises native window destruction during human
control, protected ledger replay and retained application lifetime.

Read-only Linux and positive macOS qualification must be tracked separately.
Neither fixture-backed tests nor a successful Linux run establish macOS
permission readiness. Public input and Linux Unicode insertion remediation
remain subsequent bounded-input work.
