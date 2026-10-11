# Fixed Root Credential Browser Launcher

The private `supervision.Launcher` composes the admitted runtime, fresh
loopback-only network namespace, and atomically attached cgroup forest.
It constructs one fixed program. No model command, executable, PID, profile,
URL, or password is a launcher argument. Main exposes only the in-image
`bootstrap-browser` step; it does not start a Root launcher service, open a
vault provider, or enable agent credential access.

## Protected resources

`OpenLauncher` requires real/effective Root UID and GID. Operator-installed
`mintclaw-credential` and `mintclaw-credential-browser` accounts must have
nonzero, distinct UIDs and GIDs. Deployment must separately prove that these
roles are exclusively service-controlled and outside agent administration.

The fixed lock `/run/mintclaw-credential-supervisor/lock` must already exist:
Root:Root, mode 0600, empty, single link, no ACL or file capability. Pinned
ancestors are Root:Root, without group/other write, special bits, symlinks or
ACLs. Nonblocking exclusive flock prevents concurrent launcher instances.
The launcher never creates or repairs these deployment resources.

Fixed `/usr/bin/nsenter` and `/usr/bin/bwrap` receive the ancestor checks plus
regular executable ELF, single-link and metadata checks. Pinned descriptors
prevent a later pathname replacement selecting a different helper. The
operator must select authentic supported versions and protect their native
dependencies; Root administration remains trusted.

[Runtime admission](credential-browser-runtime.md) independently checks the
complete immutable inventory. Launch additionally requires empty `proc`,
`dev`, `tmp`, and `run` slots and executable ELF `bin/setpriv` and
`bin/mintclaw-credential`. The latter must be the qualified credential binary.
All loaders, native libraries, Chromium resources and modules belong in the
manifest. No host library, home, profile, token or broker directory is shared.

## Allocation and fixed program

Only one allocation is allowed. `Prepare` creates no process; it allocates the
network, mount pin and unnamed Unix stream socketpair. `Endpoints` allows one
export of the listener and broker control endpoint to protected Root startup.
Authenticated Root-to-broker transport follows in a later integration.

Before `Start`, the broker must adopt the listener and construct the enforcing
proxy against its actual original lease and scope. Export alone proves no
proxy or grant readiness. The allocation context is not approval authority;
its 20-hour ceiling is only an additional resource limit. Cancellation must
follow the original lease/caller lifetime without replacement or renewal.

Root executes pinned nsenter in the owned cgroup and fresh network namespace.
Pinned Bubblewrap creates mount, PID, IPC, UTS and cgroup namespaces, binds
only the read-only image as root, mounts private proc/dev and mode-1777 `/tmp`,
and clears the environment. It retains the initial user namespace so
Root-owned image files remain UID 0. Root setup retains only SETUID, SETGID
and SETPCAP for the fixed in-image setpriv. Setpriv switches to the host
browser UID/GID, clears groups, drops inheritable/ambient/bounding capabilities
and sets NoNewPrivileges before the bootstrap. Core dumps are disabled.

The bootstrap checks non-Root identity, groups, zero capability sets,
NoNewPrivileges, core limit, exact fixed environment and inherited objects.
FD3 is the unnamed private Unix stream created by Root; FD4 identifies the
current network namespace, FD5 the read-only root image, and FD6/FD7 the public
helpers. On a locked OS thread, `close_range` closes every FD above 3 before
fixed Node/worker exec. Unsupported checks or syscalls deny. No token or
broker-secret descriptor is in `ExtraFiles`.

## Retirement and qualification limits

Cancellation, explicit retirement or leader exit permanently retires the
allocation. Cleanup kills and verifies the complete forest, then closes Root
control, namespace, listener and image descriptors. Failure quarantines the
launcher. An unsuccessful exit after the initial helper exec is preserved as a
fixed failure even when whole-forest cleanup succeeds. Only SIGKILL after a
requested retirement counts as the expected forced stop; nonzero exits and
other signals remain failures if cancellation races their reaping.
Exported copies remain broker-owned: the broker must close its proxy,
accepted connections and copies before acknowledging complete retirement.
Root closing its descriptors cannot revoke copies in another process.
A retired worker cannot restart or export endpoints again.

Mandatory forest qualification includes real Linux nsenter, Bubblewrap,
setpriv and bootstrap, a read-only synthetic runtime, and a synthetic program
in place of Node. It checks actual host UID/GID, groups/capabilities, private
proc, absent host home/configuration, writable private `/tmp`, unwritable image,
closed setup pins, denied external IPv4/IPv6, and outside serving of the private
loopback listener. It exercises retirement before start, explicit stop,
cancellation and leader exit, plus single-start/export rules. Existing network
and escaped-descendant forest fixtures retain separate mandatory PASS gates.
A second launcher fixture execs the initial helper successfully, then makes
the in-image Node exec fail; successful forest cleanup must still quarantine
future allocations.

This fixture does not qualify actual vendor Node/Chromium, Chromium's own
sandbox witness, production roles, Root/broker IPC, proxy/lease wiring, remote
agent intents, the Mac/VM host boundary, Android or real credentials. These
gates must pass before provider activation or a pilot.
