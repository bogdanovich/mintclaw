# Protected Credential Approval Service

> Historical component record from the superseded isolated-runtime design.
> The operator-approved [trusted-runtime design](credential-trusted-runtime-design.md)
> and [current roadmap](credential-access-roadmap.md) govern new work.
> Separate-host, VM, Root-helper, service-role, and process/network isolation
> requirements below are no longer deployment or rollout gates. Reusable
> component behavior and its exact qualification evidence remain recorded here.

`mintclaw-credential serve-approval` runs the existing Android passkey page,
approval controller, and protected initial-enrollment console as a separate
Linux service. This command does not load a 1Password token or start a provider,
credential browser, model tool, or agent-intent listener. Credential requests
and generic phone notifications still need separate integration.

## Protected startup

Root supplies `/etc/mintclaw-credential/approval.json`. The executing identity
must match the local `mintclaw-credential` account and the configuration's
nonzero UID and GID. Before reading configuration, startup requires matching
real/effective IDs, no supplementary group beyond its primary group,
NoNewPrivileges, empty effective/permitted/inheritable/bounding/ambient
capabilities, and disabled core/process dumps. Token delivery environment and
loader/runtime overrides are rejected with a fixed error.

The configuration and TLS certificate/key are Root-owned regular files with
one link, the service's group, mode 0440 or 0640, no extended access ACL, and
a size of 1–65536 bytes. Root ancestors must be protected from group/other
writes, symlinks, and extended ACLs. Startup pins each inode before reading
and checks its identity, size, modification metadata, and cancellation after
the bounded read. The service cannot write these files.

The strict JSON schema admits only these top-level fields:

| Field | Meaning |
| --- | --- |
| `version` | Schema version 1 |
| `service_uid`, `service_gid` | Dedicated nonzero Linux identity |
| `owner_id` | Immutable enrolled owner alias |
| `origin` | One canonical HTTPS DNS origin |
| `listen_address` | Canonical numeric IP and unprivileged TCP port |
| `state_directory` | Protected service-owned owner-store directory |
| `certificate_file`, `private_key_file` | Protected Root TLS files |
| `accounts` | Existing immutable account/origin/principal/login-flow policy |

Duplicate keys, case aliases, unknown fields, trailing values, excessive
nesting, and principals belonging to another owner are rejected. The schema
contains no token, token-source path, provider switch, or caller-selected file.
The account policy is loaded once; a restart restores no grants.

## Native TLS and enrollment

The configured certificate must match the origin and be currently valid.
The listener serves TLS 1.3 and HTTP/1.1 with exact SNI matching, bounded
connections, headers, reads, writes, and idle time. The existing phone and
enrollment boundaries independently validate Host, Origin, method, path,
body, and passkey ceremony. Direct dispatch preserves the original request
path so a general-purpose HTTP mux cannot normalize or redirect it first.
Raw server diagnostics are discarded.

Root may forward raw TCP from the public origin's port to the configured
unprivileged port. A proxy that terminates TLS and substitutes forwarded
headers is not admitted by this native TLS boundary. The hostname must be
reachable from Android and its certificate trusted by the phone; synthetic
CI certificates do not qualify that deployment.

Before an owner is active, the service creates a mode-0600 Unix packet socket
inside `/run/mintclaw-credential-owner`, a service-owned mode-0700 directory
under protected Root ancestors. The directory remains pinned throughout its
lifetime. The existing Root-only `mintclaw-credential enroll` command verifies
the service creator and uses a private terminal for the single-use invitation.
A Root-created socket-activation endpoint cannot replace the service-created
socket. Normal shutdown removes only its own verified socket inode; a stale
or replaced socket denies startup/cleanup and requires protected repair.

Cancellation withdraws controller authority before draining HTTP and console
work. Shutdown is bounded, closes remaining connections after the grace
period, releases the owner-store lock, and permits a clean restart.

## Qualification and remaining gates

`scripts/qualify-credential-approval-service.sh` runs in Linux CI with generated
synthetic TLS material, disposable volumes, no external network, and the
actual public startup function. The service container starts as UID/GID
65532 with no capabilities and inherited NoNewPrivileges. Its fixture covers
unsafe Root files, native TLS resources, malformed/foreign-origin requests,
wrong SNI despite a matching synthetic certificate, non-Root console denial,
socket cleanup, and repeated shutdown/restart. Existing approval fixtures
separately cover Root console admission and signed passkey ceremonies.

These checks qualify software behavior in the disposable role. Production
activation still requires an isolated host outside **all** MintClaw and
companion SSH/root/cloud administration reach, protected executable/modules,
the actual service-manager restrictions and TLS route, physical Android
enrollment/approval/revocation, and full credential-consumer qualification.
The current agent-administered host does not meet that isolation contract.
The 1Password token must be entered by the operator through protected setup
after those gates; it must never pass through chat or agent-visible tools.

See the [credential access roadmap](credential-access-roadmap.md),
[phone verifier qualification](credential-phone-approval-qualification.md),
and [browser login admission](credential-browser-login-admission.md).
