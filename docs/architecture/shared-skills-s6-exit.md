# Shared Agent Skills S6 Exit Record

Status: complete and deployed on 2026-09-26.

This record closes the
[Shared Agent Skills Roadmap](shared-skills-roadmap.md). It records the exact
merged revisions, production evidence, rollback assets, and boundaries that
remain outside the completed program.

## Delivered Revisions

| Packet | Pull request | Merge revision | Result |
| --- | --- | --- | --- |
| S0 | [#1298](https://github.com/bogdanovich/mintclaw/pull/1298) | `a6f49b81cc4629a9c7d87d3fe1c55c3210ac8674` | Admitted the roadmap and scope model. |
| S1 | [#1301](https://github.com/bogdanovich/mintclaw/pull/1301) | `7f0e6587ef73b49901fb428c569df109c2409dae` | Added one typed, bounded, runtime-aware catalog. |
| S2 | [#1308](https://github.com/bogdanovich/mintclaw/pull/1308) | `5b19e036d5b05b1a175858301dafc2029dfdf0a6` | Added coding selection and full instruction loading. |
| S3 | [#1314](https://github.com/bogdanovich/mintclaw/pull/1314) | `7c37888326998c11a1fc99636389f5d6d0df35a0` | Added the fingerprinted embedded system bundle and clean cutover. |
| S4 | [#1328](https://github.com/bogdanovich/mintclaw/pull/1328) | `4182c7788964a68d75feceba1bde94fdf1616c2a` | Added compatibility and dependency diagnostics. |
| S5 | [#1338](https://github.com/bogdanovich/mintclaw/pull/1338) | `53332e05087fe4339cb618d48317a8365bac7daf` | Published the portability audit and curated bundle. |
| S6 | [#1343](https://github.com/bogdanovich/mintclaw/pull/1343) | `4dc0d2e3eafe2596b842da84a191cdf80f5fa60a` | Added scoped lifecycle management, dry runs, self knowledge, and guides. |
| S6 rollout repair | [#1359](https://github.com/bogdanovich/mintclaw/pull/1359) | `b95a5ca9fa641a3c504f817e3a9009db5c85aec8` | Tightened natural-language self-management guidance. |
| S6 intent enforcement | [#1362](https://github.com/bogdanovich/mintclaw/pull/1362) | `c64eb99d9d4baeaf2cce52f4550b5d28dd82fafa` | Enforced install ownership across approval continuation and ambiguous requests. |

All code-bearing packets passed their focused tests, changed-package lint, and
repository CI before merge. The final implementation pull request had all 13
required checks green, no unresolved review threads, and a clean merge base.

## Final Ownership Contract

| Scope | Canonical root | Coding agent | Gateway |
| --- | --- | --- | --- |
| `system` | `$MINTCLAW_HOME/skills/.system` | visible when compatible | visible when compatible |
| `user` | `$HOME/.agents/skills` | visible when compatible | visible when compatible |
| `repository` | admitted `<repo>/.agents/skills` | visible in repository context | absent from ordinary chat turns |
| `workspace` | `$MINTCLAW_HOME/workspace/skills` | absent | visible to that gateway workspace |

The CLI defaults an otherwise unqualified interactive install to `user`.
Repository and workspace mutation require explicit scope. The gateway tool
also requires scope, cannot invent repository identity, and reports the
canonical target in dry-run and mutation results. `system` remains an immutable
release-owned target.

The bundled `mintclaw-agent` skill is the model-facing source of truth for
these rules. User intent such as “install for yourself” or “make this available
to both agents” resolves to `user`; it must not inherit the gateway process
directory or silently write to the gateway workspace. Contradictory scope and
intent fail before download or filesystem mutation. An approved continuation
retains the originating durable user request; if it is unavailable, the
installer fails closed.

## Production Rollout

The maintained host was upgraded from merged `main` revision
`c64eb99d9d4baeaf2cce52f4550b5d28dd82fafa`. The installed core reports:

```text
mintclaw v0.1.0-p8a.2-2309-gc64eb99d9 (git: c64eb99d)
Go go1.26.6
```

The core binary was installed atomically. Five affected gateway services
(`main`, `family`, `nutrition`, `reviewer`, and `spouse`) were restarted. All
ten MintClaw product services were active afterward, no product or global
systemd units were failed, no legacy units remained, and the five restarted
gateways emitted no warnings or errors in the post-deploy window. All six
maintained configurations loaded with zero Doctor errors; the main Doctor
summary remained unchanged at 14 failures, 8 warnings, and 7 informational
findings, which are existing environment diagnostics rather than config-load
regressions.

## Canary Evidence

The initial S6 rollout exercised and cleaned four isolated catalog cases:

1. a user-scoped weather skill was visible to coding and gateway from the same
   `$HOME/.agents/skills/weather/SKILL.md` file;
2. a repository-scoped GitHub skill was visible to coding and absent from an
   ordinary gateway catalog;
3. a workspace-scoped tmux skill was visible to its gateway and absent from
   coding; and
4. a deliberately missing executable produced the exact dependency diagnostic
   without attempting installation.

The final repaired build then ran a natural-language gateway canary asking the
agent to find the bundled weather skill and plan, but not perform, an install
“for yourself.” It called `install_skill` with:

```json
{
  "dry_run": true,
  "scope": "user",
  "slug": "bogdanovich/mintclaw/pkg/skills/bundled/weather",
  "version": "c64eb99d9d4baeaf2cce52f4550b5d28dd82fafa"
}
```

The result selected `user`, reported the exact target
`/home/server/.agents/skills/weather`, advertised both coding and gateway
visibility, and changed no files. Both that path and the wrong gateway target
`/home/server/.mintclaw/main/workspace/skills/weather` were absent before and
after the canary.

The passive diagnostic trace is
`trace-turn-6a551f470d10068e86063572`. It is a complete, redacted
`mintclaw.diagnostic_trace.v1` trace with 29 records and no truncation. Tool
sequence 21 contains the exact arguments above; sequence 22 records a planned
user-target create with no mutation. The agent first read the deployment's
older `mintclaw-development` workspace wrapper and attempted its no-longer
present shared-skill path. Those read-only deployment-specific lookups did not
alter the resolved scope or target and are not part of the product invariant.

A separate basic live canary returned the exact token `MINTCLAW_LIVE_OK` in
3.9 seconds. Its complete redacted trace is
`trace-turn-feec074d1d6ab4e786be977c`.

Two earlier safe dry runs exposed the defects fixed during rollout:

- `trace-turn-e0eb15133a6b1d2bb1469b74` selected workspace scope and led to
  [#1359](https://github.com/bogdanovich/mintclaw/pull/1359); and
- `trace-turn-17d6d4449e590810ab5e02af` still selected workspace scope and led
  to the runtime enforcement in
  [#1362](https://github.com/bogdanovich/mintclaw/pull/1362).

Neither attempt wrote a target. A concurrent one-time Seahorse generation-4
reconciliation appears in `trace-turn-0de75ee060dd5a41657168a5`; it accounts
for an unrelated pre-model delay and did not restart the gateway or delete
history.

## Rollback And Cleanup

The pre-deploy production state is retained at:

```text
/home/server/backups/mintclaw-s6-final-20260927T044540Z
```

It contains the previous core, node, and launcher binaries and checksums;
configuration; skill roots; user systemd units and drop-ins; service state;
Doctor reports from before, candidate, and after deployment; and canary data.
Rollback restores the backed-up binaries, configuration, skill roots, and unit
definitions, then restarts only the affected services. Session history, tasks,
interactions, memory, and diagnostic traces are outside the replacement set.

Canary skills were removed after verification and the catalogs returned to
their pre-canary contents. The production source checkout was clean after
deployment and its built binary checksum matched the installed binary.

## Completion Matrix

- One user skill was discovered by both runtimes without duplicate files.
- Repository and workspace skills remained isolated to their intended runtime.
- Missing dependencies were reported without side effects.
- CLI, agent self knowledge, and the gateway tool agree on scopes and target
  paths.
- Natural-language user ownership survived approval continuation and selected
  the user root in production.
- Dry-run planning, immutable origin identity, and bounded inventory checks
  were exercised without mutation.
- Rollback inputs and selective service recovery are preserved without
  touching conversational state.
- Every S0-S6 packet and both rollout repairs are linked above.

## Follow-up Boundaries

This exit does not admit a plugin marketplace, automatic dependency
installation, implicit cross-machine synchronization, arbitrary repository
access from gateway chat, or autonomous skill generation. The feature-owned
PDF skill is now present under the same bundle contract, but its workflow and
future expansion remain owned by the PDF program rather than this roadmap.
Future ranking, recommendation, federation, or marketplace work requires a
new measured roadmap rather than reopening this one.
