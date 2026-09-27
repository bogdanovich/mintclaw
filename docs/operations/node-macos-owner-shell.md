# macOS same-user owner shell

This optional Node Companion profile exposes non-interactive `shell.exec.v1`
as the macOS account already running `mintclaw-node`. It is intended for a
trusted owner operating a personal Mac. Fresh installations remain disabled.

It does not broaden `system.exec.v1`, install a helper, change identity, open a
PTY, inject sudo credentials, or claim root containment. The shell has all
ambient authority of the companion account. On an administrator account that
can include keychain, network, package-manager, GUI-automation, or existing
passwordless-sudo authority. Use a dedicated non-admin account when those
rights must be excluded.

## Node configuration

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

Exactly one of `owner_shell.broker_socket` and `owner_shell.local_user` may be
configured. The local-user executor is rejected on Linux and other platforms;
the existing root-owned authority broker remains the Linux owner-shell path.

## Outcome and cancellation truth

A normally exited shell returns exit code, bounded stdout/stderr, signal, and
timing through the existing durable invocation ledger. Provider retry,
gateway reconnect, and status recovery observe the same invocation and never
dispatch it again.

The macOS local-user descriptor does not advertise confirmed cancellation.
MintClaw owns and cleans the immediate process group, but arbitrary shell code
can create a new session. If timeout or disconnect occurs after start,
MintClaw performs best-effort cleanup and records the durable outcome as
`unknown`; it never reports `canceled`, `failed`, or safe-to-retry without
proof. Long-lived work should use the existing durable node-job or remote
coding-task surfaces instead of backgrounding children from `shell.exec.v1`.

## Smoke test

After updating the gateway and companion to the same merged revision, refresh
node discovery and invoke a harmless command through the configured target:

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

Rollback is disable-first: remove `shell.exec.v1` from gateway/node policy or
remove `owner_shell`, restart only the companion, refresh discovery, and
verify the command is unavailable. Retain the invocation ledger; never delete
durable state to manufacture a clean rollback.
