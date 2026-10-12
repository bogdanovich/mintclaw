# Private Credential Browser Channel

> Historical component record from the superseded isolated-runtime design.
> The operator-approved [trusted-runtime design](credential-trusted-runtime-design.md)
> and [current roadmap](credential-access-roadmap.md) govern new work.
> Separate-host, VM, Root-helper, service-role, and process/network isolation
> requirements below are no longer deployment or rollout gates. Reusable
> component behavior and its exact qualification evidence remain recorded here.

The standalone credential command's private consumer connects the approval
lease and 1Password adapter to the credential-only Chromium worker. The
executable exposes protected provisioning/enrollment, native phone approval
service startup and version commands. This component does not activate a browser
or provider, or retrieve a real token.

## Ownership and admission

Qualified Root startup supplies an inherited anonymous duplex stream, fixed
worker code and configuration, and a process/cgroup termination operation that
verifies the exact worker has died. The Node entry point accepts only inherited
FD 3; configuration and credentials never travel through argv, environment,
stdin, stdout, public listeners or ordinary browser handles. Startup must keep
agent processes out of both endpoints and prevent descriptor handoff, memory
inspection, process impersonation and modification of the executable/modules.
An arbitrary connection or caller-selected PID is not admitted by this API.

The broker snapshots the canonical flow, normalized origins and original
remaining lifetime from `approval.LoginLease`. Each browser callback checks
the current authenticated owner, actor, agent, execution host, account and
policy revision against that generation, using live origin from the private
worker. Submission also requires the independent browser action policy. No
model-supplied approval boolean substitutes for either check.

The public connection API requires the actual
[enforcing proxy](credential-browser-enforcing-proxy.md) from the same original
lease. Launcher configuration derives its endpoint and single certificate pin
from that object. Before provider retrieval, the broker seals preparation
connections; after the external-effect gate, it permits the one configured POST.
Proxy failure retires the worker, and worker retirement closes the proxy.

The binary protocol bounds every frame to 32 KiB. Fixed command kinds cover
configuration, preparation, readiness, injection, submission and shutdown.
Monotonic callback IDs, strict lengths, bounded pending callbacks and fixed
result bytes reject ambiguous, repeated or unsolicited messages. There is no
page-read, JavaScript, screenshot, cookie, profile or generic driver operation.

## Retrieval and retirement

Login is single-use. The broker checks current form/input authority before
retrieval, then rechecks the grant and live DOM after retrieval. The provider
lends owned username/password buffers to one private injection frame and
clears them after consumption. Private frame copies are also cleared. Runtime,
SDK and browser string copies cannot be guaranteed erased.

Grant retirement cancels provider work and preempts in-flight browser commands.
The worker independently checks its bounded lifetime and broker liveness,
including while idle. Close must be acknowledged within a bounded interval;
the supervisor's verified termination runs even when acknowledgement fails.
Unknown close, failed termination, channel loss or uncertain execution
permanently denies reuse. The model receives only a fixed login outcome;
service-owned session locators require a separate scoped registry.

## Qualification limits

Synthetic Go/Node tests use real Chromium and real CA1 grant lifetimes with a
fake credential source, routed HTTPS pages and fresh profiles. They exercise
private injection, page credential/cookie echoes, stale readiness before and
after retrieval, independent submission denial, identity change, revocation
and process termination. They do not enroll Android, initialize 1Password,
prove actual TLS/DNS/HTTP redirects, qualify the intended restricted launcher,
or prove separate process roles and complete host isolation. The separate real
Chromium proxy fixture exercises actual CONNECT/TLS/HTTP, synthetic credentials
and cookies, exact POST/outcome navigation, and incorrect certificate-pin denial
through a synthetic upstream. Restricted Linux browser qualification runs in CI.

The current host remains unqualified: MintClaw's shell identity has Root
administration access and its sandbox launch probe failed. Full protected browser
startup, network/host isolation and physical phone qualification must pass before
real token access. No diagnostic, screenshot, network trace, artifact or
ordinary-browser observation path is enabled for credential sessions.
