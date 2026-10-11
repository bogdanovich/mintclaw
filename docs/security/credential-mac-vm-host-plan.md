# Credential Host on an Existing Mac

## Candidate and current status

The operator proposed `ab@ab-ts` as a place to host the credential keys. A
protected Linux VM on that Mac is a candidate deployment for the
[credential roadmap](../architecture/credential-access-roadmap.md). This is
a setup plan, not an approved host change or a qualified deployment. No VM,
account, access rule, service, real token, or phone enrollment has been created
by this plan. The current credential commands require Linux and deny native
macOS startup.

Read-only observations on 2026-10-11 found the local Mac companion running
as `ab`, UID 501. That account belongs to the admin group; the tested
noninteractive sudo command required a password. The workspace Mac reported
Intel `x86_64`, macOS 26.6.2, and 64 GiB RAM. The proposed `ab-ts` SSH alias did
not resolve from either the workspace or the gateway.

A later gateway probe reached the online Mac at its current Tailnet DNS name
(`ab-2`) as `ab`, UID 501, with admin-group membership. That machine reported
the same architecture, OS version and memory, and the tested noninteractive
sudo command also required a password. Comparing platform-identity hashes
subsequently confirmed that this reachable machine is the workspace Mac;
the hardware identifier itself was not printed. The gateway can use its
normal account. The original alias remains unresolved. Complete administration
reach and a separate service identity remain unqualified: passwordless sudo
denial alone does not establish that all agent administration paths are absent.
These probes changed no host configuration.

The existing VPN VPS is currently unsuitable because MintClaw has admin
access. Reusing it would require a separate reviewed migration that removes
all equivalent access, including credentials that can restore it.

## What lives in the VM

1Password remains the vault. The VM stores the narrowly scoped service-account
token in the protected OS credential store, immutable account mappings,
the phone passkey's public verification material and counter, and the phone
service's TLS key. It does not store the Android passkey's private key or a
second copy of the whole personal vault. Grants last at most 20 hours and
disappear on service restart.

Both the credential broker and its private Chromium worker belong in the
VM, under separate Linux identities. The browser necessarily receives each
selected password and private session cookies during login. Keeping only
the token in the VM while injecting passwords into an agent-administered
browser would expose those values through that browser's process boundary.
Private browser state must be ephemeral and retired with its grant.

The agent receives typed account intents and fixed outcomes. A remote
authenticated intent bridge is still required: the existing Linux Unix
packet transport does not connect the gateway to another host. No raw
password, token, cookie, console, VM-control, or generic browser endpoint is
an agent interface.

## Proposed host boundary

Use a dedicated macOS service account, distinct from `ab`, to own and run the
VM. A Root-controlled service definition, immutable launcher and VM
configuration, private service-account storage, and protected console must
prevent the Mac companion and gateway from reading the VM disk or controlling
its process. Do not put the VM under `ab`'s home or use an `ab`-controlled
Docker, Lima, or desktop VM instance as evidence of isolation.

Select and pin the VM launcher only after a synthetic test proves it runs
under that service identity without requiring an agent-accessible GUI or
control socket. Apple's Virtualization framework supports Linux guests;
that support does not itself establish the ownership or administration
boundary. On an Intel Mac, use a matching x86-64 Linux guest. Start with two
virtual CPUs and 4 GiB RAM as a sizing candidate, then measure the full
sandboxed Chromium fixture before fixing production resources.

Exclude shared home directories, clipboard transfer, host directory mounts,
agent-visible serial output, snapshots, and backups containing the VM's disk
or keys. Restrict VM-control sockets, crash output, console access, and
service restart/replace rights. VM startup files and the hypervisor executable
must not be replaceable through the agent's package-management or file access.
The trusted operator still controls host Root and guest Root; compromise of
either defeats this boundary.

Audit every MintClaw and companion route to host or guest administration:
SSH keys, sudo rules, privileged helpers, service-control APIs, desktop
automation, remote-management credentials, hypervisor controls, backup
access, and cloud/tailnet credentials that can grant new access. A protected
VM is admissible only if those routes cannot obtain its disk, memory,
console, or administration. Removing existing access is a host change and
requires a concrete operator-approved migration, not this planning choice.

## Setup sequence and evidence

| Stage | Work to prepare | Evidence required before proceeding |
| --- | --- | --- |
| 1. Identify the Mac | Resolve the intended machine and inventory the companion's effective access without reading secret contents | Machine identity, exact host/guest admin paths, and an operator-controlled administration path |
| 2. Review host changes | Specify the service account, protected paths, pinned launcher, service definition, guest image, network route, resource limits, and rollback | A concrete change set approved before creating accounts or changing access |
| 3. Create an empty VM | Install only reviewed public software under protected ownership; disable host sharing and agent VM controls | Agent/companion attempts cannot read or replace VM files, access its console, or administer host/guest roles |
| 4. Qualify Linux services | Run synthetic provisioning, owner store, native TLS, caller, provider, sandbox, private channel, proxy, and process-retirement fixtures | Actual systemd credential delivery; separate broker/browser roles; verified sandbox; no direct browser network bypass; complete worker death |
| 5. Qualify the phone | Serve one stable trusted HTTPS DNS origin; enroll Android privately and exercise approval, expiry, revocation and restart | Physical user-verifying passkey ceremonies; original 20-hour bound; immediate session retirement; generic authorized notifications |
| 6. Pilot one account | Verify a dedicated read-only vault and one individually qualified login flow; provision only through the protected operator terminal | CA6 canary checks and one successful operator-selected login with no authentication material in model or durable output |

Guest administration and VM console access remain operator-only. If tailnet
access is chosen, qualify its policy and administrative control separately;
being on Tailscale does not establish that the agent cannot SSH into the
guest. Android must reach the exact HTTPS origin and trust its certificate.
The current native phone listener permits raw TCP forwarding, not replacement
of its TLS boundary by a header-forwarding proxy. The hostname, certificate
issuance, network access rules, and private keys need protected administration.

The real token remains unprovisioned through stages 1–5 and until every CA6
gate passes. An agent-visible SSH session or terminal capture is unsuitable
for pasting it. Synthetic values must be used to qualify the operator's
private terminal and encrypted storage path first.

## Availability and rollback

Mac sleep, shutdown, or lost connectivity makes approval and credential use
unavailable. Do not renew grants or replay uncertain logins after recovery.
Start with synthetic data and choose sleep/restart policy explicitly before
the pilot. Existing ordinary MintClaw sessions can continue independently.

Before real provisioning, rollback stops the new VM service and removes only
its reviewed empty/synthetic resources; preserve existing Mac and gateway
services. After provisioning, first revoke credential grants, stop private
sessions and revoke the service-account token in 1Password before protected
removal. Do not copy VM disks or keys into an agent-managed backup to recover
them later.

## References and limits

- [Apple: Running Linux in a Virtual Machine](https://developer.apple.com/documentation/virtualization/running-linux-in-a-virtual-machine)
  documents Linux guest support and matching the guest CPU architecture.
  The service-account isolation above is this project's deployment contract,
  not a security guarantee supplied by that sample.
- [1Password service-account security](https://www.1password.dev/service-accounts/security)
  documents vault-scoped access and token protection. Phone approval is our
  service's authorization policy; it does not cryptographically lock that token.
- [Protected broker setup](credential-broker-setup.md),
  [native approval service](../architecture/credential-approval-service.md), and
  [browser admission](../architecture/credential-browser-login-admission.md)
  retain their independent qualification gates. This plan completes none of them.
