# Shared Agent Skills Roadmap

Status: admitted for implementation.

Baseline: `origin/main` at `0bf3b2be5` on 2026-09-20. This roadmap is a
separate capability track from the coding TUI visual work. It changes skill
discovery, selection, packaging, and diagnostics; it does not change terminal
rendering.

## Objective

Give the local coding agent and the always-on gateway one coherent skill
system without giving either runtime authority it did not already have.
Portable user and system skills should be discoverable by both runtimes;
repository skills should follow the coding working directory; gateway
workspace skills should remain private to the gateway. The model should see a
bounded catalog first and receive full instructions only for selected skills.

The target follows the open Agent Skills directory format while retaining
MintClaw-owned admission, tool-policy, installation, and deployment
boundaries. Codex is a design reference for layered discovery, progressive
disclosure, and bundled-skill materialization, not a source to copy wholesale.

## Current State And Evidence

MintClaw already has useful pieces:

- `pkg/skills.SkillsLoader` discovers direct child skill directories and
  resolves duplicate names with `workspace > global > builtin` precedence;
- the gateway places a metadata catalog in its prompt and can inject selected
  skill bodies for one turn;
- `/use <skill>` provides explicit gateway activation;
- registry installation, immutable origin metadata, and bounded workspace
  inventory already exist; and
- workspace skill management is exposed through the CLI and web API.

The pieces are not yet one product contract:

- `mintclaw code` constructs a loader but its coding prompt omits the catalog
  and selected-skill injection;
- repository-local `.agents/skills` and user-level `$HOME/.agents/skills` are
  not discovered;
- the complete metadata catalog is rendered without an aggregate context
  budget;
- the default builtin root depends on process current working directory;
- the embedded onboarding workspace is not the runtime owner of a versioned
  system-skill installation; and
- `skills install-builtin` still names an obsolete source path and obsolete
  skill set.

## Product Decisions

### One catalog, runtime-specific roots

Both runtimes use the same catalog implementation and metadata model, but a
catalog snapshot is resolved for an explicit runtime context.

| Scope | Coding agent | Always-on gateway |
| --- | --- | --- |
| MintClaw system bundle | visible | visible |
| `$HOME/.agents/skills` user skills | visible | visible |
| Repository `.agents/skills` from root to current directory | visible | not visible in ordinary chat turns |
| Gateway workspace skills | not visible | visible |

An explicitly admitted channel-to-coding task receives the coding runtime's
repository catalog. It does not make repository instructions part of the
gateway's ordinary personal prompt.

### Skills are instructions, not authority

A skill may describe how to use a tool, binary, or MCP server. Discovery and
activation never add that capability to the runtime. Existing tool, MCP,
filesystem, node, channel, and execution policies remain authoritative.
Project instructions and project skills cannot mutate the admitted coding
tool catalog.

### Deterministic shadowing

MintClaw keeps one effective skill for a name. The nearest runtime-specific
root shadows lower-priority user and system entries. Diagnostics report every
shadowed candidate and the winner; the prompt never presents ambiguous
same-name entries.

### Progressive disclosure

The initial prompt contains only effective name, concise description, source
scope, and a stable locator. Full `SKILL.md` content is injected only when a
skill is explicitly selected or admitted by the runtime's implicit selection
policy.

The initial catalog budget defaults to two percent of the selected model's
context window. When the window is unknown, the fallback is 8,000 characters.
Descriptions are shortened before lower-priority entries are omitted. An
omission warning is emitted without exposing unbounded metadata.

### Clean cutover rather than permanent compatibility

There are no external MintClaw installations that require a permanent legacy
loader. Deployment may move existing local skill directories once, but the
runtime must not keep indefinite dual roots or dual writes solely for the old
layout.

## Target Layout

```text
pkg/skills/bundled/<name>/           tracked source for MintClaw system skills
$MINTCLAW_HOME/skills/.system/       fingerprinted materialized system cache

$HOME/.agents/skills/<name>/         portable user skills shared with other hosts
<repo>/.agents/skills/<name>/         repository and nested-directory skills

$MINTCLAW_HOME/workspace/skills/      gateway-private workspace skills
```

The system cache is runtime-owned and replaced atomically when its embedded
fingerprint changes. Users do not edit it. Portable personal skills belong in
the Agent Skills standard user root, not in the system cache.

## Delivery Packets

Packets are delivered as focused sequential pull requests. A later packet may
refine an API introduced by an earlier packet, but it must not silently expand
the authority or scope decisions above.

### S0 — Admit the roadmap

Scope:

- publish this roadmap and link it from the architecture index;
- record current gaps, target scopes, guardrails, ordering, and stop
  conditions; and
- establish the complete-roadmap goal and per-packet validation expectations.

Completion gate:

- the roadmap is merged and is the source of truth for the implementation
  sequence;
- every requested feature belongs to exactly one packet; and
- later PRs can cite stable acceptance criteria rather than redefining product
  behavior during review.

### S1 — Unified scoped catalog and bounded discovery

Scope:

- replace the three-path loader contract with ordered typed roots carrying
  scope, priority, locator, and trust/runtime provenance;
- discover `$HOME/.agents/skills` for both runtimes;
- for coding contexts, discover `.agents/skills` from repository root through
  the effective working directory without escaping the repository boundary;
- retain gateway workspace isolation and deterministic name shadowing;
- render a context-window-aware progressive catalog budget; and
- expose shadowing, invalid metadata, omitted entries, and discovery errors as
  structured catalog diagnostics.

Completion gate:

- unit tests prove root ordering, nested repository discovery, symlink/path
  containment, duplicate resolution, and coding/gateway scope isolation;
- a large fixture catalog cannot exceed its configured prompt budget;
- truncation and omission are deterministic and observable; and
- no full skill body is read merely to render the initial model catalog beyond
  the bounded metadata parse required for discovery.

### S2 — Coding-agent selection and full-instruction loading

Scope:

- include the bounded effective catalog in the coding system prompt;
- add explicit `$skill` and `/skills` discovery/selection behavior appropriate
  to interactive and non-interactive coding turns;
- inject the full selected `SKILL.md` once for the admitted turn;
- preserve selected-skill behavior across compaction and resume without
  permanently copying the body into every new turn; and
- keep gateway `/use` semantics on the same resolver.

Completion gate:

- the same compatible user/system skill can be selected in gateway and coding
  turns;
- repository skills are selectable in coding turns and absent from ordinary
  gateway prompts;
- unknown, disabled, incompatible, and ambiguous selectors have distinct
  deterministic errors;
- selected bodies are ordered and deduplicated; and
- coding tool admission is unchanged before and after skill activation.

### S3 — Fingerprinted bundled system skills and clean layout cutover

Scope:

- move the tracked default skill source to a package-owned embedded tree;
- materialize it under `$MINTCLAW_HOME/skills/.system` using a content
  fingerprint and atomic replacement;
- remove current-working-directory lookup and the obsolete
  `install-builtin`/`list-builtin` behavior;
- update native, Docker, launcher, and deployed layouts; and
- provide an explicit one-time migration procedure for the maintained
  deployment, without permanent dual-root compatibility.

Completion gate:

- a native binary started from an arbitrary directory sees the same system
  skills;
- unchanged fingerprints cause no rewrite and changed fingerprints replace the
  complete system set;
- interrupted installation cannot leave a partially visible system catalog;
- Docker and deployed gateway/code processes resolve the same system revision;
  and
- repository, docs, scripts, and active deployment have zero references to the
  obsolete builtin layout or commands.

### S4 — Runtime compatibility and dependency diagnostics

Scope:

- normalize skill requirements for operating system, executables, MintClaw
  tools, MCP servers, and runtime products;
- adapt existing legacy frontmatter metadata at the parse boundary rather than
  leaking it through the catalog model;
- inspect optional interoperable metadata without treating OpenAI UI metadata
  as MintClaw authority;
- filter incompatible entries from implicit model use while retaining an
  inspectable diagnostic; and
- add `mintclaw skills doctor` and scope-aware list output.

Completion gate:

- diagnostics distinguish ready, missing dependency, policy-disabled,
  runtime-incompatible, malformed, and shadowed states;
- inspection never installs packages, starts MCP servers, or changes policy;
- gateway and coding compatibility can differ for the same physical skill;
- secrets and complete instruction bodies are absent from diagnostic output;
  and
- current GitHub, PDF, browser, hardware, tmux, and weather skills receive
  explicit compatibility results.

### S5 — Portable-skill audit and curated system bundle

Scope:

- publish a pinned, exhaustive inventory of the Codex embedded system skills,
  the public OpenAI plugin catalog, separately distributed curated/runtime
  skills available to the maintained development installation, and overlapping
  MintClaw skills;
- classify every discovered skill as `port`, `adapt`, `covered`, `defer`, or
  `exclude` under the
  [bundling and portability contract](skill-bundling.md);
- port or adapt every admitted portable skill with license and provenance
  records, dependency declarations, ownership, and focused fixtures;
- retain durable reasons for covered, deferred, and excluded entries; and
- admit feature-owned skills, including the PDF skill after the PDF owner has
  completed its runtime contract, without duplicating the feature work in this
  roadmap.

Completion gate:

- the inventory covers every `SKILL.md` in each pinned source tree with no
  unclassified entries;
- every `port` and `adapt` decision is present in the tracked bundle and passes
  its advertised runtime fixtures;
- licensing and attribution are complete and independently inspectable;
- Codex-specific tool names, approval semantics, output paths, connectors, and
  UI metadata do not leak into MintClaw instructions unless an explicit
  interoperable adapter owns them;
- overlapping MintClaw skills are consolidated instead of duplicated; and
- PDF and other active feature projects remain `defer` until their owners
  publish a stable skill, then pass the same admission and canary gates.

### S6 — Installation scopes, operational rollout, and closeout

Scope:

- make install/update/remove operations require an explicit user, repository,
  or gateway-workspace scope, with a safe documented default for interactive
  use;
- preserve immutable source/origin identity and bounded inventory checks;
- add dry-run plans for moves, replacements, and dependency gaps;
- migrate the maintained deployment, run coding and gateway canaries, and
  document rollback; and
- update guides and archive this roadmap only after merged and deployed
  evidence exists.

Completion gate:

- installing one portable user skill makes it visible to both local runtimes
  on the same host without duplicate files;
- a repository install cannot mutate user, system, or gateway workspace roots;
- remote hosts receive skills only through an explicit deployment/install
  action rather than assumed filesystem sharing;
- production canaries prove one shared skill, one coding-only repository skill,
  one gateway-only workspace skill, and one incompatible dependency result;
- rollback restores the previous catalog and configuration without changing
  session history; and
- every S0-S6 packet is linked to merged revisions and validation evidence.

## Validation Strategy

Each code-bearing packet runs focused package tests first, then the changed
package lint path. Packets that affect prompt assembly, runtime construction,
configuration, CLI contracts, Docker packaging, or deployment also run the
corresponding integration and build checks. Final rollout requires both native
coding and gateway canaries; unit tests alone cannot close S5.

Test fixtures must include:

- duplicate names at multiple scopes;
- nested repository working directories;
- a large metadata catalog that requires truncation and omission;
- malformed frontmatter and optional metadata;
- missing binaries, tools, MCP servers, and unsupported runtime products;
- a skill directory symlink inside an admitted root and a target escaping the
  allowed boundary;
- explicit activation before and after compaction/resume; and
- a coding task initiated through the admitted channel handoff.

## Security And Reliability Invariants

- Skills never grant tools, filesystem paths, network access, MCP servers,
  companion access, or execution permission.
- Ordinary gateway turns never discover arbitrary repository skills.
- Discovery is read-only and bounded in entries, files, bytes, path length,
  recursion, and prompt contribution.
- Scripts and references remain inert until an admitted tool explicitly reads
  or executes them under existing policy.
- Third-party origin metadata remains immutable and inspectable.
- System bundle replacement is atomic and recoverable.
- Prompt cache invalidation follows catalog revision identity, not repeated
  unbounded directory walks on every turn.

## Explicit Non-Goals

This roadmap does not:

- reproduce the complete Codex/ChatGPT plugin marketplace;
- automatically install operating-system packages or authenticate external
  services;
- make Codex-specific skills correct merely because their files parse;
- grant the gateway a general local shell or coding repository authority;
- introduce approval UI into the yolo-default coding runtime; or
- synchronize unrelated machines without an explicit deployment action.

## Roadmap Completion

The roadmap is complete only when S0-S6 are merged, the maintained deployment
has completed the one-time layout migration, native coding and gateway canaries
pass, rollback is documented and exercised, the architecture and user guides
match the deployed behavior, and no permanent legacy builtin/global discovery
path remains.

Further plugin packaging, marketplaces, skill recommendation/ranking, remote
catalog federation, and autonomous skill generation require separate measured
product evidence and a new roadmap admission.
