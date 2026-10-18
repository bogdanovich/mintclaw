# Actual-app native input qualification

Status: private fixture harness only; no public computer-input tool or host is
enabled. This independent slice uses the existing native fixture and companion
transport, without depending on the proposed input-contract or Linux-driver PRs.

## macOS permission identity

Run the input mechanisms under the already installed and approved companion:

```bash
scripts/computer-companion-native-smoke.sh \
  --suite input \
  --driver /absolute/private/cua-driver \
  --node '/Users/ab/Applications/MintClaw Companion.app/Contents/MacOS/mintclaw-node' \
  --app '/Users/ab/Applications/MintClaw Companion.app'
```

The script verifies the original pinned driver checksum and that the selected
node binary matches the actual app. It builds a private controller and native
test helper, then uses the existing current-GUI launcher and numeric child-exit
receipt. The controller starts a separate instance of that actual app with a
temporary configuration pointing only to its own pinned loopback TLS gateway.
It never edits the installed app, TCC settings or production configuration.

This temporary companion policy admits only `system.exec.v1`, exactly the native
test helper executable, its private working directory and four explicit
environment names. The app launches the helper as its own child; neither an SSH
process nor Herdr is substituted for the app's macOS permissions. This route is
test orchestration, not the implementation of public computer actions through a
generic executable allowlist. Production computer hosts remain read-only.

The helper owns two disposable windows and checks the actual native worker's
non-prompting Accessibility and Screen Recording status. It independently
observes fixture application events for complete Unicode insertion, distinct
Unicode replacement and button effects, checking that the guard window is
unchanged. It also checks denial of the non-admitted guard window. A driver
acknowledgement alone does not establish an effect.

The controller requires every named effect test to pass, both permissions to be
granted, an untruncated result, empty stderr and a successful child exit. An
empty test selection, skipped effect, denied permission, truncated output or
failed child cannot be reported as a native qualification pass. Normal CI skips
the native run unless explicitly staged; deterministic result-verifier tests run
without macOS permissions. The launcher refuses a known locked/login screen;
it does not retry after unlocking or change OS security settings automatically.

## Limits and remaining gates

This harness is macOS-only because the Linux direct fixture run already uses
the explicitly selected GUI session, whereas macOS needs the actual app's TCC
responsibility context. It does not qualify Linux input, a rebuilt companion
identity, arbitrary native applications, pixel actions, hotkeys, scrolling,
dragging, cancellation, held-state cleanup, desktop-wide arbitration or model
tasks. It does not repair Cua's insertion-to-replacement fallback or establish
generic Linux accessibility-root/window identity.

Keep native effect results distinct from the full
[computer-use roadmap](computer-use-roadmap.md). Public input still requires
independent local admission, exact native targeting, protected ephemeral payloads,
durable no-replay outcomes, cancellation and independently verified post-action
evidence. Production deployment and desktop enablement need separate approval.
