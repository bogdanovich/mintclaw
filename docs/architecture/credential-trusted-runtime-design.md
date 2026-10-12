# Trusted Runtime Credential Access

## Decision and scope

The operator approved this trust model on 2026-10-11: the model acts in good
faith and follows instructions; the MintClaw code, host, paired companion, and
approved destination website are trusted to perform their roles. The feature
must prevent secret values from reaching LLM providers or any local trace,
log, transcript, error report, persistence record, or browser artifact. Secret
names/slugs may be exposed. Android approval grants selected-account access
for at most 20 hours and can be revoked.

This replaces the earlier adversarial-agent/appliance design. It requires no
new VM, separate credential machine, microservice fleet, Root browser helper,
immutable runtime image, or custom cross-host credential transport. Implement
credential access inside the existing MintClaw application and browser stack.
Trusted local code and the approved website necessarily receive a password;
no claim is made against hostile host administration, arbitrary malicious
model execution, or a compromised approved site.

## Components and ownership

One runtime-owned credential component holds immutable alias bindings, the
private 1Password client, phone approval authority, grant/session ownership,
and a known-value output filter. It uses the existing runtime lifecycle and
paired browser companion where needed. Its functions are private runtime APIs,
not a general model-facing secret retrieval API.

1Password remains the only vault. A service account has read access to a
dedicated MintClaw vault containing selected items. Operator configuration maps
an alias to exact vault/item/field references, permitted callers, approved HTTPS
origins, and browser/connector behavior. The model supplies aliases, not provider
IDs, secret values, tokens, or credential destination endpoints.

Keep the token outside model-visible configuration and workspaces. Ordinary
owner-only permissions or a supported OS credential facility are sufficient
under this trust model. A private operator setup command reads with echo
disabled, never through agent chat, tool arguments, shell command text, or a
captured setup transcript. No Root or separate service account is required.
The SDK may retain a private copy in memory; do not promise complete Go/SDK/
browser zeroization. Bound retention and avoid durable value copies.

Move reusable vault and approval code into a runtime-internal package instead
of maintaining two independent implementations behind command-private imports.
Retain one owner for grants and sessions, one approval verification path, and
one error/output projection. A paired companion uses its existing authenticated
transport; secret-bearing ephemeral payloads must be excluded from its durable
records before credential login is admitted there.

## Alias-only operations

The model-facing browser operation requests a configured account alias on a
runtime-owned browser session. Trusted caller identity and current browser
state come from the runtime. The component checks approval and policy, retrieves
the selected login privately, fills/submits through the browser driver, and
returns only an approved alias and fixed outcome. There is no intermediate
tool result or model turn containing a username, password, OTP, token, or cookie.

Examples of safe outcomes are `logged_in`, `approval_required`,
`handoff_required`, `denied`, `failed`, and `outcome_unknown`. Safe trace/audit
records contain an alias, operation, outcome, and approved timing/correlation
metadata. They contain no raw SDK errors, browser exceptions, request headers,
response bodies, DOM dumps, or authentication values. Slug visibility does not
permit logging the associated value.

Non-browser operations use a concrete endpoint and action selected by operator
configuration. Inject credentials inside the connector immediately before the
request, apply its own effect policy, and return an allowlisted result shape.
Provide one useful connector first; do not expose arbitrary HTTP, `get_secret`,
shell interpolation, or a credential-bearing request object to the model.

## Browser privacy transaction

Serialize credential handling with browser observations for the affected
session. Before provider retrieval, validate the live exact HTTPS origin and
current login fields using trusted browser state. Recheck grant and origin
after retrieval and immediately before filling. Names or URLs selected earlier
do not establish the current page's origin.

Suppress snapshots, screenshots, console/network capture, interaction records,
and ordinary action diagnostics while credential values can be present. Fill
and submit in one trusted operation, with no model round trip between those
steps. Pass values through an existing private/ephemeral driver channel, never
as model tool arguments, executable arguments, or serialized diagnostic data.
The existing protected-fill path needs an explicit credential admission;
merely calling ordinary fill with a secret would violate the output contract.

If submission fails or is canceled after filling, clear the filled values
before restoring observations; close the affected context if cleanup cannot
be established. Return a fixed failure/unknown outcome and do not automatically
retry uncertain external effects. A successful form submission does not by
itself prove authentication: use an appropriately bounded site outcome check.
MFA and CAPTCHA may require private operator interaction; never put an OTP,
seed, or password into chat to perform that handoff.

After login, ordinary permitted page content may be shown to the model only
through sanitized browser observations. Keep credentials and session cookies
out of page dumps, driver APIs, artifacts, and storage-state export. Prevent
password-reveal/autofill mistakes through credential-aware action admission
and the known-value output guard. This protects normal operation and accidental
echoes under the chosen trust model; it is not a proof against arbitrary secret
encoding by a malicious page or model.

## Approval and session lifetime

Reuse the existing reviewed WebAuthn library and owner enrollment logic. Serve
the minimal approval/revoke page over stable HTTPS in the existing application,
reachable from the Android phone. Enroll the owner through a private operator
setup action. Verify challenge, origin, user presence/verification, credential
ownership, replay, expiry, and persisted authenticator updates.

Approval covers named aliases for one owner, actor, agent, host, and policy
revision. Its lifetime is at most 20 hours, may be shorter, and cannot silently
renew. Bind later credential/session operations to that original grant.
Revocation, expiry, policy replacement, and shutdown disable credential use and
retire affected authenticated sessions. Grants disappear on process restart.
Independent browser/connector policy still controls purchases, messages, posts,
account changes, and other external effects.

Notifications contain only a generic access request and an approval-page link,
with no secret value or bearer approval. An opened notification cannot approve
access itself. Physical Android enrollment, remote approval, revocation, and
notification delivery must be verified; synthetic WebAuthn tests do not prove
the phone flow works on the operator's device.

## Output prevention and defense in depth

Exclude values before serialization or recording. Credential code emits safe
typed records directly and discards raw provider/driver diagnostics. Suppress
secret-bearing browser captures at their source. Review model-facing and
durable sinks in gateway, coding, browser, node, companion, logger, passive
trace, recovery, and support-bundle paths.

Register privately retrieved values with a runtime-owned output guard before
they touch the page or connector. Apply known-value and common-encoding
redaction to permitted subsequent text/JSON observations and relevant durable
sinks. Preserve aliases and safe outcomes. Bound the registry and value
lifetime; do not turn it into another persistent secret database. Screenshots
and opaque artifacts cannot be made safe by substring replacement and need
capture suppression or an explicit credential-aware admission.

The known-value filter is a backup to source suppression and structured result
projection. Generic sensitive-key regexes alone do not recognize arbitrary
passwords embedded under innocent keys. Never print a detected canary or raw
secret while verifying redaction. Qualify actual serialized requests and files
using synthetic credentials on success, failure, cancellation, MFA, expiry,
revocation, and restart.

## Migration and rollout

The [current roadmap](credential-access-roadmap.md) defines TR0–TR6. Earlier
isolation documents are historical records. Their VM, Root, service-role, and
process/network isolation gates do not constrain this implementation. Useful
policy, SDK, phone-verifier, ephemeral input, and output tests remain reusable.
Remove retired appliance machinery through a reviewed change; do not install it
or keep adding prerequisites to it.

No real credential or production capability is enabled by this document. First
complete a synthetic integrated login and output inspection, then prepare
private token entry and Android enrollment. Qualify one operator-selected
account before enabling additional sites or connectors. This is a change in
architecture and trust assumptions, not a claim that the old component tests
have completed the new feature.
