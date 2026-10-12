# Local Coding Agent P8 Platform Qualification

Parent plan:
[P8 hardening and stable release](local-coding-agent-p8-hardening.md).

Status: qualification contract merged in PR `#1604`, exact head
`d1025ea549d46f855a88c7c604fde68fa820be31`, merge
`5b87df2b55a25486c06db7001c0dc46704e33bea`. All 17 required checks passed,
including native macOS and Windows platform contracts, with a clean exact-head
review and owner merge approval. H6 retains the exact-source release snapshot
and integrated production exit.

## Supported release matrix

MintClaw distinguishes an emitted release artifact from a fully qualified
interactive coding environment. The stable release configuration emits only
these tuples:

| Artifact | OS | Architecture | Qualification |
| --- | --- | --- | --- |
| `mintclaw`, `mintclaw-launcher` | Linux | AMD64 | Build, native tests, interactive TUI, process trees, paths, locks, worktrees |
| `mintclaw`, `mintclaw-launcher` | Linux | ARM64 | Cross-build; headless runtime expected, hardware-specific TUI behavior not claimed |
| `mintclaw`, `mintclaw-launcher` | Linux | ARMv7 | Cross-build; headless runtime expected, hardware-specific TUI behavior not claimed |
| `mintclaw`, `mintclaw-launcher` | macOS | ARM64 | Build, native tests, interactive TUI, process trees, paths, locks, worktrees |
| `mintclaw`, `mintclaw-launcher` | macOS | AMD64 | Cross-build; platform-sensitive native coding CI currently runs on macOS ARM64 |
| `mintclaw`, `mintclaw-launcher` | Windows | AMD64 | Build and native headless coding, path, lock, worktree, and Job Object process-tree tests |
| `mintclaw-node` | Linux | AMD64, ARM64 | Build; use requires an explicitly admitted companion target/profile |
| `mintclaw-node` | macOS | AMD64, ARM64 | Build; use requires an explicitly admitted companion target/profile |
| container images | Linux | AMD64, ARM64 | Headless gateway/runtime image |

FreeBSD, NetBSD, Windows ARM64, Linux ARMv6, RISC-V, LoongArch, and S390X are
not stable release tuples. Developers can experiment with source builds, but
MintClaw does not publish or imply support without a future focused admission.

## Terminal and shell contract

The interactive coding TUI is stable on Linux and macOS terminals. Its
qualification reuses the TUI.15 contract and adds native CI coverage for:

- ordinary inline rendering with native scrollback;
- temporary alternate-screen ownership for overlays;
- TTY and non-TTY selection, `TERM=dumb`, and no-color fallback;
- SSH-shaped and tmux environments, narrow geometry, resize/reflow, bracketed
  paste, Unicode grapheme width, and completed long output;
- interruption while work is active, signal/error/panic exits, and terminal
  restoration before controller shutdown; and
- launch from Bash and Zsh on Unix-like hosts.

Windows AMD64 is a stable native headless coding target. PowerShell launch,
plain/JSON output, durable threads, worktrees, and worker process containment
are qualified. A native Windows interactive PTY is not yet claimed; operators
should use `mintclaw code exec`, `mintclaw resume exec`, or WSL when they need
the interactive Unix terminal experience.

## Filesystem and process contract

Platform jobs exercise canonical project identity, owner-only thread locks,
lease recovery, isolated Git worktrees, and owned descendant termination.
Linux and macOS use process groups. Windows uses a suspended process assigned
to a kill-on-close Job Object before it is resumed. Cancellation must settle
without an owned orphan process on every admitted native execution path.

Coding remote IPC and paired-node coding remain Linux/macOS capabilities.
Windows compilation or headless local coding does not silently admit a Windows
companion, Unix-domain socket emulation, privileged helper, or interactive PTY.

## CI and release evidence

Pull requests compile the complete tagged Go test graph for every emitted
binary tuple, including both Darwin architectures. Native macOS and Windows
jobs run the platform-sensitive contracts above; Linux runs the complete test,
race, integration, and interactive PTY suites. Nightly and tagged releases use
the same narrowed GoReleaser and container matrices.

PR `#1604` provides the exact-head required check run. H6 must record a
GoReleaser snapshot from its final qualified source; cross-compilation alone
does not replace that release-path check.

### Non-publishing release qualification

Use the manual `Release Qualification` workflow on `main` with one full,
40-character merged-main `commit`. It rejects non-main dispatches and source
outside main's ancestry before running project hooks. It is not an additional
job on every pull request.

The workflow uses the normal release configuration to produce archives,
Linux installers, SBOMs and both AMD64/ARM64 core/launcher container images.
GoReleaser's [snapshot container mode](https://goreleaser.com/customization/package/dockers_v2/#testing-locally)
builds local per-platform images without publishing. Signing/notarization is
excluded from this unsigned qualification; release tags, public assets and
registries are not modified. The workflow has read-only repository permissions,
no registry login and no publishing credential passed to GoReleaser.

The three-day evidence artifact records the exact source, generated release
metadata, archives/installers/SBOMs, verified payload checksums and four local
container image identities. Record the run URL and artifact digest in the H6
exit. Device-specific behavior and production signing remain separate gates.

## Residual limits

- Cross-compilation proves build compatibility, not device-specific terminal,
  memory, kernel, filesystem, or service-manager behavior.
- Windows interactive PTY and Windows companions are not stable features.
- ARMv7 receives a core archive but no companion archive or container image.
- Privileged execution is governed by P7.7 profiles and platform-specific
  helpers; release availability never grants authority.
- Community hardware reports do not change this support matrix.
