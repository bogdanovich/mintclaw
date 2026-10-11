# Private credential browser enforcing proxy

The private browser channel requires `egress.LoginProxy` from the same original
approved `approval.LoginLease`. It derives the proxy endpoint and certificate
pin from that object; a caller cannot select a generic forwarding proxy. The
executable does not yet activate this path or initialize a provider.

## Private browser TLS and verified upstream TLS

The proxy owns one startup-supplied listener at `127.0.0.1` on an unprivileged
port. It accepts an exact approved HTTPS CONNECT authority, terminates browser
TLS 1.3 with a fresh per-session key, and parses the enclosed HTTP/1.1 request.
An approved CONNECT does not grant an unrestricted byte tunnel. The SNI, Host,
method, target, headers and body must independently qualify before upstream
DNS or dialing. WebSocket upgrades, generic authorization headers, plaintext
proxy requests and unapproved destinations fail closed.

Chromium makes browser-owned startup requests outside page routing, including
requests that current launcher flags cannot disable. See
[Playwright's upstream investigation](https://github.com/microsoft/playwright/issues/42910).
Foreign CONNECT authorities and plaintext proxy requests receive a fixed denial
and their connection closes without DNS or forwarding. That refusal cannot
change the approved login phase or renew its lease. Errors on an approved
connection retire the entire private proxy. The launcher suppresses the network
time and AI eligibility features while preserving the pinned Playwright 1.63.0
disabled-feature defaults; a library upgrade requires requalification.

The private launcher supplies one canonical SHA-256 SPKI pin for this fresh
key. Chromium's [network switch definition](https://chromium.googlesource.com/chromium/src/+/main/services/network/public/cpp/network_switches.cc)
and [certificate verifier](https://chromium.googlesource.com/chromium/src/+/main/services/network/ignore_errors_cert_verifier.cc)
describe this key exception. The launcher does not enable a general certificate
exception. This flag also allows ordinary valid certificates; it does not prove
that Chromium cannot bypass the proxy. The root supervisor must establish and
qualify that separate network boundary before credential access is activated.

Upstream requests use the [pinned HTTPS transport](credential-https-transport.md)
with ordinary system CA and hostname verification, public DNS address checks,
fixed destination IPs, no environmental proxy, no automatic redirects or
retries, bounded reads, and the same current scope and original grant lifetime.
Production construction accepts no alternate DNS, dialer or trust root.

## Injection and submission lifecycle

1. Preparation allows bounded, bodyless GETs at approved origins.
2. Before provider retrieval, `LockForInjection` closes all preparation
   connections and retires their upstream transports. Subsequent network
   operations fail until the trusted broker admits submission.
3. The broker checks its independent external-effect gate before calling
   `PermitSubmit`. One native form POST may target the revision-bound submit
   URL, with the exact form content type and a bounded nonempty body.
4. A qualified POST response admits one bodyless GET to the exact success or
   human-handoff URL. Redirects cannot select a different destination or method.
   The browser confirms its configured visible outcome marker privately.
5. Later HTTP operations on approved connections, repeated submission, uncertain outcomes,
   cancellation, revocation, policy withdrawal or scope mismatch retire the
   proxy and private worker. A restored scope cannot revive that instance.

Proxy failure closes the browser channel, and worker retirement closes the
proxy. Connection tracking includes idle accepted sockets and hijacked TLS
connections. The proxy has no public page, DOM, cookie, response-body or driver
interface; formatting and JSON/text serialization return a fixed placeholder.
Responses and cookies travel only between the private upstream transport and
the private browser. Raw HTTP, TLS and worker diagnostics are discarded.

## Qualification and remaining activation gates

Synthetic Go tests exercise the actual CONNECT/TLS/HTTP boundary, the one-POST
and one-outcome limits, DNS/dial denial, wrong SNI, incorrect redirects, idle
socket retirement, revocation, scope withdrawal and failure cleanup. A separate
opt-in real Chromium fixture uses only synthetic credentials and cookies to
exercise this proxy with a correct and incorrect certificate pin. CI must run
that fixture with the existing private Go/Node browser qualification.

Neither a loopback listener nor browser proxy flags establish isolation from
other local processes. Activation still requires a root-owned supervisor,
separate broker/browser identities, an immutable browser installation, a
private process tree/profile and network namespace, verified termination, and
proof that ordinary agent tools cannot reach the listener, CDP, profile,
plaintext buffers or direct Internet paths. The current MintClaw host has
administrative agent access and cannot supply that boundary. An isolated host
outside all agent and companion administration access is required.

The first supported flow remains one explicit native POST and one outcome GET.
Facebook, Craigslist and eBay have not been qualified. Authenticated browsing,
remote human handoff, notification delivery, physical Android approval and
the operator's protected token provisioning remain separate roadmap gates.
