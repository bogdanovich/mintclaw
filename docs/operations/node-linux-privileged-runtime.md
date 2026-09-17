# Linux Node Privileged Runtime Lifecycle

Use this checklist whenever a Linux node deployment includes a root authority
broker or privileged helper with Unix sockets below `/run`. The node lifecycle
installer manages the companion unit; privileged helper units remain an
operator-owned deployment boundary and must satisfy these additional boot
invariants.

## Volatile runtime ownership

`/run` is recreated at boot. A successful manual start does not prove that a
socket parent will exist after reboot. Make one root service own the shared
runtime directory through systemd rather than creating it by hand:

```ini
[Service]
RuntimeDirectory=mintclaw
RuntimeDirectoryMode=0755
RuntimeDirectoryPreserve=yes
```

The owning service must run as root when the broker and helpers require a
root-owned, non-group/world-writable directory chain. An unprivileged companion
unit must not create that shared directory because systemd would assign it to
the companion account. A root-owned tmpfiles rule is an acceptable alternative
when more than one independent root service owns the lifecycle, but the chosen
mechanism must be installed and tested as part of the deployment.

## Acyclic startup graph

Use one direction for startup:

```text
network-online.target -> root broker -> privileged helpers -> unprivileged node
```

The broker may create sockets consumed by the later services. It must not
`Wants=`, `Requires=`, `After=`, or `PartOf=` the node that waits for its socket.
Declare the node and helpers after the broker instead. Run `systemd-analyze
verify` against the complete affected unit graph before starting it; any
ordering cycle is a deployment blocker.

## Installation and readiness gate

Before mutation, retain a checksum-verifiable compact recovery set containing
the affected configs, units, enablement and service state, and one copy of each
distinct binary digest. Then:

1. install the runtime-directory owner and dependent units;
2. run `systemctl daemon-reload` and `systemd-analyze verify`;
3. enable every required unit and confirm `UnitFileState=enabled`;
4. start the broker, helpers, and node in dependency order; and
5. verify socket ownership/mode, the effective node executable, and gateway
   target reconnection.

`ActiveState=active` is insufficient when `ExecStart` is a shell wrapper or a
socket-wait loop. Confirm that the real `mintclaw-node` process replaced the
wrapper and that fresh node discovery succeeds.

## Mandatory reboot smoke

Keep SSH or another independent recovery path until a controlled reboot proves
the boot topology. After reboot, require all of the following:

- the runtime directory was recreated with the intended owner and mode;
- broker, helpers, and node are active and not restart-looping;
- the real node executable is running and required sockets exist;
- the gateway reports the same node identity/alias as connected;
- a read-only node information request succeeds;
- dependent services remain healthy; and
- failed-unit and bounded warning/error journal checks contain no unexplained
  entries.

If a service is active only because its wrapper is waiting for a missing
socket, treat the deployment as failed. Restore the saved units or correct the
runtime/dependency graph before retrying; do not repeatedly restart the node.
