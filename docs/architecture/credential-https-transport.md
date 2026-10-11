# Private Credential HTTPS Transport

The standalone credential command's private `egress.Transport` supplies the
upstream HTTPS connection primitive for a qualified credential consumer. Its
public constructor requires the actual signed `approval.LoginLease`, one
canonical approved HTTPS origin, and a trusted current-runtime scope reader.
It creates no proxy, listener, provider client, service or model tool. The
executable does not invoke it and no real token access is activated.

## Connection authority

Every request, DNS/dial transition, TLS admission and response read checks
the complete current owner, actor, agent, execution host, account, revision
and original grant generation. Any observed admission failure permanently
retires the whole transport, including failures before or after response I/O;
restoring runtime scope cannot reopen it. The endpoint origin is immutable. Caller or
model identity fields cannot supply this scope. Startup must source it from
authenticated runtime state and keep the transport inside the private service.

The DNS answer set is bounded to 32 addresses. Empty, invalid, scoped IPv6,
special-purpose or mixed public/private sets deny before dialing. The direct
TCP dialer receives only an already-validated literal address and the approved
port. No second hostname lookup occurs during dialing. DNS is checked again
for each new request rather than renewing admission around an earlier answer.

TLS requires version 1.3 or later, normal system-root certificate verification,
and the approved hostname. The public API admits no custom roots, resolver,
dialer, insecure certificate option or environment proxy. Certificate-location
environment overrides deny construction. Protected startup must also qualify
the environment, immutable system trust store, process role and network routes.

The transport uses HTTP/1.1 and denies CONNECT, upgrade requests and switched
protocol responses. It follows no redirects: private callers must admit each
subsequent request separately. Connection reuse is disabled so uncertain
operations cannot enter the HTTP transport's automatic retry path on reused
connections. An uncertain request permanently retires the transport.
Upgrade header names and `Connection: upgrade` tokens are checked without case
assumptions before dialing, including directly constructed header maps.

## Retirement and output

Grant withdrawal and lifetime cancellation close active sockets, including
blocked TLS handshakes and response reads. A periodic current-scope check also
retires idle authority when runtime identity changes. Requests and response
streams have bounded lifetimes. Closing permanently denies transport reuse.

Transport failures have one fixed error. Generic transport formatting and JSON
reveal no private state. Response headers, bodies, cookies and TLS metadata are
private consumer data and must never enter model results or diagnostic output.
The transport does not approve HTTP methods, paths, form submission or other
account effects: the caller must apply its separate operation admission before
forwarding any request. It supplies no general-purpose model HTTP capability.

## Qualification limits

Synthetic tests use real TLS handshakes against local servers, a test-only
certificate authority, real CA1 grant lifetimes and a private synthetic
DNS/dial mapping. They verify certificate/name/protocol denial, validated-address
dialing, entire unsafe-answer-set denial, scope changes, cancellation, blocked
response revocation, redirect non-following and uncertain POST non-replay.
The public constructor has none of those test overrides.

These fixtures do not qualify public DNS connectivity, physical Android,
Chromium-to-proxy TLS, actual redirect enforcement, no-direct-network rules,
process/caller isolation, or the intended deployed host. A browser proxy must
terminate or otherwise enforce the admitted request boundary, use this upstream
transport, and prove that Chromium cannot bypass it before real credentials are
activated. The current agent-administered host remains unqualified.

## Primary references

- [Go HTTP transport](https://pkg.go.dev/net/http#Transport): connection reuse,
  retry conditions, protocol control and the separation from redirect handling.
- [Go TLS configuration](https://pkg.go.dev/crypto/tls#Config): normal certificate
  validation, hostname and minimum protocol version.
- [Go system certificate pool](https://pkg.go.dev/crypto/x509#SystemCertPool):
  platform trust and certificate-location environment overrides.
