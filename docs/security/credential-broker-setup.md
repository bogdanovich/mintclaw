# Credential Broker Setup

## Current admission

The Linux `mintclaw-credential provision` command stores an OS-encrypted
1Password service-account token for the future isolated broker. It does not
contact 1Password, decrypt credentials, create a service, issue grants, or
enable any agent tool. Android approval and browser login remain later gates
in the [credential roadmap](../architecture/credential-access-roadmap.md).

All qualification uses synthetic data. Do not provision the operator's real
token until the isolated host, authenticated transport, Android approval,
credential browser, and CA6 rollout gates have been demonstrated.

This is a trusted operator root command. Root access, terminal access, and
an executable named `sudo` do not establish human approval. Agents must have
no root shell, unrestricted sudo, service-management rights, debugger access,
or equivalent capability on the credential host. An agent with root access
can bypass the broker and phone approval. The command does not establish
this host boundary; isolated deployment must qualify it separately.

## Protected operator input

Install a reviewed build under a root-owned path whose ancestors cannot be
changed by the agent. Run that absolute binary from a private operator
terminal as root, with both real and effective UID zero. Do not grant agents
permission to run the command. The command is deliberately absent from the
general MintClaw installer and tool registry.

The only setup argument is `provision`. There is no token flag, token
positional argument, environment-token fallback, pipe input, or model-facing
setup flow. It reads its controlling `/dev/tty` with echo disabled and a
five-minute timeout. Paste the token only at that private prompt; keep it out
of chat, model tools, shell command text, history, environment files, and
screen recordings. Cancel with Ctrl-C or Ctrl-D. Input is bounded to 32 KiB
of nonempty printable ASCII. Cancellation and invalid input clear owned
buffers and restore terminal state without writing a credential.

Before private input, the command disables core dumps, marks its process
non-dumpable, and checks the encryption executable and storage ancestry.
These protections do not hide memory from a trusted root administrator or
guarantee zeroization of Go, kernel, SDK, or browser copies.

## Vault scope

In the authenticated 1Password management UI, verify that the service account
has read access only to a dedicated MintClaw vault. Copy only the selected
login items into that vault. Personal/Private vaults cannot be granted to a
service account. Confirm the selected vault and item IDs when configuring
account aliases; the model does not choose them.

Provisioning checks input syntax and storage, not provider authentication or
the service account's permissions. Successful encryption is not evidence of
valid vault access. Later protected startup and pilot checks must verify the
configured bindings. Never print token contents to diagnose a provider error.

## Storage and failure behavior

The fixed destination is:

```text
/etc/credstore.encrypted/mintclaw-credential/op-service-account
```

Each ancestor must be root-owned and not writable by group or other users;
symlinks are refused. The final directory must be exactly mode `0700`. Newly
created directories are private; the command does not repair unsafe existing
paths. The encrypted file is root-owned mode `0600`. Concurrent setup is
denied with a directory lock, and an existing destination of any type is
refused before prompting. Publication uses an atomic no-overwrite link after
file sync, followed by directory sync. The command never replaces a token.

The trusted helper is the fixed root-owned `/usr/bin/systemd-creds`, with
explicit arguments `encrypt --name=op-service-account --with-key=auto - -`.
It receives plaintext only through a private stdin pipe. Its environment is
an explicit small allowlist, stderr is discarded, stdout is bounded, and its
process group is killed on the 30-second deadline. Returned errors are fixed
messages. The base64 output check is a format guard; cryptographic protection
and authenticated decryption belong to systemd.

Systemd's `auto` key mode uses the host key and, when available, a TPM key.
The host key commonly resides at `/var/lib/systemd/credential.secret`.
Host/TPM support and persistence must be checked on the actual deployment.
Encrypted storage protects the file within that OS trust boundary; possession
of sufficient host authority can still decrypt it. No ciphertext or host key
belongs in an agent workspace or backup accessible to agents.

If final directory synchronization fails after publication, the command
reports an uncertain outcome. The operator must inspect protected file
metadata before retrying; do not replay setup or print/decrypt the token as
a diagnostic. Ordinary failure and cancellation do not activate a service.

Rotation is not an in-place operation in this slice. When the later service
exists, the operator must stop credential access, invalidate grants and
sessions, revoke the old service-account token in 1Password, and follow a
reviewed replacement procedure. Do not remove an active credential merely
to bypass the create-once check.

## Reproducible qualification

From the repository root, with Docker available:

```sh
bash scripts/qualify-credential-provisioning.sh
```

The script cross-builds the command and executes it only in a disposable
Linux container. It never accepts a real token. A synthetic canary exercises
the real controlling terminal, cancellation, no echo, systemd encryption and
private round-trip verification, permissions, and refusal to overwrite.
Container runtime storage is removed with the container. The Debian base
image is pinned by digest; installed OS package versions must be qualified
again on the eventual deployment host.

The PR workflow runs this OS check plus normal and race suites for the
credential packages. This demonstrates provisioning behavior, not broker
deployment, Android operation, or real account login. Linux is the only
admitted provisioning platform; other platforms fail closed.

## Primary references

- [1Password service-account setup](https://www.1password.dev/service-accounts/get-started)
  and [security](https://www.1password.dev/service-accounts/security).
- [Systemd credential design](https://github.com/systemd/systemd/blob/main/docs/CREDENTIALS.md)
  and [systemd-creds reference](https://github.com/systemd/systemd/blob/main/man/systemd-creds.xml).
