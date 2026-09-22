# Skill Portability Audit

Status: S5 implementation record for the
[Shared Agent Skills Roadmap](shared-skills-roadmap.md).

The exhaustive inventory is
[`skill-portability-inventory.json`](skill-portability-inventory.json). Keep
that machine-readable file as the source of truth for individual paths. This
page records the selection boundary, conclusions, and update procedure without
placing 594 rows in the main architecture guide.

## Pinned sources

| Source | Pin | Skills | Selection boundary |
| --- | --- | ---: | --- |
| `openai/codex` | `94174e44cbc54cece45f6052328ca0c2cd7a8a2a` | 17 | Every `SKILL.md` in `.codex/skills` and the embedded sample tree |
| `openai/plugins` | `1dc195897af4161d039b80d8471ec0a10c9bbc89` | 536 | Every `SKILL.md` in the repository, including its root plugin-creator |
| installed `openai-bundled` distribution | `0cc1f7fd…c944d9` skill-tree SHA-256 | 4 | Every skill exposed from the active distribution root |
| installed `openai-curated-remote` distribution | `a27910df…f9d13d` skill-tree SHA-256 | 21 | Every skill exposed from the active distribution root |
| installed `openai-primary-runtime` distribution | `8f83709b…0616eb` skill-tree SHA-256 | 6 | Every skill exposed from the active distribution root |
| MintClaw baseline | `dc8108553d128fc9b620b1b77926d389e35589b9` | 10 | Every bundled skill before this audit |

The installed-distribution pins hash each relative `SKILL.md` path and its
content. They do not pretend that an unpublished cache package has a public Git
repository. Only active configured distribution roots are sources; unrelated
or superseded cache directories that are not exposed to this installation are
not silently added to the audit.

The generator at `scripts/skillinventory` refuses dirty Git inputs and exact
count drift. The committed inventory is independently validated in tests, so
CI does not need access to the external checkouts or local plugin cache.

## Results

| Decision | Count | Result |
| --- | ---: | --- |
| `port` | 0 | No upstream package already matched MintClaw tools, paths, policy, and runtime ownership without changes. |
| `adapt` | 1 | Codex `imagegen` became the gateway-only MintClaw `imagegen` skill. |
| `covered` | 13 | Existing MintClaw skills or native runtime features already provide the contract. |
| `defer` | 6 | A stable MintClaw capability or the S6 installer must land first. |
| `exclude` | 574 | These are repository-specific, vendor/plugin-specific, proprietary, conflicting, or inappropriate as system defaults. |

Exhaustive does not mean permissive. A specialized MIT- or Apache-licensed
plugin may be useful as an explicit user or repository install after S6, while
still being wrong for every MintClaw process by default.

### Adapted

`imagegen` is the only admitted import. The useful upstream material is the
routing and prompt-shaping workflow for raster generation and source-preserving
edits. The MintClaw adaptation:

- calls `image_generate`, not Codex `image_gen` or `view_image`;
- accepts the gateway's current-turn trusted image paths and `media://`
  references;
- follows MintClaw's multi-image delivery and provider-fallback contracts;
- removes `$CODEX_HOME`, local API scripts, key setup, approval behavior, and
  Codex UI metadata; and
- declares `gateway` plus `tool:image_generate`, so coding sees a deterministic
  runtime-incompatible result rather than unusable instructions.

The package carries the upstream Apache-2.0 license and a strict
`MINTCLAW_PROVENANCE.json` pinned to the audited Codex revision.

### Covered

- Codex `review-agent` is covered by native `/review`, which freezes the diff,
  uses bounded read-only review authority, and records a stronger lifecycle.
- Codex `skill-creator` is covered by MintClaw's bundled `skill-creator`.
- The installed PDF runtime skill is covered by MintClaw's feature-owned `pdf`
  skill and admitted document tool.
- The ten pre-audit MintClaw system skills remain their own canonical owners.

Covered entries are consolidated instead of copied under a second name.

### Deferred

- `openai-docs` needs an authoritative official-document retrieval contract;
  generic search would weaken its correctness guarantee.
- `skill-installer` waits for S6 because its Codex paths and ownership defaults
  are exactly the behavior MintClaw must replace with explicit scopes.
- documents, presentations, spreadsheets, and live Excel control wait for
  stable MintClaw artifact tool contracts. A prompt cannot substitute for
  missing hosted capabilities.

Deferred is not an implicit approval. The capability owner must publish a
stable tool/policy contract and rerun the admission checklist before bundling.

### Excluded from system defaults

The eleven Codex repository-maintenance skills depend on Codex's own project,
test infrastructure, or release workflow. The plugin-creator samples depend on
Codex plugin manifests and marketplace semantics. The public plugin catalog is
mostly connector-, vendor-, UI-, or domain-specific and has mixed licensing;
it remains available for later explicit review rather than becoming a global
prompt surface.

In particular, `superpowers` is not a generic default: it imposes mandatory
planning, approval, delegation, and workflow rules that conflict with
MintClaw's runtime authority and yolo-default coding policy. Licensing alone
does not make those instructions compatible.

The installed proprietary browser, Sites, visualization, template,
plugin-management, and template-creator packages are also excluded. MintClaw
must implement or explicitly license a native contract instead of copying a
parseable file from a product cache.

## Provenance enforcement

An imported bundled package with `MINTCLAW_PROVENANCE.json` is rejected before
system-bundle publication unless all of these hold:

- schema version is known and unknown fields are absent;
- source repository is an absolute credential-free HTTPS URL;
- source revision is a lowercase full Git SHA;
- source path is a safe relative slash path;
- license and decision are bounded and valid;
- `adapt` lists unique concrete adaptations while `port` lists none; and
- sibling `SKILL.md` and `LICENSE` files exist.

Validation happens while snapshotting the embedded source, before a staging
generation or active marker is changed. A malformed import therefore leaves
the previous verified generation active.

## Updating the audit

1. Fetch clean source checkouts and select the exact active cache roots.
2. Review upstream license and capability changes before changing a decision.
3. Run `go run ./scripts/skillinventory` with explicit source paths and the
   audit date.
4. Review the decision diff; generated output is evidence, not automatic
   admission.
5. For every new `port` or `adapt`, add license, provenance, compatibility
   metadata, trigger/non-trigger coverage, and runtime fixtures.
6. Run package tests and both advertised runtime canaries before deployment.

Upstream changes never update the deployed bundle automatically.
