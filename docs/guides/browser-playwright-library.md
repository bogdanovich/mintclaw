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
              "revision": "managed-library-v2",
              "mode": "managed",
              "allowed_agents": ["browser"],
              "allowed_actors": ["telegram:owner"],
              "network_mode": "any_http",
              "capability_mode": "full_access",
              "approval_mode": "model_requested",
              "dry_run": false,
              "allow_approved_actions": true,
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
      "revision": "managed-library-v2",
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
      "headed": true
    }
  }
}
```

Preserve the existing grants, actions, limits, and runtime paths when changing
the driver. The companion verifies both the sidecar and explicitly configured
browser executable before advertising the profile.

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
   suites; and
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
