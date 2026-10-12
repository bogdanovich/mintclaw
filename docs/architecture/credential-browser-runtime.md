# Protected Credential Browser Runtime

> Historical component record from the superseded isolated-runtime design.
> The operator-approved [trusted-runtime design](credential-trusted-runtime-design.md)
> and [current roadmap](credential-access-roadmap.md) govern new work.
> Separate-host, VM, Root-helper, service-role, and process/network isolation
> requirements below are no longer deployment or rollout gates. Reusable
> component behavior and its exact qualification evidence remain recorded here.

## Scope and status

`cmd/mintclaw-credential/internal/runtimeimage` admits a fixed operator-installed
Linux runtime before a future Root launcher executes it. It does not launch a
worker, initialize 1Password, create a credential intent listener, or activate
browser login. Main does not call this package.

Root startup opens only `/opt/mintclaw-credential/browser-runtime`; there is no
model-selected runtime path. All ancestor directories must be Root-owned,
without group/other write access, special permission bits, or access/default
ACLs. The runtime is one read-only, executable tmpfs or SquashFS mount. Every
descendant must retain its original `statx` mount ID; a nested bind mount from
the same device is rejected too.

## Installed artifact contract

Directories are Root:Root `0555`. Regular files are Root:Root `0444` or `0555`,
with exactly one link, no access ACL, and no file capabilities. Symlinks,
devices, sockets, FIFOs, missing files, and unlisted files are rejected. File
inspection uses `O_PATH` before opening data through the still-live inode pin.

`runtime.manifest` is a Root:Root `0444` regular file. Its exact text format is:

```text
mintclaw-credential-browser-runtime-v1
<lowercase SHA-256> <0444 or 0555> <canonical decimal byte count> <relative path>
```

Each record ends in one newline; paths are strictly sorted and unique. No
blank records, whitespace aliases, absolute/traversal paths, or manifest
self-reference are accepted. The inventory must include:

- `bin/node` and `bin/chromium`, executable files with an ELF prefix;
- `runtime/browser/credential-login/worker.cjs`;
- `runtime/browser/playwright-library/node_modules/playwright/package.json`.

Include every other runtime file, including the worker's modules, Playwright
dependencies, Chromium resources and required shared libraries. The manifest
does not certify the contents or the declared vendor versions: trusted setup
must independently select and verify pinned vendor artifacts and the reviewed
worker, including the existing Playwright version contract. An ELF prefix is
a type check, not validation that a binary is safe or usable.

Admission bounds the manifest to 2 MiB, 8,192 files, 512 MiB per file, 2 GiB
total data, and 16,384 total directory entries. Hashing checks cancellation
between bounded reads and uses a 30-second admission context. Unsupported
filesystem/mount-ID evidence denies admission. There is no mutable fallback.

After admission, `Image` owns the original mount descriptor. `BorrowRoot`
duplicates that pin only for Root startup, rechecking the read-only mount
before returning it. Closing the image prevents further borrowing; the caller
owns any previously borrowed descriptor and must close it during teardown.
The future namespace launcher must mount this descriptor, rather than resolve
the installation pathname again. Root administrators remain trusted: they can
replace installations or alter mount controls, so agent Root access on the
credential host remains inadmissible.

## Qualification and remaining gates

`bash scripts/qualify-credential-browser-runtime.sh` runs a race-enabled test
binary in a temporary Root systemd unit with a private mount namespace. It
uses only synthetic files under a new protected `/run` directory and cleans
the mounts and unit. Cases include writable/noexec mounts, altered hashes,
missing/extra files, symlinks, hardlinks, FIFOs, unsafe owners/modes/ACLs,
same-device nested mounts, script executables, and unprivileged startup. No
synthetic executable is actually run; this fixture qualifies artifact
admission rather than Node/Chromium operation. Non-Linux startup always denies.

The full launcher still needs authenticated broker IPC, fixed command and
identity configuration, privilege dropping, filesystem namespaces, private
profiles, enforced network isolation, and complete process-forest retirement.
The actual installed vendor runtime and deployment host remain unqualified.
No password or service-account token belongs in this public runtime image.

See the [browser admission](credential-browser-login-admission.md),
[private channel](credential-browser-private-channel.md), and
[enforcing proxy](credential-browser-enforcing-proxy.md) for the independent
consumer boundaries.
