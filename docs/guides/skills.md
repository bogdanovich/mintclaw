# Skills: scopes, bundled defaults, and migration

MintClaw discovers Agent Skills packages from explicit trust scopes. A skill
is a directory containing `SKILL.md`; optional `scripts/`, `references/`,
`assets/`, and `agents/` entries stay inside that directory.

## Choose the scope first

| Scope | Canonical root | Available to |
| --- | --- | --- |
| `workspace` | `$MINTCLAW_HOME/workspace/skills` | The always-on gateway using that workspace |
| `repository` | `<repo>/.agents/skills` | Coding sessions inside that repository |
| `user` | `$HOME/.agents/skills` | Coding and gateway sessions running as that user |
| `system` | Active generation below `$MINTCLAW_HOME/skills/.system` | Coding and gateway sessions using that MintClaw release |

Gateway discovery order is workspace, user, system. Coding discovery walks
from the current directory to the project root for repository skills, then
uses user and system. The first case-insensitive skill name wins; diagnostics
retain information about shadowed packages.

Use `user` for requests such as "install for yourself", "make this personal",
"share this between the coding and live agents", or "install for both
agents". Use `repository` only when the package belongs to the current codebase.
Use `workspace` only when it should belong to the configured gateway workspace.
The directory from which a process was launched does not decide ownership.

`system` is not an installation target. It is owned by the MintClaw release.

## Bundled system skills

First-party system-skill sources live at:

```text
pkg/skills/bundled/<skill-name>/
```

The CLI and launcher embed that tree. On startup they compute a deterministic
content fingerprint and materialize a complete generation at:

```text
$MINTCLAW_HOME/skills/.system/generations/<fingerprint>/
```

`$MINTCLAW_HOME/skills/.system/active.json` selects one verified generation.
MintClaw stages and verifies a new generation before atomically replacing that
marker. An interrupted write therefore leaves the previous complete generation
active. Each running process pins the generation verified from its own embedded
bundle, so two binary revisions cannot change each other's live catalog merely
by updating the shared marker. Starting the same binary again does not rewrite
an unchanged bundle.

Do not edit or copy files into `.system`. Change `pkg/skills/bundled`, validate
the skill, and ship a new binary. The changed content produces a new
fingerprint. Both `mintclaw code` and the always-on gateway then resolve the
same active system revision without depending on the current working directory.

For the package format, portability decisions, provenance, and admission
checks, see [Skill Bundling and Portability](../architecture/skill-bundling.md)
and the pinned [Skill Portability Audit](../architecture/skill-portability-audit.md).
Imported system skills include a sibling `LICENSE` and strict
`MINTCLAW_PROVENANCE.json`; malformed provenance prevents publication of the
new system generation and leaves the previous generation active.

## Installing non-system skills

`mintclaw skills list --runtime gateway` and
`mintclaw skills list --runtime coding` inspect scope and compatibility for
the selected runtime. Run `mintclaw skills doctor --runtime <gateway|coding>`
for dependency details; add `--json` for a stable machine-readable report.
These commands are read-only: they do not install executables, start MCP
servers, or change policy.

`mintclaw skills show <name>` inspects the effective gateway catalog. The
mutation commands use one scope resolver and one atomic manager:

```bash
# Safe interactive default: shared by coding and gateway on this host.
mintclaw skills install owner/repository/skills/example
mintclaw skills install --scope user owner/repository/skills/example

# These ownership choices must be explicit.
mintclaw skills install --scope repository --project . owner/repository/skills/example
mintclaw skills install --scope workspace owner/repository/skills/example

# Inspect the complete operation, target, origin, and dependency gaps first.
mintclaw skills install --dry-run --json owner/repository/skills/example
mintclaw skills update --scope user example --dry-run
mintclaw skills move example --from-scope workspace --scope user --dry-run
mintclaw skills remove --scope user example --dry-run
```

`install` and `update` stage, validate, and fingerprint the complete package
before atomically publishing it. `update` follows the package's immutable
registry and canonical slug; replacement cannot silently change origin.
`move` validates and copies the bounded package before removing its old copy.
`remove` mutates only the selected scope. `system` is rejected by all mutation
commands.

The live agent's `install_skill` tool also requires `scope`. It supports
`user` and `workspace` in the gateway process; a repository request reports
that repository scope is unavailable instead of guessing a current directory.
Use the repository-aware CLI or coding agent for that operation. Requests such
as "install for yourself", "personal", "shared", or "for both agents" always
mean `user`, even when the request arrived through a gateway channel.

Dry runs may retrieve an external archive to validate it, but they never create
or change files under the selected scope. If an agent cannot write the
requested scope, it must report the limitation and canonical target rather
than silently changing ownership.

Before importing an external package, verify:

- license and provenance;
- MintClaw-compatible tool names and runtime capabilities;
- dependency availability on the target host;
- that instructions do not grant themselves extra authority; and
- trigger, non-trigger, and failure behavior.

Another host or paired companion has a separate user catalog. Installing a
skill locally does not deploy it remotely; use an explicit remote
installation/deployment and run the same compatibility checks on that host.

## Declaring runtime requirements

Keep the portable Agent Skills frontmatter in `SKILL.md`. MintClaw-specific
runtime requirements belong in `agents/mintclaw.yaml`:

```yaml
schema_version: 1
products:
  - coding
  - gateway
requirements:
  os:
    - darwin
    - linux
  executables:
    - gh
  tools:
    - exec
  mcp_servers:
    - github
```

All fields are optional except `schema_version`. Supported products are
`coding` and `gateway`. Requirement identifiers are declarative facts, not
grants: a manifest cannot enable a disabled tool, relax an agent policy, start
an MCP server, or install a missing executable.

Discovery adapts the legacy `metadata.nanobot.os`,
`metadata.nanobot.requires.bins`, and `metadata.nanobot.requires.tools` fields
at the parser boundary. A valid `agents/mintclaw.yaml` takes precedence. New
or updated packages should use the MintClaw manifest instead of adding new
legacy metadata.

Optional `agents/openai.yaml` metadata is exposed only as interoperability
information. Its interface, dependency, and implicit-invocation fields do not
control MintClaw tool admission, compatibility, or selection.

Compatibility states are:

- `ready`: all declared requirements are available;
- `missing_dependency`: an executable, tool, or MCP server is absent;
- `policy_disabled`: configuration or agent policy denies a dependency;
- `runtime_incompatible`: the OS or runtime product is not supported;
- `malformed`: skill or compatibility metadata is invalid; and
- `shadowed`: a higher-priority skill with the same case-insensitive name won.

Only `ready` skills enter the implicit model catalog. Incompatible packages
remain visible to `list` and `doctor`, and an explicit selection fails with a
deterministic compatibility error. Diagnostic output includes requirements and
paths, never complete instruction bodies or configuration secrets.

## One-time migration from the old layout

Old MintClaw builds also searched `$MINTCLAW_HOME/skills` as a mutable global
root and could discover a `skills` directory relative to the process working
directory. Those compatibility paths are intentionally removed.

Before deploying this layout:

1. Inspect each direct child of the old `$MINTCLAW_HOME/skills` directory,
   excluding `.system`.
2. Move personal packages to `$HOME/.agents/skills/<name>`.
3. Keep gateway-private packages under
   `$MINTCLAW_HOME/workspace/skills/<name>`.
4. Put repository-owned packages in `<repo>/.agents/skills/<name>`.
5. Do not migrate old copies of MintClaw defaults; the new binary installs the
   signed-off embedded set into `.system`.
6. Start the new binary, run `mintclaw skills list`, and verify the expected
   source/scope before removing the old copies.

There is no permanent dual-root fallback. This prevents stale packages from
silently shadowing the release bundle.

## Deployment and rollback

Before upgrading a maintained host, record the current binary revision and
back up only the mutable skill catalogs and configuration that the rollout may
change:

- `$HOME/.agents/skills`;
- `$MINTCLAW_HOME/workspace/skills` for each maintained workspace; and
- `$MINTCLAW_CONFIG` (plus its protected companion file when applicable).

After installing the merged binary, run `skills list` and `skills doctor` for
both runtimes, then exercise one user, repository, workspace, and incompatible
dependency canary. Record the canonical paths and compatibility states.

To roll back, stop the affected processes, restore the previous binary plus
those catalog/config backups, and restart the same services. The older binary
reselects its own verified system-bundle generation. Do not restore, remove,
or rewrite session, task, interaction, memory, or diagnostic history as part
of a skill-catalog rollback. Verify catalog fingerprints and both runtime
views before resuming normal traffic.
