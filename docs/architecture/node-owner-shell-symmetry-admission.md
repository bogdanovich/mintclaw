# Node Owner-Shell Cross-Platform Symmetry Admission

## Status And Decision

This document admits one bounded follow-up to the completed Node Companion P1
owner-control slice. It makes the two existing owner-shell deployment choices
available on both supported desktop/server operating systems:

- `local_user` on Linux and macOS; and
- `privileged_helper` on Linux and macOS.

Both choices continue to expose the same model-facing `shell.exec.v1` command,
the same target/profile policy, durable approval, invocation identity, WSS,
result, recovery, no-replay, and redacted-event paths. Platform code may differ
only where process containment, local IPC, or service lifecycle genuinely
differs.

Symmetry means that an operator can choose either authority mode on either OS.
It does not mean claiming capabilities that an OS cannot prove. In particular,
the initial macOS privileged helper does not advertise interactive terminal or
confirmed cancellation support. Linux keeps its existing cgroup-backed PTY and
confirmed process-domain cancellation guarantees.

The admitted work is a short dependent PR sequence:

1. this docs-only contract;
2. Linux `local_user` support by sharing the existing Unix same-user executor;
3. a macOS privileged helper and its exact LaunchDaemon lifecycle; and
4. cross-platform proof, accurate operations docs, and deployment evidence.

If the macOS helper cannot remain an exact owner-shell component, requires a
generic privileged-operation framework, or requires changing the durable
gateway invocation state machine, implementation stops for an architecture
checkpoint.

## Why This Follow-Up Exists

P1 originally chose the smallest useful implementation for each immediate
deployment: a root authority broker on Linux and a same-account shell on
macOS. That left the product shape unnecessarily asymmetric:

- a Linux personal node could not choose a broad shell as its ordinary service
  account without installing the root broker; and
- a macOS personal node could not deliberately grant an administrator shell
  while keeping the network-facing companion unprivileged.

Those are configuration choices, not fundamentally different capabilities.
The model should reason about one shell command and discovered facts, while
the operator chooses its OS identity and trust boundary out of band.

This work does not broaden `system.exec.v1`. The constrained typed executor
remains the recommended delegated/product surface. `shell.exec.v1` remains an
explicit owner-control capability whose blast radius is the configured OS
identity.

## Fixed Capability Matrix

| OS | Executor | OS authority | Confirmed cancellation | Agent PTY | Initial status |
| --- | --- | --- | --- | --- | --- |
| Linux | `local_user` | Companion service account | No | No | Admitted by this follow-up |
| macOS | `local_user` | Companion service account | No | No | Already implemented |
| Linux | `privileged_helper` | Exact helper-owned profile, including UID 0 | Yes | Yes | Already implemented |
| macOS | `privileged_helper` | Exact helper-owned profile, including UID 0 | No | No | Admitted by this follow-up |

These facts are independent:

- `local_user` versus `privileged_helper` selects where OS authority resides;
- `supports_cancel` states whether the executor can prove the entire admitted
  process domain is gone;
- `supports_terminal` states whether the executor implements the existing
  terminal protocol; and
- approval mode is route/target/profile policy, not an executor property.

Discovery must publish the real values. No implementation may infer terminal
or cancellation support merely because a profile is privileged, or infer
privilege merely because a terminal exists.

## Authority And Configuration

Owner shell remains absent and deny-all by default. An enabled node config
selects exactly one executor. The public configuration vocabulary becomes:

```json
{
  "owner_shell": {
    "enabled": true,
    "local_user": {
      "revision": "owner-user-v1",
      "profile": "owner-user",
      "shell_path": "/bin/zsh",
      "login": true,
      "working_scopes": {"home": "/Users/operator"},
      "fixed_environment": {},
      "permitted_environment_names": ["LANG"],
      "timeout_seconds_max": 240,
      "output_bytes_max": 131072,
      "concurrent_commands": 2
    }
  }
}
```

or:

```json
{
  "owner_shell": {
    "enabled": true,
    "privileged_helper": {
      "endpoint": "/run/mintclaw/node-authority-broker.sock"
    }
  }
}
```

`endpoint` is required on Linux. On macOS the `privileged_helper` object is
empty: the exact LaunchDaemon install supplies a private inherited descriptor,
and configuration rejects a caller-selected pathname. These are operator and
lifecycle settings and are never model or gateway arguments.

The existing Linux `broker_socket` field is renamed to `privileged_helper` as
an explicit operator migration. There is one current config contract: the new
binary does not retain a second legacy runtime path or a protocol-version
compatibility shim. Deployment must back up and atomically update the existing
Linux config with the binary so it does not fail halfway between schemas.

Exactly one executor is valid. Disabled or absent `owner_shell`, an unknown
executor, mixed executor fields, an unavailable platform implementation, or a
helper whose snapshot cannot be authenticated fails closed before the node
advertises `shell.exec.v1`.

The model may select only the discovered target alias, profile alias, working
scope, permitted environment values, bounded script, and timeout. It cannot
select an executable shell, UID, GID, supplementary groups, helper endpoint,
service label, child binary, config path, or credential.

## Shared Execution Contract

All four matrix entries reuse the current shell contract:

1. fresh node discovery projects one bounded model contract;
2. gateway target policy authorizes the exact target and profile;
3. durable approval binds the prepared script, scope, environment, timeout,
   actor, agent, route, session, tool call, execution identity, catalog, and
   authority digest;
4. the companion revalidates its current policy and executor snapshot;
5. the selected executor runs one non-interactive shell process; and
6. the existing invocation ledger reports a durable result or explicit
   unknown outcome without automatic replay.

Changing executor, profile revision, UID/GID, shell, working scopes,
environment policy, or effective limits changes the authority digest and
invalidates stale discovery and approval.

Shells receive only fixed profile environment plus explicitly permitted input.
The companion's service environment is not inherited. A login shell may load
the configured OS user's normal startup files; that behavior is explicit in
the profile and is not a security boundary. Working scopes choose an initial
directory and do not claim filesystem sandboxing.

`system.exec.v1`, jobs, coding, files, services, browser, updates, and remote
workspace retain their existing policies and implementations. None silently
falls back to owner shell.

## Linux `local_user`

Linux reuses the current same-user Unix executor rather than acquiring a new
daemon or protocol. The implementation derives effective UID and GID from the
running companion, validates the same bounded profile fields, creates a fresh
process group, bounds output and concurrency, and reports the same conservative
outcome rules as macOS.

Linux `local_user` deliberately does not reuse the privileged Linux broker.
Requiring a root daemon merely to run as the already-running account defeats
the purpose of this mode. It also does not acquire cgroup ownership, so it must
not advertise confirmed cancellation or terminal support.

On normal completion, an observed surviving child, retained output pipe, or
uncertain process observation produces `UNKNOWN`. On timeout, cancellation,
disconnect, or companion loss after start, the executor makes a best-effort
process-group termination attempt and reports `UNKNOWN`; it never claims the
Linux broker's cgroup proof.

## macOS `privileged_helper`

### Boundary

The full companion remains an unprivileged process. A root LaunchDaemon owns
one exact owner-shell profile and runs only the narrow authority-broker
protocol. It does not parse model messages, connect to the gateway, read chat
attachments, expose file/service/update verbs, or accept arbitrary operation
names.

The LaunchDaemon supervises the exact configured unprivileged companion
instance and passes it a private inherited local capability channel. There is
no connectable root-shell socket available to arbitrary processes running as
the same macOS user. The daemon configuration fixes:

- the companion executable and configuration paths;
- the unprivileged child UID, GID, and supplementary groups;
- the single helper profile and revision;
- shell executable, login behavior, working scopes, environment policy,
  timeout/output/concurrency ceilings, and target UID/GID/groups; and
- the managed launchd label and state locations.

The daemon verifies that its executable, configuration, child executable, and
their directory chains are absolute, non-symlink, root-owned, and not writable
by group or other. It drops the child to the configured unprivileged identity
before exec. Failure to establish the private channel or exact identity keeps
the node offline instead of starting without the requested authority.

The inherited channel is an implementation detail, not a new node transport.
It carries only the existing bounded authority-broker snapshot and execute
frames. It cannot name a new profile or expand authority. A disconnected child
cannot reconnect through a public pathname; only a newly supervised exact
child receives a new channel.

### Execution truth

The helper starts a new process group under the fixed profile identity. macOS
does not provide the cgroup/subreaper containment proof used by the Linux
broker, so this slice makes no claim that an arbitrary shell cannot detach a
descendant.

Consequently:

- normal leader completion with no observed surviving process-group member may
  return the bounded exit result;
- a timeout, cancellation, channel loss, helper restart, or ambiguous child
  observation after start returns `UNKNOWN`;
- the helper performs best-effort process-group cleanup but advertises
  `supports_cancel: false`;
- it rejects terminal-open frames and advertises `supports_terminal: false`;
  and
- neither gateway nor companion automatically replays an invocation that may
  have started.

Adding macOS PTY or confirmed descendant cancellation requires a separate
evidence-backed admission. It is not a review-time extension of this work.

### Lifecycle and updates

The lifecycle command may install the privileged mode only in system scope and
only with an explicit service account. Install, status, uninstall, rollback,
and ownership checks extend the existing managed launchd transaction rather
than adding a second installer.

The first slice does not combine macOS `privileged_helper` with the P4 managed
update coordinator. The lifecycle command fails closed if both are requested.
Supporting safe transfer of the private capability across coordinator-managed
payload replacement is a separate composition problem; silently falling back
to a public socket, running the complete companion as root, or weakening update
identity is forbidden. Existing macOS `local_user` managed-update deployments
remain unchanged.

## Install, Migration, And Rollback

- Existing Linux privileged-helper configurations continue to work.
- Existing macOS `local_user` configurations continue to work.
- No existing install is converted automatically.
- Linux `local_user` requires only companion configuration and the ordinary
  user/system service lifecycle.
- macOS `privileged_helper` requires an explicit system install by root and a
  root-owned helper configuration.
- Switching modes is an operator migration: stop the instance, back up config
  and lifecycle files, install/validate the new mode, then reconnect and verify
  discovery before enabling target policy.
- Rollback restores the previous config, plist/unit, and binaries together.
  A partial rollback that leaves a privileged helper reachable is failure.

The helper binary is shipped in the normal release artifact matrix. It does
not download itself, modify the companion update trust root, or become a
general package manager.

## Events, Secrets, And Retention

Existing shell invocation events remain the audit surface. They may record
target/profile aliases, executor mode, policy revision, authority digest,
invocation identity, bounded timestamps, outcome class, exit code, truncation,
and truthful cancellation/terminal support.

They must not retain raw scripts, stdout/stderr, environment values, startup
file contents, local IPC bytes, credentials, or unrestricted filesystem paths.
The helper writes bounded operational logs without request payloads. Existing
event retention and diagnostic trace policy apply; this work creates no audit
database, replay evaluator, or second ledger.

## Validation And Proof Matrix

| Requirement | Required evidence |
| --- | --- |
| One model surface | Identical `shell.exec.v1` schema and gateway path for all four executor/OS combinations |
| Linux local user | Native Linux real-process success, environment isolation, output bound, concurrency bound, timeout/child uncertainty, and disabled-default tests |
| macOS local user regression | Native macOS focused tests and existing live canary remain healthy |
| Linux privileged regression | Broker race tests, cgroup identity/cancel tests, and PTY tests remain healthy |
| macOS privileged boundary | Native tests for root-owned paths, exact supervised UID/GID, private inherited channel, no public socket, malformed/oversized frames, and child reconnect denial |
| macOS outcome truth | Success plus timeout, disconnect, helper restart, detached-child, and no-replay cases report only provable result or `UNKNOWN` |
| Capability truth | Discovery matrix proves cancellation and terminal flags independently of privilege mode |
| Policy binding | Changed executor/profile/revision/arguments invalidate stale discovery and approval |
| Lifecycle | LaunchDaemon install/status/uninstall/rollback tests and a real native system-scope canary |
| Compatibility | Existing Linux privileged, macOS local-user, update, files, jobs, coding, browser, and workspace focused suites remain green |
| Deployment | Merged-main Linux and macOS binaries, config backups, healthy reconnect, descriptor inspection, and noninteractive shell canaries |

Run focused race tests for touched companion, broker, lifecycle, invocation,
approval, and WebSocket packages; tagged lint; relevant broad tests; and CI.
Native Linux behavior must be proven on Linux and native Darwin lifecycle and
process behavior on macOS. Cross-compilation alone is not proof.

## PR Boundaries And Checkpoints

### PR 1: architecture admission

This document and roadmap pointers only.

### PR 2: Linux same-user executor

Share the current Unix same-user implementation across Linux and macOS, add
Linux-focused tests, and keep descriptor semantics conservative. Do not touch
privileged lifecycle or protocol.

### PR 3: macOS privileged helper

Add only the Darwin authority-broker adapter, private supervised channel, and
the exact LaunchDaemon lifecycle needed to run it. Reuse the existing profile,
frame, shell, invocation, and discovery contracts. Do not add PTY, confirmed
cancellation, generic helper verbs, or managed-update composition.

### PR 4: proof and operations

Run the native real-process matrix, update accurate configuration/operations
docs, deploy from merged `main`, and record rollback and health evidence. Code
changes are allowed only for a defect revealed by proof and should be split
into a focused implementation PR when material.

After four substantive review/fix cycles, the same invariant challenged three
times, production code doubling from a PR's published baseline, or a fix
crossing into a second unplanned subsystem, stop patching and perform the
autonomous workflow architecture checkpoint.

## Explicit Non-Goals

This slice does not add:

- another model command, gateway store, WSS message family, invocation ledger,
  approval mechanism, or audit subsystem;
- automatic choice or fallback between executors;
- a general sudo, RPC, plugin, service-management, file, update, or privileged
  helper framework;
- caller-selected UID/GID, shell path, helper path, socket, credential,
  service label, executable, or configuration path;
- macOS interactive PTY or confirmed descendant cancellation;
- P4 managed-update composition for the macOS privileged mode;
- a root-run complete companion;
- Windows, mobile, containers, SSH bootstrap, fleet rollout, or policy UI; or
- broader `system.exec.v1`, jobs, coding, browser, file, or workspace authority.

## Definition Of Done And Mandatory Stop

This follow-up is complete only when:

- both executor modes are explicitly selectable and deny-by-default on Linux
  and macOS;
- all four cells use one `shell.exec.v1` model and durable invocation path;
- discovery truthfully reports cancellation and terminal support;
- Linux `local_user` and macOS `privileged_helper` have native real-process
  proof, while both existing cells retain regression proof;
- macOS privileged execution keeps the companion unprivileged, has no public
  same-UID root-shell endpoint, and returns `UNKNOWN` whenever completion
  cannot be proved;
- policy, approval, identity, no-replay, redaction, lifecycle, rollback, and
  disabled-default gates are evidenced;
- all implementation PRs are merged, merged `main` is validated, applicable
  Linux and macOS companions are deployed healthily, and docs match behavior.

Immediately stop when those conditions are evidenced. Do not continue into
macOS PTY, stronger containment, managed-update composition, fleet work,
generic helper infrastructure, or any other deferred item.
