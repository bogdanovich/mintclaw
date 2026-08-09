# Node Companion

`mintclaw-node` is the slim first-party process that connects a Linux or macOS
machine to a MintClaw gateway. It does not include models, agents, channels,
sessions, MCP hosting, or workspace memory.

The companion creates a durable device identity, authenticates it with a signed
challenge over WSS, and keeps retrying while the gateway records an unknown
node as `pending_pairing`. After explicit operator approval, the gateway can
invoke only the commands allowed by both gateway policy and the node-local
policy. The current command surface includes `node.info.v1`, model-visible
execution, file transfer, service administration, single-node update, and
optional durable jobs. Every family remains disabled until its node-local
profile exists and the exact commands are approved during pairing.

## Build

```bash
make build-node
```

The resulting binary is `build/mintclaw-node`.

## Configure

Create `~/.mintclaw-node/config.json`:

```json
{
  "gateway_url": "wss://mintclaw.example.com/nodes/v1/ws",
  "state_dir": "~/.mintclaw-node",
  "tls": {
    "ca_file": "/etc/ssl/private/mintclaw-ca.pem"
  },
  "reconnect": {
    "min_delay_seconds": 1,
    "max_delay_seconds": 30,
    "pending_delay_seconds": 30
  }
}
```

Normal public certificates use the operating-system trust store and do not need
`ca_file`. A private CA can be supplied as shown. An exact out-of-band
certificate pin can be used instead:

```json
{
  "tls": {
    "certificate_sha256": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  }
}
```

There is no `insecure_skip_verify` option. Plain `ws://` is accepted only for a
loopback endpoint when `allow_loopback_plaintext` is explicitly true.

### Model-visible `system.exec.v1`

Fresh companion configurations do not enable `system.exec.v1`. To make a
bounded command usable by a model, first grant its raw node-local authority,
then add separate operator-owned discovery aliases:

```json
{
  "policy": {
    "revision": "diagnostic-v1",
    "allowed_commands": ["system.exec.v1"],
    "maximum_risk": "write",
    "max_timeout_seconds": 10,
    "max_output_bytes": 4096
  },
  "system_exec": {
    "working_roots": ["/srv/mintclaw-smoke"],
    "executables": ["/usr/bin/printf"],
    "environment": ["LANG"],
    "discovery": {
      "executable_aliases": {
        "diagnostic": "/usr/bin/printf"
      },
      "working_scope_aliases": {
        "workspace": "/srv/mintclaw-smoke"
      },
      "environment_names": ["LANG"],
      "guidance": [
        "Use diagnostic only for the bounded smoke check."
      ],
      "examples": [
        {
          "argv": ["diagnostic", "ready"],
          "cwd": "workspace",
          "timeout_seconds": 5,
          "env": {}
        }
      ]
    }
  }
}
```

Alias destinations must already be present in `executables` or
`working_roots`, and visible environment names must already be present in
`environment`. Discovery metadata cannot grant new authority. Raw normalized
paths remain accepted by node-local enforcement for existing operators but
are not shown to the model. Without at least one executable alias and one
working-scope alias, the command remains `partially_described` and cannot be
invoked by the model.

### Durable direct jobs

Durable jobs reuse the executable, working-scope, and environment aliases from
`system_exec`. They run a non-interactive argv process without shell parsing,
retain bounded stdout and stderr, expose truthful status and cancellation, and
snapshot only declared regular-file artifacts. An enabled profile is explicit;
an omitted or disabled `node_job_profiles` map advertises no job commands.

```json
{
  "policy": {
    "revision": "build-jobs-v1",
    "allowed_commands": [
      "job.start.v1",
      "job.status.v1",
      "job.logs.v1",
      "job.artifacts.v1",
      "job.cancel.v1"
    ],
    "maximum_risk": "write",
    "max_timeout_seconds": 30,
    "max_output_bytes": 65536
  },
  "system_exec": {
    "working_roots": ["/srv/project"],
    "executables": ["/srv/project/bin/build-job"],
    "environment": ["BUILD_MODE"],
    "discovery": {
      "executable_aliases": {"build": "/srv/project/bin/build-job"},
      "working_scope_aliases": {"project": "/srv/project"},
      "environment_names": ["BUILD_MODE"]
    }
  },
  "node_job_profiles": {
    "project-builds": {
      "enabled": true,
      "revision": "project-builds-v1",
      "executor": "system_exec",
      "timeout_seconds_max": 14400,
      "concurrent_jobs": 2,
      "stdout_bytes_max": 8388608,
      "stderr_bytes_max": 8388608,
      "artifact_count_max": 8,
      "artifact_bytes_max": 268435456,
      "artifacts_total_bytes_max": 268435456,
      "retention_seconds": 86400,
      "cancel_guarantee": "process_group",
      "approval": {"start": "required", "read": "none", "cancel": "required"}
    }
  }
}
```

Bind the gateway target to that exact profile:

```json
{
  "execution": {
    "targets": {
      "build": {
        "type": "node",
        "node": "linux-builder",
        "executor": "local",
        "job_profile": "project-builds"
      }
    }
  }
}
```

Approve all five advertised job commands only for a node intended to run the
profile. The model uses `nodes describe` immediately before each call, invokes
the commands through `nodes_invoke`, and keeps the returned opaque `job_id`.
`job.logs.v1` reads repeatable bounded chunks by stream and cursor.
`job.artifacts.v1` returns metadata and an opaque reference; `nodes_download`
then copies the selected immutable snapshot through the existing transfer
spool. A fresh `job.artifacts.v1` discovery revision is required for that
download.

Gateway restart or WSS disconnect does not stop a job accepted by a live
companion. `nodes_status` recovers the original start invocation without
redispatch, and a later routed turn can query the job itself. Companion or host
restart does not relaunch or reattach the process: a previously nonterminal job
becomes `unknown` or `interrupted`. Disable the profile and remove the target's
`job_profile` binding to roll back exposure; after the catalog changes, renew
pairing approval without the job commands.

## Run

```bash
mintclaw-node run --config ~/.mintclaw-node/config.json
```

The first successful handshake creates
`<state_dir>/identity.json` with owner-only permissions. Back up that file as a
secret: replacing it creates a different node identity.

## Systemd Services On Linux

Install a named systemd user service after creating its configuration:

```bash
mintclaw-node install \
  --instance main \
  --config ~/.mintclaw-node/main/config.json
```

System installation requires an absolute configuration path and an explicit
unprivileged account:

```bash
sudo mintclaw-node install \
  --system \
  --instance vpn \
  --config /etc/mintclaw/vpn-node.json \
  --service-user mintclaw-node
```

Installation is create-only. It refuses an existing managed or administrator
unit rather than replacing it. The installer serializes work per service,
publishes the unit without replacement, starts it, and waits for a stable
`active` state. A failed install removes only the exact unit created by that
transaction. Reinstall, upgrade, and uninstall are separate lifecycle actions.
The per-service lock coordinates MintClaw lifecycle commands; administrators
must not edit or reload the same unit concurrently with a lifecycle transaction.

Remove a managed user service:

```bash
mintclaw-node uninstall --instance main
```

System-service removal is explicit:

```bash
sudo mintclaw-node uninstall --system --instance vpn
```

Uninstall is idempotent when both the managed unit and its systemd registration
are absent. It refuses administrator units, units resolved from another path,
drop-ins, unexpected enablement links, and unsupported service states. The
command disables and stops the verified service before removing it. If removal
cannot be committed, it restores the exact unit and its prior persistent
enablement and active state when safe.

Inspect a named systemd user service:

```bash
mintclaw-node status --instance main
```

System-service status is explicit:

```bash
sudo mintclaw-node status --system --instance vpn
```

Use `--json` for stable machine-readable output from install, uninstall, and
status. Status is read-only and
fail-closed: it refuses symlinked or unowned unit files, units resolved from a
different systemd search path, units modified by drop-ins, and stale systemd
state awaiting `daemon-reload`. `run` remains available on every supported
platform.

## Launchd Services On macOS

Install a named per-user LaunchAgent:

```bash
mintclaw-node install \
  --instance main \
  --config ~/.mintclaw-node/main/config.json
```

This writes
`~/Library/LaunchAgents/com.mintclaw.mintclaw-node.main.plist`, bootstraps it
into the current user's launchd domain, and waits for a stable running state.
Installation is create-only and refuses an existing or foreign plist or an
already loaded job.

Inspect or remove that instance with:

```bash
mintclaw-node status --instance main
mintclaw-node uninstall --instance main
```

A system LaunchDaemon requires root, an absolute configuration path, and an
explicit unprivileged service account:

```bash
sudo mintclaw-node install \
  --system \
  --instance vpn \
  --config /etc/mintclaw/vpn-node.json \
  --service-user mintclaw-node
sudo mintclaw-node status --system --instance vpn
sudo mintclaw-node uninstall --system --instance vpn
```

The LaunchAgent and LaunchDaemon lifecycle is transactional and fail-closed.
Status and removal verify the managed plist identity and the exact launchd
domain and plist path. Uninstall first unloads the verified job, quarantines
the exact plist, and restores the previous plist and loaded state when removal
cannot be committed safely. As on Linux, `--json` provides stable
machine-readable output.

## Lifecycle Compatibility

| Platform | User service | System service | Install | Status | Uninstall |
| --- | --- | --- | --- | --- | --- |
| Linux | systemd user unit | systemd system unit | Supported | Supported | Supported |
| macOS | LaunchAgent | LaunchDaemon | Supported | Supported | Supported |
| Other | None | None | Not supported | Not supported | Not supported |

Lifecycle tests exercise both managers' rendering, identity checks,
create-only publication, state inspection, rollback, and removal behavior.
Darwin command tests are cross-compiled on non-macOS development hosts; final
release qualification should still run the lifecycle suite on the listed
native operating systems.

## Multiple Workspaces

The MVP uses one gateway binding per process. Run named service instances from
the same binary with distinct config and state directories:

```text
~/.mintclaw-node/main/config.json
~/.mintclaw-node/main/state/
~/.mintclaw-node/nutrition/config.json
~/.mintclaw-node/nutrition/state/
```

Each instance is paired and authorized independently. Do not point multiple
instances at the same state directory. A future multi-gateway supervisor may
share a capability runtime with explicit resource scheduling, but gateway trust,
policy, identity, and invocation ledgers will remain isolated per binding.

## Configure Named Targets

Operators bind stable target names to paired node IDs or aliases, then grant
each agent only the names it may select:

```json
{
  "nodes": {
    "enabled": true
  },
  "execution": {
    "targets": {
      "vpn": {
        "type": "node",
        "node": "vpn-box"
      },
      "build": {
        "type": "node",
        "node": "linux-builder",
        "executor": "local"
      }
    }
  },
  "agents": {
    "defaults": {
      "target_policy": {
        "default_target": "build",
        "allowed_targets": ["build"]
      }
    },
    "list": [
      {
        "id": "ops",
        "target_policy": {
          "allowed_targets": ["vpn", "build"]
        }
      }
    ]
  }
}
```

An agent-specific `target_policy` replaces the defaults policy for that agent.
An explicit empty `allowed_targets` list grants no targets. Target names and
their node references are operator configuration; a model cannot provide a
hostname, WebSocket URL, credential, or arbitrary node ID in place of a target
name.

Target visibility is only one authorization layer. It does not grant commands
that were not approved during pairing, bypass durable human approval, or
broaden the node-local command policy.

## Pairing Administration

After an unknown companion connects, inspect and approve its durable identity
from the gateway host:

```bash
mintclaw nodes list --state pending_pairing
mintclaw nodes describe node_<fingerprint>
mintclaw nodes approve node_<fingerprint> \
  --alias vpn-box \
  --display-name "VPN box" \
  --allow-command node.info.v1
```

Approval grants no commands unless each advertised command is named explicitly
with `--allow-command`. If the authenticated catalog changes, execution is
suspended until `nodes approve` is run again with the complete aliases,
display name, and allowed-command set to retain. Deny an untrusted pending
identity or revoke a paired one with a recorded reason:

```bash
mintclaw nodes deny node_<fingerprint> --reason "unknown device"
mintclaw nodes revoke vpn-box --reason "device retired"
```

All read and mutation commands accept `--json`. The CLI prints only a public-key
fingerprint, never the stored raw public key.
