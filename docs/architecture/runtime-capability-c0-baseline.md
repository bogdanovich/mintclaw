# C0 Runtime Capability Baseline

Status: implementation baseline for C0 of the
[Runtime Capability And Turn-Engine Convergence Roadmap](runtime-capability-convergence-roadmap.md)

Baseline: `origin/main` at `a78206277`, 2026-09-27

## Purpose

Freeze the current gateway and coding capability boundaries before C1 and C2
change construction and compatibility reporting. C0 does not make PDF or
browser available to coding and does not change runtime authority.

## Registry Construction Inventory

| Surface | Construction owner | Current contract |
| --- | --- | --- |
| Gateway file and shell tools | `pkg/agent/instance.go:initCoreAgentTools` | Config and per-agent tool policy decide registration. |
| Gateway shared feature tools | `pkg/agent/agent_init.go:registerSharedTools` | Process services, delivery state, feature configuration, and per-agent policy decide registration. |
| Gateway runtime tools | `pkg/agent/agent_inject.go` and feature wiring such as `pkg/gateway/browser_runtime.go` | Factories are injected into eligible agents and rebuilt across gateway config generations. |
| Gateway MCP tools | `pkg/agent/agent_mcp.go` | Server configuration and per-agent MCP policy decide registration and deferred exposure. |
| Coding tools | `pkg/agent/instance.go:initCodingAgentTools` | A sealed trusted-local catalog is constructed before the coding loop starts; ordinary dynamic injection and MCP bootstrap are disabled. |
| Coding attachments | `pkg/agent/agent_init.go:registerCodingMediaTools` | `coding_attachment` is added only when the thread supplies a coding media store. |
| Coding privileged execution | `pkg/agent/instance.go:initCodingAgentTools` | `privileged_exec` is added only for an explicitly admitted privileged executor. |

The normal mutable coding baseline is:

```text
append_file
apply_patch
exec
list_dir
read_file
repository_diff
repository_status
request_user_input
search_files
update_plan
write_file
```

Read-only coding omits `append_file`, `apply_patch`, `exec`, and `write_file`.
`request_user_input` remains configuration-dependent. Context-manager-owned
retrieval tools, coding attachments, and privileged execution are explicit
additions rather than part of the fixed base list.

The existing coding runtime-profile tests freeze the mutable and read-only
lists. C0 additionally freezes the absence of document and browser tools from
configured coding compatibility.

## PDF Boundary

The bundled `pdf` skill currently declares:

```yaml
products: [gateway]
requirements:
  tools: [document]
```

The gateway registers `document` only when configuration enables it and the
`inspect`, `extract`, and `render` backends are supported. The tool starts
hidden and is reached through `tool_search_tool_bm25`; it is not initially included
in provider-visible tool definitions.

Coding does not register `document`. This is not only a manifest restriction:

- `DocumentTool.executionAuthority` requires an authority-bound media store;
- `media.NewMediaOwner` currently requires workspace, agent, actor, route,
  session, channel, and chat identity;
- gateway construction supplies protected form jobs, audit policy, a durable
  document-write state root, and outbound delivery inspection; and
- the coding media store does not currently provide the complete owned-media
  and durable document-artifact contract expected by `DocumentTool`.

Therefore the precise C0 coding incompatibility is: the product manifest is
gateway-only, the required `document` tool is absent from the sealed coding
registry, and the current tool authority/artifact lifecycle is gateway-shaped.
C3 must resolve those runtime contracts before changing the skill manifest.

## Browser Boundary

The bundled `agent-browser` skill currently declares:

```yaml
products: [gateway]
requirements:
  tools:
    - browser_act
    - browser_observe
    - browser_session
    - browser_targets
```

Gateway browser setup injects exactly these seven runtime tools into an
explicitly granted agent using an ordinary profile:

```text
browser_act
browser_capture
browser_contexts
browser_diagnostics
browser_observe
browser_session
browser_targets
```

`browser_execute` is an eighth, conditional tool. It is registered only when
the agent is granted a profile with privileged execution enabled and the
runtime source supports that separate execution contract.

Coding registers none of them. The narrow `BrowserToolSource` interface is
already reusable, but its production implementation and lifetime are owned by
`pkg/gateway/browser_runtime.go`. Browser calls also derive `browser.Owner`
from a canonical inbound channel actor plus agent, session, and execution
identity. A coding turn does not currently have that channel actor mapping or
an authenticated client to the existing broker.

Therefore the precise C0 coding incompatibility is: the product manifest is
gateway-only, every required browser tool is absent from the sealed coding
registry, the only production source is gateway-owned, and browser ownership
is derived from inbound channel identity. C4 must provide a runtime-neutral
principal and a single-broker client before changing the skill manifest.

## Frozen Tests

| Test | Boundary frozen |
| --- | --- |
| `TestNewCodingAgentLoopUsesExternalStateAndExactToolProfile` | Mutable coding base catalog and optional coding attachment. |
| `TestNewCodingAgentLoopReadOnlyAuthorityOmitsMutationTools` | Read-only coding catalog. |
| `TestConfiguredSkillCompatibilityEnvironmentKeepsCodingSurfaceIsolated` | Coding reports document and all browser tools missing, and keeps MCP disabled. |
| `TestGatewayDocumentCapabilitySurfaceBaseline` | Supported gateway document registration, BM25 discovery, and initial hidden exposure. |
| `TestBrowserToolsTrackAgentGrantAcrossReload` | Exact seven-tool ordinary gateway browser delta and removal after the grant is removed. |
| `TestBrowserExecuteBindsApprovalAndProtectsSourceAndLiveResult` | Conditional `browser_execute` exposure, approval binding, and source/result protection. |
| `TestBundledSkillManifestsHaveExpectedGatewayAndCodingCompatibility` | Exact PDF/browser product and tool requirements plus current coding runtime incompatibility. |

These are deliberate change detectors, not permanent product requirements.
Later packets must update them when they intentionally admit capabilities,
while preserving the authority and isolation assertions they protect.

## C0 Completion Evidence

C0 is complete when the focused tests above pass on the supported local test
environment, `make fmt`, changed-package lint, and documentation lint pass, and
the implementation PR merges without changing a production tool definition,
runtime policy, persistence schema, or deployment behavior.
