# Credential Provider And Isolation Qualification

Qualification record dated 2026-10-10 for CA2 of the
[credential access roadmap](credential-access-roadmap.md). This records
provider build evidence and the service boundary required by the next
admission. Protected token provisioning is implemented; isolated service
activation and actual provider/consumer qualification remain pending.
The record does not enable credential access or admit CA3/CA4.

## Provider candidate

The official 1Password Go SDK tag `v0.4.1` resolves to
`f60aea196b471a9c35ddad5f9096e5fd2b5c939c`. A separate temporary checkout
successfully built `./...` with `CGO_ENABLED=0` for Linux AMD64, Linux ARM64,
Darwin ARM64, and Windows AMD64. The service-account token variable was removed
from every build environment. No real token, vault, or account was accessed.

Service-account authentication uses the embedded WASM/Extism core. Desktop-app
authentication is a distinct native-library path; its CGO constraint does not
apply to this service-account build. Prefer a pinned SDK adapter to a shell
wrapper. SDK initialization must be serialized; its shared core initialization
is not guarded. Retain one private client for the service lifetime. Finalizer
release is not a guarantee of immediate memory erasure.

Expose only the item retrieval method to the adapter. Validate that the
returned vault ID, item ID, login category, required field IDs/types, count,
and value bounds match the operator's binding. Reject duplicates, ambiguous
bindings, malformed items, attachments, unsupported login types, and raw MFA
seeds. Provider errors and cancellation produce fixed failures, without raw
SDK error text or serialized item contents. Do not grant the caller a generic
secret-resolution URI or any write/share API.

Sources:

- [Official SDK v0.4.1](https://github.com/1Password/onepassword-sdk-go/tree/v0.4.1)
- [Client initialization](https://github.com/1Password/onepassword-sdk-go/blob/v0.4.1/client_builder.go)
- [Embedded service-account core](https://github.com/1Password/onepassword-sdk-go/blob/v0.4.1/internal/extism_core.go)
- [SDK authentication and versioning](https://www.1password.dev/sdks)

## Required process boundary

The broker owns the provider client and credential browser worker. The gateway
routes operation intents and aliases, and receives safe status or admitted
observations. No IPC response carries a username/password pair, token, cookie,
MFA seed, or driver handle. Use a separate service identity and private runtime
directories. A client must not select its own principal: authenticated caller
identity maps to operator-configured owner/actor/agent/host authority.

Protect both the service token and IPC authentication material from agent
execution. A same-UID arbitrary shell can read identity files or inspect other
processes; socket permissions alone do not solve this. Root execution on the
credential host is inside the trusted operator boundary and cannot be granted
to an untrusted credential consumer. Ordinary browser sessions must have no
reference to the credential worker or its storage.

Existing `pkg/isolation` sandboxes child processes, not the gateway. The general
Linux profile adds mounts for absolute command arguments and does not unshare
the PID namespace. It cannot alone establish this credential isolation
boundary. Use a separately owned service and explicitly qualified execution
profiles; do not infer security from `isolation.enabled`.

## Credential-browser startup qualification

The first pilot targets an isolated Linux browser on the MintClaw server.
This is an implementation assumption pending the operator's placement choice;
it does not admit an attached desktop profile or a cloud browser.

The ordinary Playwright sidecar is not yet a qualified credential consumer.
`playwrightLibraryEnvironment` copies the parent environment, and the sidecar
does not set `chromiumSandbox`. Playwright documents that Chromium sandboxing
defaults to disabled. Before enabling the credential path:

- Build a bounded child environment from an operator-owned allowlist rather
  than copying the broker environment. Exclude vault tokens, service-credential
  directory variables, IPC authentication material, loader overrides, and
  arbitrary runtime startup options. Forward only explicitly qualified proxy
  and browser configuration, never a model-provided command or environment.
- Require Chromium sandboxing and verify a real launch under the service's
  deployment restrictions. Do not retry with `--no-sandbox`, accept arbitrary
  launch flags, or silently use an unqualified browser when startup fails.
- Keep broker credential files and IPC keys outside the browser child's
  filesystem view. A clean environment alone does not make files unreadable.
  Verify this separately from the denial checks for agent shell/file tools.
- Use a private credential-only context/profile. Disable inherited recording,
  downloads, storage export, raw execution, and diagnostics until their safe
  output contracts are admitted. Qualify lease checks and output projection
  before loading a token or retrieving a login item.

On Linux, evaluate `LoadCredentialEncrypted=` with a private service mount
namespace as the preferred token-delivery path. Systemd decrypts credentials
at activation and exposes them to service code as files; this does not replace
separate OS identities or protect against a trusted service compromise.

Sources:

- `pkg/browser/playwright_library_client.go`
- `runtime/browser/playwright-library/sidecar.cjs`
- [Playwright Chromium sandbox option](https://playwright.dev/docs/api/class-browsertype#browser-type-launch-option-chromium-sandbox)
- [Systemd service credentials](https://github.com/systemd/systemd/blob/main/docs/CREDENTIALS.md)

## Provisioning and lifecycle

Prefer an OS service credential facility with encryption at rest. A fallback
token file requires strict ownership, private parent directories, no symlink
traversal, a bounded regular-file read, and no agent-visible filesystem mount.
Accept the token only through a protected local setup path, never chat, argv,
shell history, a model-visible environment, or debugging output. Do not load a
token until process-isolation and provider-readiness checks pass.

Review the existing service account's dedicated-vault read-only permissions in
the authenticated 1Password management UI during setup. The documented CLI has
creation and rate-limit subcommands, not a general permission-inspection
command. Do not invent a scope verification API or test write permissions by
making a live write. The local adapter remains read-only regardless.

Serialize policy replacement, pending approval invalidation, and browser lease
revocation as one service control operation. Returning to an earlier canonical
policy must not resurrect pending phone challenges. CA1 checks the current
revision and clears live grants, but deliberately does not authenticate or own
the future challenge store.

The browser consumer captures the exact immutable binding and its revision,
checks the grant before provider access, and rechecks before injection. It
cannot look up a replacement binding under an already-authorized request.
Readiness and authority must be established before retrieving a password;
there is no useful reason to fetch a real secret without a qualified consumer.

Sources:

- `pkg/isolation/platform_linux.go`
- `pkg/isolation/README.md`
- [Service-account security](https://www.1password.dev/service-accounts/security)
- [Documented service-account CLI operations](https://www.1password.dev/cli/reference/management-commands/service-account)

## Reviewable implementation slices

1. Provider adapter and safe credential container, tested entirely with fake
   item APIs; no gateway imports and no public transport.
2. Protected provisioning and standalone service startup with mandatory
   identity/path/isolation checks, disabled unless explicitly configured.
3. Authenticated, bounded typed operation transport and independent runtime
   tests for identity substitution, secret output, cancellation, and errors.

Phone approval and credential-browser execution retain their CA3/CA4
admissions. A transport with no qualified consumer returns unavailable rather
than exporting a secret to make a partial implementation appear functional.

## SDK initialization rejection probe

A temporary qualification test exercised the private adapter's real SDK
initialization with a deliberately invalid synthetic token. It returned the
fixed unavailable error. Captured stderr was empty, and neither captured stream
contained the raw, base64, or hex canary. The probe file was removed after the
run; no real token, vault, or item retrieval was used. This is evidence for one
rejection path, not qualification of successful SDK retrieval or all diagnostic
paths. Service isolation and CA6 output tests remain required.

## CA2a adapter implementation

The first independent CA2 slice lives under
`cmd/mintclaw-credential/internal/vault`. Go's internal-package rule prevents
the gateway and model tools from importing its credential retrieval API.
It depends on the pinned SDK, not the CA1 policy package. No public listener,
service executable, token loading, model tool, or live credential access is
introduced by this slice. Wiring it to a consumer still requires CA1 and the
remaining service/phone/browser gates.

The adapter validates exact returned vault/item IDs and the Login category,
built-in username/password field IDs and types, duplicates, value bounds, and
single-line UTF-8 input. Extra item fields are never exposed to the consumer.
Retrieval is serialized with cancellation while queued and a bounded request
deadline. Errors and panics are projected without their original text.

A private login container supports one synchronous consumption, safe generic
formatting and serialization, explicit destruction, and clearing its owned
byte buffers on success, error, or panic. Consumers must not retain borrowed
buffers. This cannot guarantee erasure of copies held by the SDK, Go strings,
or the browser, or cancel an injection already in flight.

Validation uses fake item APIs and synthetic canaries; provider authentication
and a real isolated deployment remain pending. The SDK also builds against
MintClaw's resolved Wazero `v1.12.0` and `x/sys v0.48.0` for the four recorded
targets in the separate qualification checkout.

## Linux host readiness observation

Read-only observation on `server@oc`, 2026-10-11 at 06:51–06:55 UTC:

- Deployed core: `422300d7c5401651da945ff5fcd11ceffa987b77`.
- Kernel: `7.0.0-30-generic`; systemd: `255.4-1ubuntu8.17`.
- The main gateway is a user service under operator UID/GID `1000`. The
  operator belongs to the sudo group and has a shell authorization rule.
  Its observed service properties are `NoNewPrivileges=no`,
  `PrivateUsers=no`, `ProtectSystem=no`, and `ProtectProc=default`.
- No production service or configuration changed. No vault token, private
  environment, or full configuration was displayed or copied.

These properties do not qualify the credential host. Agent execution and
file authority require separate evidence. A new broker UID alone cannot
protect secrets if an agent retains root, operator impersonation, or
service-management authority on that host. Before real provisioning, qualify
the complete confined execution path or use a credential/browser host outside
those capabilities. Preserve existing administration flows while preparing
that boundary; do not silently revoke operator privileges during setup.

A private socket-pair probe passed on the actual server: kernel
`SO_PEERCRED` matched the connected process, and `SO_PEERPIDFD` returned a
live close-on-exec handle. This supports the intended caller-authentication
mechanism. The private transport fixtures below exercise delegated descriptor
denial, dead-peer handling, and typed requests. Trusted process-role binding
and complete deployment integration remain pending on the actual host.

A temporary disposable Linux fixture also demonstrated service-UID-only
credential ACL access. A distinct agent UID was denied credential-file,
broker environment, process-memory, and process-control access. Its overlay
filesystem supported ACLs; its tmpfs did not. Systemd may use service ownership
with a read-only mount when ACLs are unavailable. Check the actual descriptor,
ACL/ownership authority, mount protection, and bounded input together; an
owner/mode check alone is insufficient for the loader admission.

An attempted disposable systemd activation exited before starting a service.
It supplied no credential and qualifies no activation path. Actual systemd
credential delivery, mount isolation, browser-child filesystem isolation, and
provider-readiness-before-retrieval remain open gates. Keep them explicit in
the next CA2 merge unit and keep runtime credential access disabled.

Sources:

- [Linux v6.12 socket peer identity implementation](https://github.com/torvalds/linux/blob/v6.12/net/core/sock.c)
- [Systemd v252 credential ownership and ACL implementation](https://github.com/systemd/systemd/blob/v252/src/core/execute.c)

## CA2 private intent transport

`pkg/credentialbroker/intent` defines the shared, versioned request/response
contract and its Linux client. It has no 1Password SDK dependency. Requests
carry account aliases, bounded approval duration, and opaque request/grant
references. They cannot choose a principal, provider item, URL, selector,
command, or secret. Responses admit fixed outcomes and controller-owned
references, never provider errors or browser diagnostics. References locate
service state; possession does not authorize a grant or session.

`cmd/mintclaw-credential/internal/transport` owns the private handler boundary.
Its caller configuration is trusted startup authority, not evidence that a
process is a gateway. Before calling `PinCaller`, startup must prove the
supervised process role, separate OS identities, and denied agent access to
process control, service management, and credential storage. A saved PID file,
model-selected PID, or same-UID process is insufficient. Restarting the caller
requires fresh trusted admission; an exited process's pin is never rebound.

The Linux transport requires a live process handle, `SO_PEERPIDFD`, matching
kernel UID/GID/PID, and matching per-message `SCM_CREDENTIALS`. It admits one
bounded sequenced packet per connection. Descriptor handoff and received
rights are rejected; received descriptors are made close-on-exec atomically
and closed before rejection. Exact JSON keys, duplicate rejection, field
bounds, and operation-specific response validation prevent free-form output.
Private handler errors, panics, and invalid responses become fixed failures.

Calls have a five-second deadline and a shared eight-call capacity. Caller
exit, client disconnect, cancellation, or service shutdown invalidates the
borrowed call and cancels its handler. A trusted handler must honor that
context and acknowledge private side effects; transport cannot forcibly stop
Go code or undo an injection already in flight. The client makes one attempt
and does not replay an uncertain result. Grant/policy/origin/browser-lease
checks remain required immediately before every private consumer operation.

Startup owns the Unix listener and must establish protected path permissions
and sender-credential reception before publishing readiness. The client needs
a protected socket path and qualified dedicated server identity. This slice
creates no production listener, loads no token, invokes no provider, and adds
no model tool. `ready` requires a qualified service handler and consumer;
the existence of a transport is insufficient. Linux fixtures use synthetic
inputs and unprivileged identities. They do not qualify systemd activation,
host confinement, Android approval, or credential-browser execution.
