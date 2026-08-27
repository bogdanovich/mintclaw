# Browser Functional Parity Phase 6 And Global Completion Evidence

## Status

Browser Functional Parity Phase 6, **BF2 Media, Transfer, Diagnostics, And
Snapshot Delivery**, is merged, deployed, and complete. The complete six-phase
Browser Functional Parity Execution Goal is also complete.

The production gateway and configured Darwin companion run binaries built from
merged `main` commit `8418b02158a2117d8a4d86ad5aead91e0e8f4590`. This record
closes the Phase 6 acceptance criteria in
[Browser Functional Parity Execution Goal](../architecture/browser-functional-parity-execution-goal.md)
and the stop conditions in
[Browser Capability BF2 Media, Transfer, Diagnostics, And Snapshot Admission](../architecture/browser-capability-bf2-media-transfer-diagnostics-admission.md).

## Merged implementation

The earlier phase evidence remains authoritative for Phases 1 through 5. The
principal Phase 6 implementation and prerequisite repair pull requests are:

| Slice | Pull request | Merge commit |
| --- | --- | --- |
| BF2 admission | [#755](https://github.com/bogdanovich/mintclaw/pull/755) | `e4736c5d` |
| Companion output-transfer foundation | [#756](https://github.com/bogdanovich/mintclaw/pull/756) | `5c76fe93` |
| Shared screenshot foundation | [#757](https://github.com/bogdanovich/mintclaw/pull/757) | `a8b31259` |
| Companion screenshot transfer | [#758](https://github.com/bogdanovich/mintclaw/pull/758) | `737a47b1` |
| Native screenshot driver coverage | [#765](https://github.com/bogdanovich/mintclaw/pull/765) | `cdcbeaeb` |
| Companion upload and download | [#768](https://github.com/bogdanovich/mintclaw/pull/768) | `4870cf2e` |
| Model-visible retained screenshots | [#779](https://github.com/bogdanovich/mintclaw/pull/779) | `b20b9886` |
| Session diagnostics | [#847](https://github.com/bogdanovich/mintclaw/pull/847) | `1adb08f7` |
| Large-snapshot production-WSS delivery | [#857](https://github.com/bogdanovich/mintclaw/pull/857) | `2c6edf6c` |
| Trusted action-effect correction | [#842](https://github.com/bogdanovich/mintclaw/pull/842) | `bda82453` |
| Companion runtime readiness | [#844](https://github.com/bogdanovich/mintclaw/pull/844) | `96a8132a` |
| Catalog upgrade | [#865](https://github.com/bogdanovich/mintclaw/pull/865) | `02d6aece` |
| Snapshot transfer budget | [#867](https://github.com/bogdanovich/mintclaw/pull/867) | `b267e60e` |
| Objective receipt accounting | [#869](https://github.com/bogdanovich/mintclaw/pull/869) | `631aaff8` |
| Driver process-tree cleanup | [#874](https://github.com/bogdanovich/mintclaw/pull/874) | `8757b074` |
| Companion stale-authority repair | [#875](https://github.com/bogdanovich/mintclaw/pull/875) | `45027a54` |
| Post-navigation context recovery | [#876](https://github.com/bogdanovich/mintclaw/pull/876) | `abe893a7` |
| Browser-store retention | [#877](https://github.com/bogdanovich/mintclaw/pull/877) | `4ecd74c3` |
| Observation publication | [#882](https://github.com/bogdanovich/mintclaw/pull/882) | `1be5c6e6` |
| Cross-placement parity harness | [#884](https://github.com/bogdanovich/mintclaw/pull/884) | `e07c9dff` |
| Attributed-download interception repair | [#910](https://github.com/bogdanovich/mintclaw/pull/910) | `474256f0` |
| Unknown-effect external-action receipts | [#913](https://github.com/bogdanovich/mintclaw/pull/913) | `8418b021` |

Every code pull request passed its required lint, security, unit, race,
integration, Darwin, Windows, and browser checks before merge. The final
receipt repair initially encountered an unrelated companion terminal-worker
timing failure in the general test job. The unchanged functional diff passed
all nine jobs on a fresh latest-main head.

## Exact deployment

The exact merged artifacts were built with Go 1.26.6 and installed before the
live completion matrix:

| Artifact | SHA-256 |
| --- | --- |
| Linux gateway and CLI | `c64259f5497bca2b340ae91a54b7482b6b9476fda0a95b1468ee79c84a163831` |
| Linux companion | `7f2311e1778e9aee94bae13eeec8d1364c3fbbc0965950d49a12b5d3a82bfa04` |
| Darwin amd64 companion | `4ad938e3377d66fef037224e39ee83850da5adb50819e00f33fedd37e1ca6d96` |
| Linux launcher | `6b32cb590788da0663b34eff6a77ae7740a77bf242d763308399278ef386eef0` |

Both the gateway and companion report
`v0.1.0-p8a.2-844-g8418b021`. The gateway, launcher, five gateway profiles,
Linux canary companion, and Darwin LaunchAgent were restarted only through
their named service managers. Aggregate active-config hash
`f32cb4a031d8f0f4af57491a2a5e795f8e99887c2a33bc5458c0a05f0fb93cc2`
was identical before and after deployment.

A post-deployment live-gateway smoke returned exactly
`BROWSER_PARITY_DEPLOY_8418B021_OK`. Trace
`trace-turn-e3450cdec38fcb000e6489f0` has schema
`mintclaw.diagnostic_trace.v1`, completed with eight records, redacted content,
and no truncation.

## Capability catalog

Final catalog trace `trace-turn-729dc244f4669f9c49ee5736` completed with 11
records and no truncation. For the managed profile, both placements were
`ready`, used `any_http`, were non-dry-run, allowed approved actions, and
advertised the same shared action set:

```text
check, click, dialog, download, drag, file_chooser, fill, hover,
navigate, press, scroll, select, uncheck, upload
```

Both advertised tabs, popups, frames, page and element screenshots, upload,
download, and diagnostics. Common limits were identical, including one
session, four tabs, 262,144 snapshot bytes, 500 snapshot references, 8 MiB
screenshots, 32 MiB uploads and downloads, and a 327,680-byte tool-result
budget. The gateway alone advertised its intentionally placement-specific
headed-view and handoff support.

## Live BF2 matrix

### Screenshots, upload, diagnostics, and large snapshots

Equivalent gateway and companion canaries used only first-party browser tools.
Each placement:

- observed a source-bounded large snapshot with `truncated=true`, exactly
  262,144 snapshot bytes, and 500 semantic references;
- returned one redacted console-error summary and bounded failed-request
  summaries without bodies or headers;
- discovered a child frame and restored the primary context;
- retained page and element screenshots as opaque artifacts;
- uploaded an authorized retained screenshot through an exact approved
  `unknown` action; and
- closed, immediately reopened at `about:blank`, and closed the managed
  profile again.

Gateway screenshot artifacts included `artifact_73309bd906b1e468970e1128222e8d61`
and `artifact_42f345701d9a787820a07bf251d946dd`. Companion artifacts included
`artifact_3796e5b470f50956e0014668be29ae03` and
`artifact_5c6216ed0d665f21c0548eb941d66531`. The approved upload invocations
`invocation_9200434399eb051fa938baf44d024c29` and
`invocation_956c8140978137fe7800270fc4c6a6ea` both reached terminal success.

### Download and receipt-bound completion

The final synthetic download used a standards-valid anchor download plus a
response with `Content-Disposition: attachment`. Gateway interaction
`interaction_0f3e77b3687cb3f2e376f35d772ad362` bound exactly one receipt,
`invocation_1f0a85f94059c46a9e4f000d8c814bd3`, to its declared external-action
objective. Companion interaction
`interaction_20bef211d78412be0ecf4750d2e5fae7` similarly bound
`invocation_f3dff59528a656887b23b674020ea0f9`.

Both downloads completed once with trusted effect `unknown`, retained a
41-byte `text/plain` artifact named `mintclaw-bf2-download.txt`, and produced
SHA-256 `92e8f8b31b4ce7790e746380056c7dbc655016537ebf0d0456ab187f57751d02`.
Gateway artifact `transfer-artifact://639649f4d350d99eee9cac8d71add731`
and companion artifact
`transfer-artifact://7679e8282a61f5faa4cdbf6c58fbd757` were committed.
Both workflows then completed `close -> reopen -> observe about:blank ->
close` and returned canonical successful JSON outcomes.

The corresponding completed traces are
`trace-turn-68933ac22addeb582c820a60` for gateway and
`trace-turn-5f742b557a12adaa2655db42` for companion. Their approval-bound
predecessor traces are suspended rather than falsely completed, and every
trace is redacted, untruncated, and has zero dropped records.

One preliminary gateway validation deliberately was not replayed after its
action completed: the caller had declared the canary only as a read-only
result, so the new receipt was correctly rejected as unclaimed. The fresh
receipt-bound canaries separated the external action from the result objective
and passed. This provides live fail-closed and no-blind-replay evidence.

## Privacy, boundedness, and cleanup audit

The final production audit found:

- 87 retained browser sessions in `closed` state and 19 historical sessions in
  terminal `lost` state, with zero nonterminal sessions;
- zero nonterminal browser invocations;
- 17 bounded transfer-spool records, all `committed`, totaling 1,130,722
  observed bytes, with no pending or staging transfer;
- zero phase-owned gateway or companion browser-driver processes and zero
  managed-profile lock holders after the final close;
- both temporary local and remote BF2 fixture servers stopped;
- every expected MintClaw service active, zero failed units, zero legacy
  product processes, expected launcher HTTP 302 and reviewer HTTP 404 probes,
  and zero error-level journal entries in the final ten-minute window; and
- all five active profile configs parsed through `doctor.v1` with zero load
  errors. Existing policy findings were not changed by this deployment.

The gateway and companion receipt traces were scanned only across browser-tool
argument, result, and error previews. All four scans returned zero matches for
host paths, base64 data, authorization headers, cookies, storage state, and
driver internals. Artifacts retained only owner-bound references, metadata,
digests, bounded sizes, and expiry.

Historical Playwright MCP artifacts remain immutable history. A Playwright MCP
process owned by a separate spouse profile was outside this goal and was not
treated as a phase-owned orphan. The validated browser specialist workflows
used only the shared first-party tools.

## Global completion audit

| Global criterion | Completion evidence |
| --- | --- |
| Every phase criterion is satisfied | Phases 1 through 4 were completed before the Phase 5 record; [Phase 5 evidence](browser-functional-parity-phase5-evidence.md) closes the remaining ordinary interactions; this record closes the admitted Phase 6 scope. |
| Every required pull request is merged | The Phase 5 record lists its admissions, implementations, and repairs. The Phase 6 table above lists every principal foundation, implementation, conformance, lifecycle, and regression-fix pull request through `8418b021`. |
| Exact merged binaries run on both placements | The deployment section records the common version, artifact digests, unchanged configuration digest, and post-deployment smoke trace for the gateway and configured Darwin companion. |
| Common features have one contract | Catalog trace `trace-turn-729dc244f4669f9c49ee5736` shows the same actions, common features, policy mode, and limits on both placements; only headed-view and handoff remain an explicit gateway placement difference. |
| A first-party-only real-browser workflow covers documents, forms, screenshots, and transfer | The Phase 5 gateway and companion canaries cover forms and document contexts. The Phase 6 canaries cover large pages, child frames, retained screenshots, upload, and download through only first-party tools. |
| Production state passes privacy, boundedness, and cleanup checks | The audit above records trace scans, terminal session and invocation state, committed bounded artifacts, stopped fixtures, released profile locks, zero phase-owned browser processes, and healthy services. |
| No-replay and terminal recovery remain intact | The preliminary unclaimed action was not replayed, its receipt failed closed, both fresh receipt-bound actions ran exactly once, and every final session closed and immediately reused its profile. |
| Final docs describe shipped and deferred behavior | This record, the execution goal, roadmap, architecture index, and operations index identify the completed scope, exact evidence, rollback, BF3 proposal, BF4 deferral, and excluded BF2 features. |

## Rollback

The server rollback snapshot is:

```text
/home/server/mintclaw-deploy-backup-20260826T234500Z
```

It contains the previous gateway, CLI, Linux node, launcher, systemd user
tree, gateway run scripts, service states, and checksums. The actual previous
gateway executable is retained as `bin/gateway-linux-amd64`; a preserved
dangling `bin/gateway` symlink records the earlier topology alias and is not a
rollback payload.

The previous Darwin companion binary is:

```text
/Users/ab/.local/bin/mintclaw-node.backup-20260826T234500Z
```

Rollback restores only the affected executable from these snapshots and
restarts its named gateway unit or LaunchAgent. Mutable browser and artifact
state is not rolled back over newer records.

## Completion and deferred work

All six phase-level and global completion criteria are satisfied. The ordinary
first-party contract now covers semantic document contexts, form interaction,
screenshots, upload, download, diagnostics, and bounded large snapshots on
gateway and companion placements without exposing raw Playwright MCP tools.

BF3 privileged Playwright execution remains a separate, disabled-by-default
future proposal. BF4 managed runtime distribution remains deferred
until deployment evidence demonstrates a packaging or offline reliability
need. PDF, HAR, video, device emulation, locale, timezone, geolocation,
clipboard, browser permissions, and headed handoff parity remain explicitly
outside this completed six-phase goal.
