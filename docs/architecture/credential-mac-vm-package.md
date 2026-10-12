# Protected Mac Credential VM Package

> Historical component record from the superseded isolated-runtime design.
> The operator-approved [trusted-runtime design](credential-trusted-runtime-design.md)
> and [current roadmap](credential-access-roadmap.md) govern new work.
> Separate-host, VM, Root-helper, service-role, and process/network isolation
> requirements below are no longer deployment or rollout gates. Reusable
> component behavior and its exact qualification evidence remain recorded here.

This package prepares the fixed host launcher for the
[existing Mac candidate](../security/credential-mac-vm-host-plan.md). It does
not install a service account, start a guest, read a key, or qualify that
Mac's administration boundary. The guest credential broker and private
browser still require independent Linux qualification.

## Build and public qualification

On macOS 13 or newer, `bash scripts/build-credential-mac-vm.sh` accepts no
arguments and prints the fresh private output directory. It compiles the
public Swift sources against the installed Apple SDK for the current CPU,
applies an ad hoc hardened-runtime signature with the virtualization
entitlement and no debugger entitlement, and creates `SHA256SUMS` for the
binary, disabled service definition, and entitlement file. No download,
installer, VM boot, or secret provisioning runs. Protect and review the
selected build before any later Root installation; these checksums do not
independently authenticate a build or Apple toolchain.

`bash scripts/qualify-credential-mac-vm-inputs.sh` builds that package and
executes disposable public fixtures as an ordinary user. It checks descriptor
close-on-exec, bounded hashing, owner/mode/path/symlink/hardlink rejection,
Darwin ACL handling, strict manifest parsing, caller argument rejection, and
ordinary-user startup denial. It removes its package and fixture directory.
The macOS portability CI job runs this qualification. These tests create no
protected installation and are not an actual dedicated-account VM boot test.

## Fixed installation contract

Later operator-reviewed installation must use these paths and ownerships:

| Object | Required owner/group and mode |
| --- | --- |
| `/Library/PrivilegedHelperTools/mintclaw-credential-vm` | Root:wheel, `0555`; protected ancestors and no ACL allow entry |
| `/Library/LaunchDaemons/org.mintclaw.credential-vm.plist` | Root:wheel, `0644`; protected ancestors, no ACL allow entry, remains disabled |
| `/Library/Application Support/MintClawCredentialVM/boot` | Root:`_mintclawcredvm`, `0750` |
| `boot/manifest.v1` | Root:`_mintclawcredvm`, `0640`, single-link regular file, at most 512 bytes |
| `boot/vmlinuz` and `boot/initrd` | Root:`_mintclawcredvm`, `0440`, single-link regular files with manifest SHA-256 |
| `/private/var/db/mintclaw-credential-vm` | Root:`_mintclawcredvm`, `0750` |
| `guest.raw` in that state directory | `_mintclawcredvm`:`_mintclawcredvm`, `0600`, single-link regular file, exact manifest length |

The launcher admits only the named service UID and GID, distinct from `ab`
and Root and outside staff/admin groups. Supplementary groups must contain
only the service GID. It rejects token, injected-library, and SSH-agent
environment variables and disables core files. The launchd definition fixes
the executable and `serve` argument, user/group, no group initialization,
private umask, null standard streams, zero core limits, a ten-second stop
timeout, and no automatic start or restart. Actual launchd group behavior
under the dedicated account must still be tested.

The descriptor walk anchors at `/`, rejects symlinks and noncanonical paths,
and requires Root-owned ancestors without group/other write or special mode
bits. Ancestor groups may be wheel, admin, or the service group; admin-group
ownership of a read-only ancestor grants no replacement right. Final directories
require the exact service group and `0750`. Any extended ACL allow entry
denies; Root's customary deny-delete ACL is permitted. The service cannot
replace the Root-owned containing directories or immutable boot files.

The launcher retains pinned input descriptors and an exclusive disk lock
for its entire lifetime. The framework receives only fixed paths under those
Root-protected directories. Host Root remains trusted and can replace them;
descriptor pins are not a defense against a malicious administrator.

The boot manifest is exactly five ASCII lines followed by a newline:

```text
mintclaw.credential_vm_boot.v1
x86_64
<64 lowercase hexadecimal kernel SHA-256 characters>
<64 lowercase hexadecimal initrd SHA-256 characters>
<canonical decimal raw-disk byte length>
```

Use `arm64` on an Apple Silicon build. Disk length must be 4–128 GiB.
This manifest authenticates immutable boot inputs under trusted Root; it
does not authenticate a guest installation or its mutable disk contents.

## Guest shape and activation gates

The launcher fixes two CPUs, 4 GiB RAM, a matching-architecture Linux boot
loader with `root=/dev/vda rw panic=1`, one raw virtio disk with fsync
synchronization, one NAT interface, a fixed MAC, and entropy. It exposes no
shared host directory, serial output, display, input device, clipboard,
vsock control, snapshot, or caller-selected VM setting. `validate` and
`serve` are its only arguments; errors emit one fixed message.

NAT alone does not provide Android ingress or qualify guest network policy.
The guest image, protected operator-only provisioning/admin path, stable
HTTPS route, guest identities/services, actual browser runtime, backup and
memory protection, and complete companion/agent administration audit remain
required. No real token belongs in the image or this build package. Sleep or
process loss makes the service unavailable; no automatic grant renewal or
login replay is provided.

Before activation, prepare the concrete empty-guest installation and rollback
change set described by the host plan. Qualify it with synthetic data under
the actual service identity. After real provisioning, revoke grants and the
1Password service account before removing protected guest state.
