# Config Schema Contract

MintClaw accepts one `config.json` schema at runtime. The `version` field is
required and must equal `config.CurrentVersion`; the current value is `3`.

```json
{
  "version": 3,
  "agents": {},
  "channel_list": {},
  "model_list": [],
  "tools": {}
}
```

Missing, older, and future versions are rejected before the configuration is
decoded. Loading never converts the document, writes a replacement, or creates
a migration backup. Unknown and removed fields are also rejected with their
exact JSON paths.

This keeps startup on one typed schema and makes configuration changes an
explicit part of a coordinated upgrade.

## Current field shapes

- Models belong in `model_list`; credentials use the `api_keys` array.
- Agent model selection uses `agents.defaults.model_name`.
- Channels belong in `channel_list`. Every entry has a `type`, common channel
  fields at the entry root, and channel-specific fields under `settings`.
- Agent routing uses `agents.dispatch.rules`; the removed top-level `bindings`
  field is not accepted.
- Removed tool names such as `tools.edit_file` are not ignored.

Every chat, fallback, routing, subagent, voice, TTS, and vision selector names a
configured `model_list[].model_name` exactly. The provider-native identifier is
stored only in that entry's `model` field; raw model IDs, `provider/model`
references, provider aliases, and `agents.defaults.provider` are not alternate
selector syntaxes. A `model_name` may contain `/` when that exact text is the
declared name, and repeated names remain valid for load balancing.

Configuration loading rejects unknown references and surrounding whitespace.
Workspace `AGENT.md` model frontmatter follows the same rule and is rejected
when the workspace agent is constructed if its exact name is not configured.

See [`config/config.example.json`](../../config/config.example.json) for a
complete current document.

## Coordinated upgrade procedure

When a release changes the configuration schema:

1. Stop all MintClaw processes that share the configuration.
2. Back up `config.json` and `.security.yml` using the deployment's normal
   backup mechanism.
3. Convert both files to the new schema and set the new `version`.
4. Validate the converted files with the new binary before replacing the
   running version.
5. Upgrade the gateway, CLI, coding frontend, companions, and other first-party
   clients together.
6. After the rollout is healthy, remove obsolete fields and temporary cutover
   artifacts.

Do not rely on MintClaw startup to preserve or transform an older document.

## Changing the schema

For a breaking configuration change:

1. Update `Config` and its nested current-schema types.
2. Increment `CurrentVersion`.
3. Update defaults, examples, documentation, fixtures, and the deployed
   configuration in the same rollout.
4. Add tests that the new version loads and older, missing, and future versions
   fail without changing files.
5. Document the one-time operator conversion in the release notes.

Do not add version-specific runtime structs, migration chains, deprecated field
stripping, dual readers, automatic rewrites, or migration backups. A one-time
offline conversion script is acceptable for a large coordinated cutover, but it
must not become part of the runtime loader.
