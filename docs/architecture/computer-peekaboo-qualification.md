# Peekaboo comparison: pinned candidate and isolation gate

Status: source audit and private artifact staging only, October 18, 2026. The
candidate has not been executed, installed, permission-approved, selected as a
backend or natively qualified. This is one remaining driver comparison in the
[computer-use roadmap](computer-use-roadmap.md), not an alternative input grant.

## Exact candidate

The inspected release is
[Peekaboo v4.9.5](https://github.com/openclaw/Peekaboo/releases/tag/v4.9.5),
source commit `260fc35320d6c350af503338b944d68311e2bebb`. Its MIT license and
verified tag signature were inspected. The privately staged Intel archive and
executables have these SHA-256 digests:

| Artifact | SHA-256 |
| --- | --- |
| `peekaboo-macos-x86_64.tar.gz` | `0d3aaf6f45d2980b973b213f05ad3af826968368127872e6d2d6a40057193faf` |
| `peekaboo` | `fb47d42003cfb2aae3c81a26ef929e817be01df0af2ea3fdf7cf7602153ec6a4` |
| Accompanying dylib | `3c7dc6e74fd8cf75370ffed1954dfb263ac7dc9cd681090257096330a176ff52` |

Offline strict code-signature verification passed for the executable and dylib;
the executable identifies `boo.peekaboo.peekaboo`, OpenClaw Foundation team
`FWJYW4S8P8`, with a macOS 15.0 minimum. Archive inspection found only ordinary
relative files, without traversal paths, symlinks or devices. These checks do
not establish runtime behavior, TCC grants or compatibility with MintClaw.

## Configuration isolation is not snapshot isolation

The pinned
[configuration manager](https://github.com/openclaw/Peekaboo/blob/260fc35320d6c350af503338b944d68311e2bebb/Core/PeekabooCore/Sources/PeekabooAutomation/Configuration/ConfigurationManager.swift)
accepts `PEEKABOO_CONFIG_DIR`. Legacy migration must separately be disabled with
`PEEKABOO_CONFIG_DISABLE_MIGRATION=1`; a private config root alone does not prevent
legacy configuration import. Any future harness must also exclude ambient
credentials and agent/model routes without replacing `HOME`.

However, the CLI's
[local service factory](https://github.com/openclaw/Peekaboo/blob/260fc35320d6c350af503338b944d68311e2bebb/Apps/CLI/Sources/PeekabooCLI/Commands/Base/Runtime/RuntimeServiceFactory.swift)
constructs the ordinary `SnapshotManager`. Its public initializer leaves the
storage override unset, while the initializer accepting a storage URL is
internal to its module. The
[snapshot path implementation](https://github.com/openclaw/Peekaboo/blob/260fc35320d6c350af503338b944d68311e2bebb/Core/PeekabooAutomationKit/Sources/PeekabooAutomationKit/Services/Support/SnapshotManager+Helpers.swift)
then resolves `FileManager.default.homeDirectoryForCurrentUser` plus
`.peekaboo/snapshots`, independently of `PEEKABOO_CONFIG_DIR`, and creates that
directory when resolving storage.

Therefore a stock CLI snapshot workflow with only the config override does not
satisfy MintClaw's disposable-state qualification contract. This conclusion is
from source inspection, not an observed write or a claim that every CLI command
accesses snapshots. No existing user snapshot directory was inspected or
modified to test it.

## Local routing is a separate boundary

The pinned
[runtime resolver](https://github.com/openclaw/Peekaboo/blob/260fc35320d6c350af503338b944d68311e2bebb/Apps/CLI/Sources/PeekabooCLI/Commands/Base/Runtime/RuntimeHostResolver.swift)
recognizes explicit local isolation, including `PEEKABOO_NO_REMOTE`, and avoids
known remote endpoint resolution in that mode. A prospective harness must use
an explicit caller-local route and verify the actual approved companion's TCC
responsibility context. Existing Bridge hosts, automatic daemon startup and
permission fallback are not substitutes for that identity. Local routing alone
does not solve snapshot storage, visualizer, logging or coordination isolation.

## Required next slice

Before executing a comparison, choose and test an adapter or patched private
build with independently verified disposable snapshot storage and bounded
coordination state. Audit service initialization, logging and visualizer paths;
do not assume that disabling the agent removes every optional service. A
library-based adapter may be appropriate, but has not been selected or built.

Use the same independently observed disposable-window effects as Cua, with
exact native target binding and literal text semantics. Direct CLI typing is
not yet qualified as MintClaw's protected arbitrary-text transport. Do not
enable clipboard/paste/watchers, general agent/MCP/shell tools, new permission
prompts, remote-host fallback or production desktop access for the comparison.
If the candidate cannot meet a gate, report that limitation rather than
claiming a pass from help output, a signature or an acknowledged action.
