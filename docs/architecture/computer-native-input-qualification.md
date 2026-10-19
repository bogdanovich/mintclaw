# Actual-app native input qualification

Status: private fixture harness only; no public computer-input tool or host is
enabled. The harness uses the existing native fixture and companion transport.
Its actual-app permission path is independent of the bounded input contracts
and corrected Linux driver artifact; all three prerequisites are now merged.

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

## Nonempty caret corpus

The Mac fixture now accepts the same controller-only bounded text/caret seeding
protocol as the GTK fixture. It uses the real AppKit field editor and selection,
not a custom accessibility setter or a model-visible setup command. Protocol
carets count Unicode scalars; AppKit ranges count UTF-16 units. Desktop-free
Swift self-tests cover the conversion, empty text, bounds and malformed values.

Seven insertions exercise ASCII, Cyrillic, CJK, accented and combining text, an
emoji ZWJ sequence and Arabic between exact sentinels. The prefix itself includes
non-BMP, ZWJ and combining scalars, so a byte/UTF-16/grapheme index confusion
cannot accidentally pass. Every case independently observes complete application
state and an unchanged sibling field. The actual-app controller requires all
seven named passes as well as its original effect checks; an old empty-field-only
helper cannot establish this corpus's qualification.

Before the first unlocked expanded run, the Mac helper's full-suite context
budget is fixed at 150 seconds (formerly 90 for the smaller suite), with 160-second
process, 170-second private command, 180-second invocation and four-minute outer
test limits. This is qualification-only orchestration, not a production timeout
change. Linux retains its original 90-second helper budget. No timeout is raised
in response to an observed corpus performance failure.

The first expanded Mac attempt refused at the lock-screen preflight with exit
75; no new successful Mac corpus measurement is established by that attempt.

## Historical empty-field fixture results

On October 18, 2026, the actual installed Intel Mac companion completed this
suite in 41.47 seconds with both worker permissions granted. Independent fixture
events verified insertion (10.56 seconds), replacement (10.03), click (9.59) and
scope denial (8.74). The app and original pinned driver were not rebuilt or
reinstalled. No private fixture, worker or companion instance remained afterward;
the unrelated existing companion remained running.

The final reviewed head in merged
[#1774](https://github.com/bogdanovich/mintclaw/pull/1774) repeated all four checks
in 47.83 seconds using newly compiled controller/helper executables: insertion
13.33 seconds, replacement 13.34, click 10.07 and scope denial 8.48. All 14 CI
checks and automated review passed. These are complete fixture-run timings, not
per-action model latency or a full representative-task score. The original
Linux artifact separately passed replacement/click/denial in 2.00 seconds after
the platform-schema correction; that run does not qualify Linux insertion.

The first attempt refused the locked desktop with exit 75. After unlocking, a
second attempt exposed an invalid qualification request: Mac `set_value` is
always-background semantic AX mutation and its pinned schema rejects the Linux
`delivery_mode` property. Following each platform's exact schema fixed the
request; the complete Unicode and independent guard-window assertions stayed
unchanged. These earlier refusals/failures are not counted as passing tests.

## Expanded nonempty-caret result

After operator unlock, the unchanged approved Intel Mac companion passed the
expanded corpus from the final reviewed head of merged
[#1788](https://github.com/bogdanovich/mintclaw/pull/1788),
`6891d33133b8e09909532ea064219290473e6e8b`, in 102.72 seconds. The child completed
in 101.18 seconds and the input group in 100.51 seconds. All seven middle-of-text
cases passed, individually taking 9.19–9.49 seconds; the complete insertion
group took 74.72 seconds. Exact non-BMP/ZWJ/combining prefix and suffix, complete
inserted text and unchanged sibling state were independently observed.
Replacement (8.70 seconds), click (8.83) and scope denial (7.73) also passed.

The frozen budgets were unchanged. The installed app and original pinned driver
were not rebuilt, replaced or re-permissioned. Private fixture processes exited
and the unrelated original companion remained running. The prior locked
preflight remains a refusal, not part of this passing measurement.

A freshly compiled Linux helper from that same final head independently repeated
the selected attached GNOME/X11 fixture in 3.07 seconds, with the input group in
2.78 seconds and all seven caret cases in 1.32 seconds. Replacement, click and
scope denial also passed, with the previously qualified patched Linux driver.
This repeat is separate from the earlier 3.22/4.57-second candidate runs; it is
not a replacement for their recorded provenance or failures. All 15 CI checks
and automated review passed before #1788's authorized merge.

## Limits and remaining gates

This harness is macOS-only because the Linux direct fixture run already uses
the explicitly selected GUI session, whereas macOS needs the actual app's TCC
responsibility context. The actual-app harness does not itself qualify Linux
input, a rebuilt companion identity, arbitrary native applications, pixel actions,
hotkeys, scrolling,
dragging, cancellation, held-state cleanup, desktop-wide arbitration or model
tasks. The earlier 41.47/47.83-second Mac measurements began in an empty field;
they do not qualify the new nonempty-caret corpus. It does not repair Cua's
insertion-to-replacement fallback or establish
generic Linux accessibility-root/window identity.

Keep native effect results distinct from the full
[computer-use roadmap](computer-use-roadmap.md). Public input still requires
independent local admission, exact native targeting, protected ephemeral payloads,
durable no-replay outcomes, cancellation and independently verified post-action
evidence. Production deployment and desktop enablement need separate approval.
