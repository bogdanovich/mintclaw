# Skill Bundling And Portability Contract

Status: target contract for the
[Shared Agent Skills Roadmap](shared-skills-roadmap.md). The current runtime
does not yet implement every path below.

## Purpose

Define how a reusable skill becomes part of MintClaw, how the same package is
made available to the coding agent and always-on gateway, and how imported
skills are accepted or rejected without confusing a parseable `SKILL.md` with
a working capability.

## Package Shape

A bundled skill uses the open Agent Skills directory shape:

```text
<skill-name>/
├── SKILL.md                 required instructions and trigger metadata
├── scripts/                 optional deterministic helpers
├── references/              optional detailed documentation
├── assets/                  optional non-secret static inputs
├── agents/
│   └── openai.yaml          optional interoperability metadata
├── LICENSE                  required when imported licensing needs attribution
└── MINTCLAW_PROVENANCE.json required for an imported or adapted skill
```

MintClaw-authored skills do not need a provenance file unless they derive
material content from another package. Optional files do not grant execution,
network, MCP, filesystem, or installation authority.

Tracked system skills live under `pkg/skills/bundled/<skill-name>/`. The build
embeds that tree and the runtime materializes it into
`$MINTCLAW_HOME/skills/.system/` under one content fingerprint. The installed
system tree is runtime-owned and read-only from skill-management commands.

## Availability And Runtime Compatibility

Bundled does not mean universally executable. Discovery resolves compatibility
for the current runtime before presenting a skill to the model.

Each skill is classified as one of:

- **shared**: the same instructions and dependencies work in coding and
  gateway turns;
- **capability-adaptive**: one package contains explicit branches for admitted
  runtime capabilities, with tests for every advertised branch;
- **coding-only**: requires repository, shell, or local artifact authority that
  ordinary gateway turns do not have;
- **gateway-only**: requires current-message attachment, channel, durable job,
  or delivery authority absent from local coding turns; or
- **unsupported**: retained only in the portability inventory and not bundled.

An adaptive skill must detect capabilities from model-visible runtime facts or
tool availability. It must not probe by repeatedly calling unrelated tools.
When two runtimes have materially different safety or correctness workflows,
prefer two clearly named skills over a long ambiguous instruction tree.

## Feature-Owned Skills

The owner of a first-party capability owns its skill contract. A feature is not
advertised through a bundled skill until its tools, policies, errors, and
deployment behavior are stable enough to document and test.

For example, the PDF implementation currently has separate local-file and
gateway attachment concerns. The PDF capability owner should finish the
runtime contract first, then provide either:

- one tested capability-adaptive `pdf` skill whose coding and gateway paths are
  explicit; or
- separate `pdf-local` and `pdf-attachments` skills when a shared workflow
  would hide important authority differences.

Once admitted, the skill enters `pkg/skills/bundled`, becomes visible to both
catalogs where compatible, and receives coding and gateway canaries. The
shared-skills work must not pre-empt or duplicate an active feature owner's
runtime implementation.

## Upstream Portability Audit

Before importing skills, create a machine-readable inventory pinned to exact
upstream revisions. The initial audit covers:

- system samples embedded by `openai/codex`;
- the current public `openai/plugins` catalog;
- separately distributed OpenAI curated/runtime skills available to the
  maintained development installation; and
- an existing MintClaw skill with the same purpose, if any.

Every discovered skill receives one decision:

| Decision | Meaning |
| --- | --- |
| `port` | Host-neutral instructions and dependencies already match MintClaw. |
| `adapt` | The workflow is useful, but tool names, policies, paths, metadata, or tests must change. |
| `covered` | An existing MintClaw skill already provides an equal or stronger contract. |
| `defer` | Potentially useful, but its required MintClaw capability is not yet stable or available. |
| `exclude` | Product-specific, unsafe, unlicensed, obsolete, duplicative, or outside MintClaw's scope. |

The inventory records upstream repository, revision, skill path, license,
decision, target MintClaw name, supported runtimes, required capabilities,
adaptation notes, and validation evidence. `port` and `adapt` entries are not
complete until their files and tests are merged. `defer` entries name the
missing capability and owning roadmap; they are reconsidered when that owner
finishes. `exclude` entries retain a short durable rationale.

The audit is exhaustive over the pinned source trees, but importing is
selective. Vendor-specific plugin workflows, connector instructions, and UI
metadata are not portable merely because the repository license allows
copying them.

## Admission Checklist

A skill may enter the system bundle only when all applicable checks pass:

1. **Purpose** — one bounded job with a clear trigger and non-trigger boundary.
2. **License** — redistribution and adaptation are allowed, with required
   notices retained.
3. **Provenance** — upstream revision and local modifications are recorded.
4. **Tools** — every named tool or MCP dependency maps to an admitted MintClaw
   capability.
5. **Authority** — instructions do not broaden runtime policy or treat a path,
   chat message, or repository file as authorization by itself.
6. **Portability** — supported runtimes and operating systems are explicit.
7. **Failure behavior** — missing dependencies fail once with an actionable
   diagnostic rather than retries or unrelated fallback tools.
8. **Prompt cost** — the catalog description is concise; details live in the
   body or bounded references.
9. **Tests** — trigger, non-trigger, compatibility, and critical workflow
   fixtures cover every advertised runtime.
10. **Ownership** — a subsystem or roadmap owns future corrections.

## Provenance Record

Imported and adapted skills use a versioned record such as:

```json
{
  "schema_version": 1,
  "source_repository": "https://github.com/example/project",
  "source_revision": "full-commit-sha",
  "source_path": "plugins/example/skills/example",
  "license": "MIT",
  "decision": "adapt",
  "adaptations": [
    "replace host-specific tool names",
    "apply MintClaw runtime policy"
  ]
}
```

This record is informational provenance, not executable configuration. The
build rejects malformed records for imported bundle entries.

## Update Policy

Upstream updates are reviewed as source changes, never pulled into a deployed
bundle automatically. An update must:

- compare against the pinned previous revision;
- preserve local safety and runtime adaptations;
- repeat license and dependency checks;
- rerun affected skill fixtures and canaries; and
- produce a new system-bundle fingerprint.

Removing a bundled skill also requires an inventory decision so disappearance
is intentional and diagnosable.

## Installation Outside The Bundle

User and repository installs use the same package shape but are not silently
promoted to system skills:

- `$HOME/.agents/skills` is the shared personal catalog for compatible local
  hosts, including MintClaw code and the gateway running as that user;
- `<repo>/.agents/skills` is repository-owned and coding-only by default; and
- `$MINTCLAW_HOME/workspace/skills` is gateway-workspace-owned.

Different machines do not share these paths automatically. Reproducing a user
skill on a companion or deployment host requires an explicit install or
deployment manifest.

### Self-management vocabulary

MintClaw must not infer installation ownership from whichever directory the
gateway process happened to start in. CLI commands and agent tools resolve the
same explicit scopes:

| User intent | Scope | Target root |
| --- | --- | --- |
| "install for yourself", "personal", "shared", or "for both agents" | `user` | `$HOME/.agents/skills` |
| "install for this repository/project" | `repository` | the admitted repository `.agents/skills` |
| "install for the gateway workspace" | `workspace` | `$MINTCLAW_HOME/workspace/skills` |
| MintClaw release-owned defaults | `system` | embedded source materialized into `$MINTCLAW_HOME/skills/.system` |

`system` is not a mutable installer target. Ambiguous requests should produce
the proposed scope and canonical target before mutation; they must not default
to the gateway workspace solely because the request arrived through Telegram.

The bundled `mintclaw-agent` skill owns model-facing self knowledge for this
contract. Once the scoped installer exists, that skill must document:

- the four scopes and their visibility;
- how to inspect effective and shadowed skills;
- when to use user, repository, and workspace installation;
- that another machine requires explicit deployment or installation; and
- that skills never grant missing tools or permissions.

The skill is tested against representative requests, including "find the
Codex deployment skills and install them for yourself". The expected result is
a portability/compatibility check followed by a `user`-scope plan, not an
unqualified write to the gateway workspace.

## Completion Evidence

The bundling contract is implemented only when:

- the tracked source and fingerprinted runtime layout exist;
- the pinned upstream inventory is complete;
- every `port` and `adapt` decision is represented in the bundle with required
  attribution and tests;
- every `covered`, `defer`, and `exclude` decision has a recorded rationale;
- first-party feature skills, including PDF after its owner finishes the
  capability contract, use this admission process; and
- coding and gateway canaries prove the advertised runtime classifications.
