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
checks, see [Skill Bundling and Portability](../architecture/skill-bundling.md).

## Installing non-system skills

`mintclaw skills list` and `mintclaw skills show <name>` inspect the effective
gateway catalog. The existing `mintclaw skills install ...` command and
`install_skill` agent tool install into the configured gateway workspace. They
must not be used as a substitute for a user- or repository-scoped install.

Until the scoped installer lands, place reviewed packages in the canonical
user or repository root using an authorized filesystem/deployment workflow.
If an agent cannot write the requested scope, it should report the limitation
and target path rather than silently changing scope.

Before importing an external package, verify:

- license and provenance;
- MintClaw-compatible tool names and runtime capabilities;
- dependency availability on the target host;
- that instructions do not grant themselves extra authority; and
- trigger, non-trigger, and failure behavior.

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
