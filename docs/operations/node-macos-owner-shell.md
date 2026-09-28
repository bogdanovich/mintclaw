# macOS owner shell

MintClaw offers two explicit macOS executors behind the same non-interactive
`shell.exec.v1` model contract:

- `local_user` runs as the account already running `mintclaw-node`; and
- `privileged_helper` keeps the network-facing companion unprivileged while a
  root LaunchDaemon owns one exact configured shell profile.

Both are intended for a trusted owner operating a personal Mac. Fresh
installations remain disabled and deny-all. The model cannot choose the
executor, shell path, UID/GID, helper path, service label, or config path.

Neither mode broadens `system.exec.v1`, opens an agent PTY, injects sudo
credentials, or claims Linux-style cgroup containment. A same-user shell has
all ambient authority of the companion account. On an administrator account
that can include keychain, network, package-manager, GUI-automation, or
existing passwordless-sudo authority. Use a dedicated non-admin account when
those rights must be excluded.

## Same-user node configuration

Add one executor under `owner_shell` in the companion configuration:

```json
{
  "policy": {
    "revision": "owner-user-policy-v1",
    "allowed_commands": ["shell.exec.v1"],
    "maximum_risk": "privileged",
    "max_timeout_seconds": 240,
    "max_output_bytes": 131072
  },
  "owner_shell": {
    "enabled": true,
    "local_user": {
      "revision": "owner-user-v1",
      "profile": "owner-user",
      "shell_path": "/bin/zsh",
      "login": true,
      "working_scopes": {
        "home": "/Users/operator"
      },
      "fixed_environment": {
        "HOME": "/Users/operator",
        "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
      },
      "permitted_environment_names": ["LANG", "LC_ALL", "TERM"],
      "timeout_seconds_max": 240,
      "output_bytes_max": 131072,
      "concurrent_commands": 2
    }
  }
}
```

The normal node policy may allow additional commands; the example shows only
the fields relevant to the shell. The gateway target and agent target policy
must separately grant `shell.exec.v1`. Existing exact target-scoped approval
bypass may be enabled for an owner-only target. Otherwise every invocation
uses the normal durable human-approval flow.

The model sees `owner-user`, `home`, permitted environment names, and limits.
It does not see the shell path, real working path, UID/GID, fixed environment
values, or node-local configuration. `home` selects only the initial directory:
arbitrary shell text can access any resource available to the account.

The child does not inherit the companion service environment, which avoids
implicitly forwarding gateway or provider secrets. Fixed environment values
and login files provide the intended owner `PATH` and shell setup.

Exactly one current executor may be configured. `local_user` is supported on
Linux and macOS. Linux `privileged_helper` uses an explicit root-owned Unix
socket endpoint; macOS `privileged_helper` instead uses a private inherited
descriptor and rejects a configured endpoint. The legacy Linux-only
`broker_socket` input is accepted only as a bounded load-time migration alias
and must not appear in new configuration.

## Privileged-helper configuration

The node config selects the mode without naming an IPC endpoint:

```json
{
  "owner_shell": {
    "enabled": true,
    "privileged_helper": {}
  }
}
```

The separate root-owned broker config binds the exact child executable,
companion config, unprivileged account, and single authority profile. A minimal
shape is:

```json
{
  "companion": {
    "executable_path": "/opt/mintclaw/mintclaw-node",
    "config_path": "/etc/mintclaw/node.json",
    "uid": 501,
    "gid": 20,
    "supplementary_groups": []
  },
  "revision": "owner-root-broker-v1",
  "profiles": {
    "owner-root": {
      "revision": "owner-root-profile-v1",
      "shell_path": "/bin/zsh",
      "login": false,
      "uid": 0,
      "gid": 0,
      "supplementary_groups": [],
      "working_scopes": {"root": "/"},
      "fixed_environment": {
        "PATH": "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
      },
      "permitted_environment_names": ["LANG"],
      "network": "inherit",
      "timeout_seconds_max": 240,
      "output_bytes_max": 131072,
      "concurrent_commands": 2
    }
  }
}
```

Every referenced file and directory chain must be absolute, non-symlink,
root-owned, and not group/world writable. The configured service account must
match the companion UID/GID and every supplementary group. Install the exact
pair in system scope:

```sh
sudo /opt/mintclaw/mintclaw-node service install \
  --system \
  --instance owner \
  --config /etc/mintclaw/node.json \
  --service-user operator \
  --authority-broker /usr/local/libexec/mintclaw-node-broker \
  --authority-config /etc/mintclaw/node-authority-broker.json
```

The lifecycle transaction publishes one root LaunchDaemon that supervises the
exact unprivileged child and passes a private inherited capability. It creates
no public root-shell socket. Privileged mode is intentionally incompatible
with the managed-update coordinator until private-capability handoff across an
update is separately admitted. A failed install removes an unready service and
restores the create-only lifecycle state.

## Outcome and cancellation truth

A normally exited shell returns exit code, bounded stdout/stderr, signal, and
timing through the existing durable invocation ledger. Provider retry,
gateway reconnect, and status recovery observe the same invocation and never
dispatch it again.

Neither macOS executor advertises confirmed cancellation or terminal support.
MintClaw owns and cleans the immediate process group, but arbitrary shell code
can create a new session. If timeout, disconnect, helper restart, or ambiguous
child observation occurs after start, MintClaw performs best-effort cleanup
and records the durable outcome as `unknown`; it never reports `canceled`,
`failed`, or safe-to-retry without proof. Long-lived work should use the
existing durable node-job or remote coding-task surfaces instead of
backgrounding children from `shell.exec.v1`.

## Smoke tests

After updating the gateway and companion to the same merged revision, refresh
node discovery and invoke a harmless same-user command through the configured
target:

```text
Use nodes describe for shell.exec.v1 on target ab-2. Then invoke profile
owner-user in cwd home with: printf 'OWNER_SHELL_OK\\n'; id -u; pwd. Do not use
system.exec, terminal, coding tasks, or a different target.
```

Expected evidence is the marker, the companion account UID, the configured
home directory, one durable invocation ID, and no duplicate execution. Verify
that discovery reports `supports_cancel: false`, paths and environment values
are absent from discovery and logs, the node remains connected, and unrelated
typed commands still work.

For a privileged-helper smoke, use its configured target, profile, and working
scope, verify the returned UID, and require discovery to report
`supports_cancel: false` and `supports_terminal: false`. Never use a shell
invocation to approve or install its own helper.

Rollback is disable-first: remove `shell.exec.v1` from gateway/node policy or
remove `owner_shell`, then restore the saved binary, config, and plist as one
operator transaction. For privileged mode, boot out the root LaunchDaemon and
remove its private service before restoring the ordinary companion lifecycle.
Refresh discovery and verify the command is unavailable. Retain the invocation
ledger; never delete durable state to manufacture a clean rollback.
