# Retired Credential Appliance

The [trusted-runtime decision](credential-trusted-runtime-design.md) replaces
the earlier hostile-runtime/appliance model. The integrated alias-login
qualification now exercises the retained vault, approval, browser and output
boundaries together. The unused appliance is removed rather than deployed.

## Removed implementation

- `cmd/mintclaw-credential` and its private provisioning, startup, supervision,
  immutable-image, fixed launcher, resource-service, egress-proxy, private
  browser-worker and Unix transport packages.
- `internal/credentialipc` and the unused `intent.Client` and serialized
  appliance-operation protocol. Shared alias/reference validation and bounded
  approval-status metadata remain.
- The old Root-only phone enrollment command and separate owner-console socket.
  Initial enrollment now uses the integrated private operator command and
  existing gateway, with an ordinary private operator-owned state directory.
- `runtime/browser/credential-login`, the separate appliance browser runtime.
  The existing first-party Playwright library owns private credential use.
- Appliance-only OS-encryption/bootstrap, loader, service-startup, runtime-image
  and process/network forest fixtures, containers, scripts and CI steps.

Tests specific to these deleted APIs are removed with their implementation.
They are historical evidence, not rollout gates for the current design.

## Retained implementation and qualification

- `pkg/credentialbroker` policy, principal/origin/revision bindings and live
  revocable grant leases.
- `internal/credentials/vault` exact-item SDK adapter, bounded private
  consumption and private operator-token storage.
- `internal/credentials/approval` passkey verifier, private owner store,
  enrollment capability, phone UI and single in-memory grant owner.
- `cmd/mintclaw/internal/credentials` private terminal setup and enrollment.
- The existing gateway, first-party browser, output guards and alias tool.
- Policy/approval/vault/setup races; current private TTY and state-permission
  tests; Linux protected-owner/ACL qualification; Chromium phone and private
  browser fixtures; integrated alias/grant-reuse/revocation/output qualification.

The phone qualification script now runs ordinary-owner private CLI tests and
the virtual passkey page. It creates no special user, Root console or service.
The protected-owner Linux fixture remains because filesystem admission is part
of the current design, independently of the retired appliance.

Historical architecture/qualification documents are retained and labelled as
such. The repository history preserves the removed source. This removal does
not change any deployed host, service, firewall or real credential store.

Full output qualification, supported site/placement flows, the selected scoped
connector and the physical Android/personal-account pilot remain separate
roadmap gates. Removal of unused machinery does not establish those outcomes.
