# Gateway Credential Approval

This implements the approval portion of the
[trusted-runtime design](credential-trusted-runtime-design.md) in the existing
gateway. It creates no separate listener, host, VM, or credential service.
The current change does not retrieve a vault token or password, enable browser
login, send notifications, or qualify a physical Android device.

The gateway owns one runtime-internal WebAuthn controller and mounts its phone
page at `/credentials/`. Reuse the existing reviewed passkey ceremonies,
scope-bound approval/revocation, durable authenticator updates, maximum
20-hour grant lifetime, and nonpersistent grants. The ordinary phone handler
cannot issue initial-enrollment invitations or directly create a grant.

## Operator configuration

`credentials` is disabled by default. It contains references and metadata only:

```json
{
  "credentials": {
    "enabled": true,
    "owner_id": "owner",
    "origin": "https://mintclaw.example.org:18443",
    "state_directory": "/home/operator/.mintclaw/credential-state",
    "trust_loopback_tls_proxy": true,
    "accounts": [{
      "alias": "facebook",
      "vault_id": "dedicated-vault-id",
      "item_id": "facebook-item-id",
      "origins": ["https://www.facebook.com"],
      "principals": [{
        "owner_id": "owner", "actor_id": "actor",
        "agent_id": "agent", "host_id": "host"
      }]
    }]
  }
}
```

Use actual operator-owned identifiers and the runtime's authenticated identity
when configuring the later login coordinator. Accounts must use one dedicated
vault. No service-account token or login value belongs in this configuration.

The state directory must be outside configured agent workspaces, owned by the
current runtime user/group, and mode `0700`. The passkey record is mode `0600`,
single-linked and opened without following a final symlink. Store locking
prevents two controllers from writing concurrently; file and directory sync
acknowledge authenticator updates before grant issuance. Ordinary private home
directories are supported on Linux and macOS; Root-owned ancestry and a
dedicated service identity are unnecessary under the current trust model.
Containment resolves existing filesystem ancestors of both state and workspace
paths, including intermediate symlinks and state directories not yet created.
Configured workspaces must already exist so their filesystem identities can be
checked against every existing state ancestor; case aliases on macOS cannot
bypass this check. Distinct directories on case-sensitive volumes remain valid.
Prepare normal workspace directories before enabling credentials. Broken
or otherwise unresolvable aliases fail validation. The tree remains controlled
by the trusted operator; this adds no privileged-host isolation requirement.
On Linux the store rejects additional POSIX ACLs. On macOS setup must choose an
unshared directory without extra ACL grants; the portable runtime checks
ownership and POSIX permissions, rather than claiming macOS ACL verification.

Expose the page through stable HTTPS using the existing application's local
reverse proxy. `trust_loopback_tls_proxy` is an explicit trust decision for that
proxy: only a loopback peer with exactly one `X-Forwarded-Proto: https` header
is admitted without native TLS. Host and Origin must still match the configured
origin. Remote peers cannot establish HTTPS by supplying forwarded headers.
Native TLS requires TLS 1.3 and the correct server name. Canonical nondefault
HTTPS ports are supported and remain part of WebAuthn's exact origin check.

## Private enrollment and lifecycle

Run `mintclaw credentials init` with the intended profile configuration to
prepare the private state directory, then restart the gateway. Run
`mintclaw credentials enroll` manually in the operator's private terminal.
The command uses the gateway's existing PID-file authentication token over
loopback, disables HTTP proxies and redirects, and writes the five-minute
enrollment link directly to `/dev/tty`. It has no token/link argument or JSON
output and never writes the link to stdout/stderr. Do not run it in an agent
tool, captured terminal, or recorded session: a terminal descriptor does not
prove its viewer is the operator. Open that private link on Android and create
the approval passkey. No password is involved in enrolling the approval key.

The local operator enrollment endpoint requires authentication, a loopback
peer, no browser Origin header, and an empty JSON object. Enrollment ceremonies
also require the expiring invitation token and verified WebAuthn registration.
The existing invitation rules prevent refresh from extending its deadline.

Full shutdown closes the controller, disables leases, removes the mounted
route, and releases its store lock. A restart loads the passkey but restores
no grants. Unrelated gateway hot reload retains the approval controller;
changing credential configuration requires a gateway restart and fresh grants.

Synthetic tests cover actual HTTP mounting, private command output, durable
passkey updates and restart denial, origin/port and loopback-proxy admission,
and the phone UI's enrollment/20-hour approval/revocation on both root and
gateway paths using a virtual authenticator. These tests do not substitute for
actual Android enrollment or end-to-end password-output qualification.
