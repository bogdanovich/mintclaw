# Direct Playwright Library Driver

MintClaw can run its first-party browser tools through a private Node.js
sidecar that imports the official Playwright library. The sidecar is not an
MCP server: it does not perform tool discovery, it is not registered with an
agent, and its JSON-lines protocol remains inside the browser worker.

The existing `playwright_mcp` driver remains a supported rollback path. Both
drivers use the same Go worker, broker, profile lease, network proxy, durable
receipts, approval policy, artifact boundary, and cleanup state machine.

## Install The Pinned Dependency

From an exact MintClaw checkout, run:

```sh
make install-browser-playwright-library
```

This runs `npm ci` against
`runtime/browser/playwright-library/package-lock.json`. It installs the pinned
Playwright library but does not download a browser. Configure an existing
Chrome, Chromium, Firefox, or WebKit executable. Node.js 20 or newer is
required by the pinned package.

Keep the checkout and `node_modules` directory readable by the service
account. The sidecar file itself must be executable.

On macOS, install the companion with the managed `mintclaw-node service`
lifecycle. Its launchd definition includes the standard Homebrew executable
directories so the sidecar's `env node` launcher can resolve a Homebrew Node.js
runtime. A pre-existing launchd definition must be reinstalled or given an
equivalent `PATH` before selecting this driver.

## Gateway Configuration

A gateway-local target selects the direct driver with
`driver: "playwright_library"`. It uses direct driver fields instead of an MCP
server reference:

```json
{
  "tools": {
    "browser": {
      "enabled": true,
      "agents": ["browser"],
      "default_target": "gateway",
      "targets": {
        "gateway": {
          "enabled": true,
          "placement": "gateway",
          "driver": "playwright_library",
          "driver_executable": "/opt/mintclaw/runtime/browser/playwright-library/sidecar.cjs",
          "driver_arguments": [
            "--browser=chromium",
            "--executable-path=/usr/bin/chromium"
          ],
          "default_profile": "managed",
          "profiles": {
            "managed": {
              "enabled": true,
              "revision": "managed-library-v3",
              "mode": "managed",
              "allowed_agents": ["browser"],
              "allowed_actors": ["telegram:owner"],
              "network_mode": "any_http",
              "capability_mode": "full_access",
              "approval_mode": "model_requested",
              "dry_run": false,
              "allow_approved_actions": true,
              "privileged_execution": {
                "enabled": true,
                "runtime_seconds": 15,
                "output_bytes": 65536,
                "actions": 64,
                "memory_mb": 64,
                "network_requests": 64,
                "artifacts": 4,
                "artifact_bytes": 8388608,
                "concurrent": 1
              },
              "runtime": {
                "profile_directory": "/var/lib/mintclaw/browser/managed",
                "lock_file": "/run/mintclaw/browser-managed.lock",
                "headed": true
              }
            }
          }
        }
      }
    }
  }
}
```

The worker adds the profile directory, ephemeral isolation root, output
directory, headed mode, and enforcing network proxy. Do not place those
host-owned options in `driver_arguments`.

## Companion Configuration

Companion profiles already keep their driver configuration on the execution
host. Change the profile driver and executable, update the executable digest,
and increment the profile revision:

```json
{
  "browser_profiles": {
    "managed": {
      "enabled": true,
      "revision": "managed-library-v3",
      "driver": "playwright_library",
      "driver_executable": "/opt/mintclaw/runtime/browser/playwright-library/sidecar.cjs",
      "driver_executable_sha256": "<lowercase-sidecar-sha256>",
      "driver_arguments": [
        "--browser=chromium",
        "--executable-path=/Applications/Chromium.app/Contents/MacOS/Chromium"
      ],
      "profile_directory": "/var/lib/mintclaw/browser/managed",
      "lock_file": "/run/mintclaw/browser-managed.lock",
      "mode": "managed",
      "network_mode": "any_http",
      "capability_mode": "full_access",
      "approval_mode": "model_requested",
      "dry_run": false,
      "allow_approved_actions": true,
      "privileged_execution": {
        "enabled": true,
        "runtime_seconds": 15,
        "output_bytes": 65536,
        "actions": 64,
        "memory_mb": 64,
        "network_requests": 64,
        "artifacts": 4,
        "artifact_bytes": 8388608,
        "concurrent": 1
      },
      "headed": true
    }
  }
}
```

Preserve the existing grants, actions, limits, and runtime paths when changing
the driver. The companion verifies both the sidecar and explicitly configured
browser executable before advertising the profile.

## Privileged Browser Execution

`browser_execute` is a separate, opt-in first-party tool for cases where the
typed browser actions do not expose a required Playwright operation. It is
available only on an enabled, revisioned `playwright_library` profile whose
`privileged_execution.enabled` value is `true`. Enabling or changing this
authority requires a new profile revision.

The example values above are the defaults materialized by MintClaw. Operators
may lower them per profile. A tool call cannot raise them. The host enforces
the following independent budgets outside submitted JavaScript or TypeScript:

| Field | Default | Maximum |
| --- | ---: | ---: |
| `runtime_seconds` | 15 | 60 |
| `output_bytes` | 65,536 | 262,144 |
| `actions` | 64 | 256 |
| `memory_mb` | 64 | 256 |
| `network_requests` | 64 | 256 |
| `artifacts` | 4 | 8 |
| `artifact_bytes` | 8,388,608 | 8,388,608 |
| `concurrent` | 1 | 1 |

The source receives only a scoped `{page, context, artifacts}` facade. It has
no process, filesystem, import, environment, endpoint, credential, profile
path, or raw artifact-path authority. The complete source digest, fresh
document authority, declared effect, profile and policy revisions, effective
budgets, and approval decision bind one durable invocation. Once accepted, an
invocation is never replayed automatically.

Execution cannot widen the profile's network authority. `exact_origins`,
`public_web`, or `any_http` and the canonical exact-origin set are bound into
the durable invocation and checked again by the execution host. The request
boundary covers navigation, subresources, redirects, fetches, and WebSockets;
`public_web` also rejects special-purpose addresses after DNS resolution.
If an execution can leave page-scheduled work behind, its network authority
and remaining request budget stay attached to that browser context until it is
closed. Later guards compose by intersection. This prevents delayed timers,
event handlers, or WebSocket creation from escaping an invocation after its
worker has returned; opening a fresh session is the way to discard an exhausted
or intentionally narrower retained boundary. Service workers are disabled in
the direct-driver context so they cannot bypass that routing boundary.

The effect selects the permitted facade operations. Read and navigation calls
cannot mutate the page, `local_edit` permits typed form and keyboard edits, and
arbitrary `page.evaluate` or `locator.evaluate` is available only with
`external_commit` or `unknown`. This is an effect-integrity boundary, not a
mandatory prompt: a full-access profile with `approval_mode=none` still runs
those effects unattended.

Execution uses the profile's existing `approval_mode`: `none` runs unattended,
`model_requested` prompts only when the model supplies `confirmation`,
`always_commit` prompts for commit/unknown effects, and `policy` follows the
configured policy. This tool does not add a separate hard-coded approval rule.
Keep it disabled on profiles that need only typed browser actions.

Restricted profiles evaluate privileged execution with the policy-only action
name `execute`. Declarative rules and hooks receive only bounded metadata
(effect, origin, and revisions), never source text or browser data. Gateway and
companion decisions are bound separately and revalidated immediately before
their respective dispatch boundaries.

## Cutover And Rollback

Changing the driver for an enabled managed profile requires a new profile
revision. Gateway hot reload rejects a same-revision driver transition. The
MCP and library drivers acquire the same profile lock, so they cannot open the
same persistent identity concurrently.

Hot reload also rejects removing a configured managed profile or changing it
to another mode. Perform that administrative removal across a controlled
gateway restart. This keeps a remove-and-readd sequence from bypassing the
driver revision gate.

Before selecting the direct driver as the default:

1. stop or close every session on the profile;
2. install the pinned dependency on the execution host;
3. change the driver and increment the profile revision;
4. restart or safely reload the relevant gateway or companion;
5. run `core`, `managed-reuse`, `ephemeral-cleanup`,
   `driver-conformance`, `provider-lifecycle`, and `playwright-library` smoke
   suites, plus `privileged-execute` when that capability is enabled; and
6. verify clean session, profile-lock, driver-process, and browser-process
   state after every suite.

Rollback performs the same controlled transition in reverse: close the direct
session, select `playwright_mcp`, restore `driver_server` or companion MCP
launcher fields, clear gateway direct-driver fields, increment the managed
profile revision again, and rerun the canary. Never run both drivers against
one persistent profile to compare them.

The direct driver currently supports managed and ephemeral browser profiles.
Attached-user browser control remains on the deferred extension path and is
not admitted through the library sidecar.
