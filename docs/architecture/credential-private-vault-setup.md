# Private Vault Setup and Runtime Consumer

This connects the existing gateway approval owner to the runtime-internal
[1Password adapter](credential-runtime-vault-adapter.md). It creates no listener,
VM, separate credential host or model-facing value-retrieval tool.

## Operator setup

In the 1Password desktop app, create a separate vault such as `MintClaw` and
move the existing Login items you want the agent to use into it. Service
accounts cannot access the built-in Personal/Private vault. On 1Password.com,
open **Developer**, select your service account and inspect its vault access.
If it does not already have the intended scope, create a replacement account:
select only the dedicated vault, open its settings and enable **Read Items**
only. Do not grant write, sharing or vault-creation permissions. Existing
service-account vault permissions cannot be changed after creation. Save the
token privately in your Personal vault, outside the dedicated agent vault.
See [1Password service account setup](https://www.1password.dev/service-accounts/get-started).

Use a 1Password service account with read-only access to one dedicated vault.
Configure exact vault/item identifiers and account aliases in the existing
reference-only credential configuration. The adapter exposes only exact login
item reads; it does not expose listing, writes, arbitrary fields or SDK calls.
Identifiers must refer to the item's destination after moving it. No password
or service-account token belongs in this reference configuration.

Select the same profile as the gateway before setup. For the deployed main
profile, run these yourself in a private terminal:

```sh
export MINTCLAW_HOME=/home/server/.mintclaw/main
export MINTCLAW_CONFIG="$MINTCLAW_HOME/config.json"
```

The profile must already have valid credential configuration with
`credentials.enabled` set to `true`, its exact account bindings and a private
state directory outside agent workspaces. Keep browser login disabled until
its rollout qualification is complete. If `credentials token` reports that
credential access is disabled, finish this configuration before retrying;
the command has not requested or read the token.

Prepare ordinary workspace directories before enabling credentials. Run
`mintclaw credentials init`, then run `mintclaw credentials token` manually in
the operator's unrecorded private terminal. The token command has no token
argument, environment or stdin loader. It opens `/dev/tty`, disables echo before
showing its prompt, bounds input to 32 KiB, clears canceled input, and restores
terminal state after submission or Ctrl-C. The command's ordinary output is a
fixed setup result. Never run token entry in an agent tool or captured terminal,
or paste a token into chat.

Initial provisioning creates `service-account-token` beside approval state.
The existing owner directory must be mode `0700`; the file is mode `0600`,
single-linked, regular and opened without following a final symlink. Provisioning
never overwrites an existing file. Reads use a private synchronous callback and
clear their byte buffer afterward. The directory remains outside all configured
agent workspaces. Linux and macOS are supported; other platforms deny this path.
As with approval state, choose an unshared macOS directory without extra ACL
grants. These controls assume the approved trusted runtime and host.

Restart the gateway to initialize the SDK, then enroll the phone using
`mintclaw credentials enroll` if needed. Missing/invalid private token state or
SDK initialization failure leaves approval setup available but provides no
vault consumer. Restart restores the owner passkey and original unexpired
approval metadata matching the current policy; see
[durable approval](credential-durable-approval.md).
This implementation does not replace an existing token; rotation requires
operator administration of the private file and a gateway restart.

## Private use and lifetime

Startup registers the service token before passing it to the official SDK.
The SDK retains its own private token copy and provides no explicit client
destruction operation in the pinned adapter API. Its output registration is
therefore retained until process exit, including initialization failure; the
temporary file-read buffer is cleared. This is not a memory-zeroization claim.

The approval controller reuses an active signed grant for an exact
owner/actor/agent/host, alias, origin and policy revision. It owns that lookup;
the gateway creates no second grant cache and publishes no grant issuer.
Returned leases retain existing revocation/expiry/close behavior and immutable
provider binding and flow metadata.

Gateway login admission is runtime-private and returns a live lease plus a lazy
consumer. The browser must verify current form readiness before invoking the
consumer. It resolves the exact item from the live lease, checks authorization
before retrieval and again after retrieval, registers username/password values
before use, and projects callback/provider failures to a fixed error. Temporary
registrations end when the callback returns; a browser session independently
retains registrations for values/cookies it keeps until acknowledged cleanup.

## Qualification and remaining work

Synthetic tests cover token file permissions, link rejection, bounded input,
private buffer cleanup, no-echo entry and terminal restoration in a real child
PTY, signed grant reuse and scope denial, exact item binding, revocation before
and after retrieval, and fixed credential-bearing callback/SDK errors.

This does not activate a model login tool, send a notification, inspect a real
vault, qualify every output/artifact/persistence sink, or complete Android
enrollment. Current-cookie refresh, browser placement/flow admission, alias-only
tool wiring, one scoped connector, complete serialized-output qualification and
the physical Android/one-account pilot remain rollout requirements. No real
service token has been provisioned by this work.
