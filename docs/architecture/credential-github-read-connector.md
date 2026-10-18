# Private GitHub Pull Request Reads

This is an optional non-browser credential operation in the
[trusted-runtime roadmap](credential-access-roadmap.md). It uses the existing
gateway, 1Password adapter, Android passkey approval and generic chat notice.
It needs no additional service or host and stays disabled unless the operator
requests it. Browser logins do not depend on GitHub access. No GitHub token,
repository selection or real-token connector pilot is needed for the browser
credential feature.

## Scope and configuration

The `github_pull_requests` tool accepts only `account_alias` and `repository`.
It lists at most 20 open pull requests with number, title, constructed GitHub
URL and draft status. Those requested results may enter model context; the
authentication value remains inside local code.

Create a GitHub fine-grained personal access token for the selected repositories
with **Pull requests: Read-only**. GitHub documents this permission for
[listing pull requests](https://docs.github.com/en/rest/pulls/pulls#list-pull-requests).
Save it as an **API Credential** item in the same dedicated MintClaw 1Password
vault, in its built-in concealed `credential` field. This is an API token, not
the website Login item's password. Keep both the API token and service-account
token out of chat and agent tools.

Add an exact reference-only account entry to the prepared credential policy:

```json
{
  "alias": "github-review",
  "vault_id": "VAULT_UUID",
  "item_id": "API_CREDENTIAL_ITEM_UUID",
  "origins": ["https://api.github.com"],
  "principals": [{
    "owner_id": "operator",
    "actor_id": "actor_OPAQUE_AUTHENTICATED_ACTOR_ID",
    "agent_id": "main",
    "host_id": "gateway"
  }],
  "github_read": {"repositories": ["owner/repository"]}
}
```

Use the deployment's real owner/actor/agent/host identifiers. A GitHub account
entry has no browser `login` flow. Repository names are case-normalized, sorted
and defensively copied. The complete allowlist contributes to the approval
revision; editing it requires a restart and fresh approval. All accounts still
use the one dedicated vault. Enable `credentials.github_read_enabled` only
after the selected gateway's private setup, phone route and output qualification
are ready. This flag is independent of browser login and defaults to false.

## Private operation

The gateway derives caller identity from authenticated runtime context and
checks the configured repository before requesting approval. The existing phone
page grants the alias for at most 20 hours. Matching requests reuse that grant.
The shared controller remains the only approval owner; there is no connector
grant cache. A bounded wait can return `approval_required` before the operator
finishes approving. Initial admission covers the gateway agent pipeline;
unqualified coding contexts are denied.

After approval, the runtime reads the exact API Credential item, checks the
live lease and injects its value into one private `Authorization` header. The
operation is fixed to `GET https://api.github.com/repos/{owner}/{repo}/pulls`
with `state=open&per_page=20`. The client has no environment proxy or cookie
jar, follows no redirects and bounds time, headers and body size. It discards
raw error bodies and projects failures to fixed outcomes.

Only the named result fields leave private code. API-provided links and bodies
are ignored, and authentication echoes are removed while the value's output
registration remains active. Revocation or expiry cancels an in-flight request;
the lease is checked again before releasing its protected result. Temporary
API-key byte buffers are cleared after use. Existing SDK/string lifetime
limitations still apply; this is not a memory-zeroization claim.

## Qualification and rollout

Focused tests cover exact API-item category/field/binding validation, private
buffer cleanup, immutable repository scope, actual HTTP authorization/projection,
redirect denial and fixed private failure handling.

The opt-in `TestCredentialGitHubOutputFixture` runs the production gateway tool,
virtual phone passkey, local HTTP API and real OpenAI-compatible/Codex adapters.
It approves once, performs two agent turns with grant reuse, then revokes during
a third in-flight API request. It scans actual provider requests, canonical
history, Seahorse state, logs, passive traces and both support-bundle modes for
the synthetic API token. The fixture accesses no real GitHub, 1Password account
or physical Android device. The implementation and synthetic qualification are
complete. Production activation and real-token acceptance are separate optional
rollout work, not a browser-credential completion gate.
