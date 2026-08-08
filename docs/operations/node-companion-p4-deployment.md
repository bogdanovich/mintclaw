# Node Companion P4 single-node update deployment

This runbook installs and validates one P4-managed companion on Linux systemd
or macOS launchd. The default deployment grants no remote update authority.
Fleet rollout, package managers, coordinator self-update, bootstrap, and
same-UID malicious-process resistance are outside P4.

## Trust and artifact preflight

Use a reviewed merged-main revision. Build the stable coordinator separately
from the replaceable companion payload; P4 never remotely updates the
coordinator:

```sh
go build -tags 'goolm stdjson' -o ./dist/mintclaw-node ./cmd/mintclaw-node
go build -tags 'goolm stdjson' -o ./dist/mintclaw-node-coordinator ./cmd/mintclaw-node-coordinator
./dist/mintclaw-node version
./dist/mintclaw-node-coordinator version
```

Production installation requires release-versioned binaries rather than
development version output. Record the source revision and SHA-256 of both
binaries. The coordinator must be a separate, non-group-writable executable.
For a user service it is owned by that user; for a system service it is owned
by root and launches the exact configured unprivileged service account.

Verify the detached Ed25519 manifest offline as described in
[`node-companion-release-signing.md`](node-companion-release-signing.md). It
must enumerate exactly these slim payload tuples:

- Linux `amd64` and `arm64`;
- macOS `amd64` and `arm64`.

Never install the signing seed on a gateway or node. The companion receives
only the pinned public key. A repository checksum, URL, TLS connection, model
argument, or downloaded manifest is not release authority by itself.

## Back up and install deny-by-default

Back up the companion config, state directory, identity, invocation ledger,
current binary, coordinator binary, and owned service definition. Record
ownership, modes, hashes, service scope, and the rollback command before
changing the service. Use a reversible, non-essential canary instance first;
do not use the gateway, reviewer, SSH, VPN, or Tailscale service as the first
mutation.

An absent update configuration grants nothing. A disabled profile may retain
no release aliases:

```json
{
  "node_update_sources": {
    "production": {
      "base_url": "https://github.com/OWNER/REPOSITORY/releases/download",
      "public_key": "UNPADDED_BASE64_ED25519_PUBLIC_KEY"
    }
  },
  "node_update_policies": {
    "stable-node": {
      "enabled": false,
      "revision": "stable-node-v1",
      "source": "production",
      "channel": "stable",
      "approval": "required"
    }
  }
}
```

Install a fresh or deliberately stopped canary through the create-only
lifecycle. All paths must be absolute after shell expansion:

```sh
mintclaw-node install \
  --instance update-canary \
  --config /ABSOLUTE/PATH/config.json \
  --managed-update \
  --coordinator /ABSOLUTE/PATH/mintclaw-node-coordinator
mintclaw-node status --instance update-canary --json
```

For a systemd system service or LaunchDaemon, add `--system` and an explicit
unprivileged `--service-user`. Pre-provision the private state directory and
identity for that account. User scope creates a systemd user unit on Linux or
LaunchAgent on macOS; system scope creates a system unit or LaunchDaemon. The
owned lifecycle definition starts the stable coordinator, which selects the
companion payload from its private bounded store.

Confirm the original companion reconnects as the same node before enabling a
release. Merely installing the coordinator, upgrading the gateway, or adding
a trust source must not advertise `node.update.v1`.

## Enable one exact canary release

Enable one policy only after its signed manifest and exact platform archive
verify. The operator config binds a model-safe alias to an exact equal tag and
version; no URL or digest is model input:

```json
{
  "node_update_policies": {
    "stable-node": {
      "enabled": true,
      "revision": "stable-node-v2",
      "source": "production",
      "channel": "stable",
      "approval": "required",
      "releases": {
        "canary": {
          "tag": "vX.Y.Z",
          "version": "vX.Y.Z",
          "description": "reviewed canary"
        }
      }
    }
  }
}
```

The companion local policy must explicitly allow `node.update.v1` at
`privileged` risk. Pairing must approve that exact advertised command. The
gateway target binds `update_profile: "stable-node"`, and only the intended
agent target policy grants that target. Do not add it to a broad approval
bypass; routed durable human approval remains required by default.

After every config change, restart only the canary and obtain fresh discovery.
The expected model-facing sequence is:

1. `nodes describe` for `node.info.v1`, then `nodes_invoke` and
   `nodes_status` to record the installed version;
2. `nodes describe` for `node.update.v1` and verify it enumerates only
   release alias `canary`;
3. `nodes_invoke` with only `{"release":"canary"}`;
4. approve the exact target/release request as the routed human;
5. use `nodes_status` after disconnect or restart; never invoke again to
   recover an uncertain outcome; and
6. obtain fresh `node.info.v1` to independently observe the installed version.

Success requires authenticated same-node health with the expected version,
platform, and architecture after the companion reports a bounded catalog
digest. The coordinator validates that the catalog digest is well formed; it
does not compare it with the pre-update catalog because a versioned successor
may legitimately advertise a different catalog. Signed manifest protocol and
config compatibility are checked before staging. Service-manager acceptance
or process existence is insufficient.
Rollback is successful only when the previous verified payload reconnects and
passes the same health contract. Otherwise report `unknown` or
`operator_action_required`; do not retry automatically.

## Validation and redaction

Run the native real-process canaries on both operating systems:

```sh
go test -tags 'goolm stdjson' ./pkg/nodes/update/coordinator -run '^TestRealProcess' -v
go test -race -tags 'goolm stdjson' ./pkg/nodes/update/coordinator -run '^TestRealProcess'
```

The same tests cover user/system installation metadata, healthy activation,
bounded candidate attempts, verified rollback, disconnect, repeated status,
coordinator restart, and no second staging or activation. Existing coordinator
fault-injection tests cover durable state publication, download, verification,
activation, health commit, rollback, and cleanup boundaries.

Scan bounded gateway and companion logs, runtime events, approval prompts,
and passive traces. They may contain aliases, fixed phases, booleans, safe
error codes, and opaque correlation IDs. They must not contain URLs, manifests,
signing material, local paths, credentials, binary bytes, unrestricted output,
or raw request/result previews.

## Rollback and disable

To stop new update authority, remove the target from the agent grant, remove
its `update_profile` binding, and disable the companion update policy. Restart
the gateway and canary, then verify fresh discovery no longer exposes
`node.update.v1`. This does not rewrite a transaction that was already durably
accepted; recover it through `nodes_status` or local coordinator inspection.

For binary rollback, first disable remote authority and stop the owned canary
service. Restore the recorded coordinator, companion config, state, binary,
and service definition together, preserving ownership and modes. Start the
service, verify the same node identity reconnects, inspect errors and bounded
state, and confirm that no candidate activation was replayed. Do not replace
coordinator state piecemeal or delete the only verified previous payload.
