# Browser Continuation Phase 2 Evidence

## Status

Browser continuation Phase 2, **Direct Playwright-Library Driver**, is merged,
deployed, live-validated, and complete. Phase 3, **Privileged Browser
Execution**, is the next dependency-ordered phase.

The direct driver was admitted on merge commit
`7e87d4dec07b0c8fdaacc01af6aa3dc02e96e487`. Five focused deployment,
lifecycle, evidence, and smoke-contract follow-ups were subsequently merged.
The final source baseline used for closeout was
`18b759c8b0f70dd5c2ae27cab08d01f539d290f8`.

## Merged Work

| Pull request | Head | Merge commit | Outcome |
| --- | --- | --- | --- |
| [#1236](https://github.com/bogdanovich/mintclaw/pull/1236) | `ae39a43a` | `7e87d4de` | Added the pinned direct Playwright-library sidecar, selected it for gateway and companion, retained the MCP rollback driver, and added conformance and real-browser coverage |
| [#1238](https://github.com/bogdanovich/mintclaw/pull/1238) | `4f3a405d` | `887ecc46` | Kept provider-lifecycle smoke on its deterministic blank-page fixture |
| [#1239](https://github.com/bogdanovich/mintclaw/pull/1239) | `a7208255` | `ca5b284d` | Supplied a deterministic Node.js and npm search path to launchd-managed companions |
| [#1241](https://github.com/bogdanovich/mintclaw/pull/1241) | `c96782c6` | `ce218fb5` | Made direct-driver shutdown retryable after cancellation or an interrupted first cleanup attempt |
| [#1242](https://github.com/bogdanovich/mintclaw/pull/1242) | `f73b90ea` | `615c2c20` | Correlated live execution evidence with the exact parent root turn when the child session key differs |
| [#1243](https://github.com/bogdanovich/mintclaw/pull/1243) | `cc9ebe78` | `18b759c8` | Required each smoke stage to prove capability and perform its prescribed workflow in one browser session |

Each code pull request passed all ten required CI jobs. Automated review ran
against every exact head, all findings were resolved, and the owner rocket
approval was present before merge. Review of the initial driver found three
material issues before admission: serial dialog handling could deadlock, fill
validation did not prove focus semantics, and removing and re-adding a managed
driver could bypass the profile-revision rule. Those defects were corrected in
the admitted head and covered by regression tests.

## Shipped Architecture

The model and browser specialist continue to call the same MintClaw-owned
first-party tools. Driver choice and runtime provisioning remain private:

```text
browser specialist
    |
    v
first-party browser tools and broker authority
    |
    v
local provider -> selected private driver -> browser runtime
                       |
                       +-- playwright_library (default)
                       +-- playwright_mcp (disabled rollback path)
```

The default `playwright_library` driver launches one isolated MintClaw-owned
Node.js sidecar per browser session. The sidecar uses the official Playwright
Node.js library pinned to `1.63.0` and exchanges bounded JSON-lines messages
over standard input and output. It is not an MCP server: there is no MCP tool
discovery, MCP protocol, shared service, or model-visible raw Playwright tool.

The sidecar implements the existing typed worker operations for session
lifecycle, observations, tabs and frames, actions, files and retained
artifacts, diagnostics, dialogs, and human handoff. The Go client preserves
cancellation, action acceptance, durable terminal or unknown outcomes,
bounded output, one cleanup owner, and no blind replay. Provider runtime
handles, process arguments, profile paths, browser endpoints, native IDs, and
page secrets remain outside model and ordinary durable payloads.

Gateway and companion use the same provider and driver seams. A persistent
profile still has one writer. Changing a profile between `playwright_mcp` and
`playwright_library`, including removing and later re-adding its driver field,
requires a profile revision change. The MCP implementation remains available
as an explicit rollback path but is not the selected production driver.

## Automated Validation

The admitted implementation added or extended:

- direct sidecar protocol and client tests, including malformed output,
  cancellation, process exit, output bounds, dialog sequencing, and cleanup;
- driver conformance through both direct-library and MCP implementations;
- provider lifecycle tests for creation, attach, partial startup, crash,
  restart, close failure, cleanup, opaque data, and configuration replacement;
- managed-profile revision and single-writer tests;
- gateway and companion configuration, policy, browser-host, and routing tests;
- existing BF1/BF2 actions, contexts, artifacts, diagnostics, and handoff tests;
- real Chromium tests through the pinned library; and
- canonical smoke-runner self-tests that reject missing tool-call evidence,
  extra probe sessions, incomplete cleanup, timeout, and signal failures.

The dependency lock and CI installation path make the Node dependency exact.
Ordinary install or update tooling supplies that dependency; generated
`node_modules` trees are not committed and managed runtime distribution remains
deferred.

## Complete Production Matrix

The first admitted deployment completed all twelve required gateway and Darwin
companion cases on 2026-09-14:

| Target | Profile | Suite | Checks | Execution evidence | Cleanup | Duration |
| --- | --- | --- | ---: | --- | --- | ---: |
| Gateway | `managed` | `core` | 4/4 | Verified | Clean | 89,401 ms |
| Gateway | `managed` | `managed-reuse` | 4/4 | Verified | Clean | 137,513 ms |
| Gateway | `ephemeral` | `ephemeral-cleanup` | 9/9 | Verified | Clean | 120,468 ms |
| Gateway | `managed` | `driver-conformance` | 4/4 | Verified | Clean | 84,386 ms |
| Gateway | `managed` | `playwright-library` | 4/4 | Verified | Clean | 87,387 ms |
| Gateway | `managed` | `provider-lifecycle` | 6/6 | Verified | Clean | 105,452 ms |
| Companion | `managed` | `core` | 4/4 | Verified | Clean | 154,075 ms |
| Companion | `managed` | `managed-reuse` | 4/4 | Verified | Clean | 278,168 ms |
| Companion | `ephemeral` | `ephemeral-cleanup` | 9/9 | Verified | Clean | 247,217 ms |
| Companion | `managed` | `driver-conformance` | 4/4 | Verified | Clean | 169,941 ms |
| Companion | `managed` | `playwright-library` | 4/4 | Verified | Clean | 155,908 ms |
| Companion | `managed` | `provider-lifecycle` | 6/6 | Verified | Clean | 172,484 ms |

Every accepted report had a null safe error, verified the parent delegation
and child browser calls, closed every opened session, stopped its fixture, left
no profile lock or sidecar, and proved immediate reuse.

## Post-Correction Closeout Matrix

The final baseline reran the lifecycle-, isolation-, and driver-specific suites
on both placements after all follow-up corrections:

| Target | Profile | Suite | Checks | Execution evidence | Cleanup | Duration | Report SHA-256 |
| --- | --- | --- | ---: | --- | --- | ---: | --- |
| Gateway | `managed` | `provider-lifecycle` | 6/6 | Verified | Clean | 117,480 ms | `180cddf872242f9e27855117390e0397a00452e76d0ddb60eff7b33a27bd1361` |
| Gateway | `ephemeral` | `ephemeral-cleanup` | 9/9 | Verified | Clean | 123,717 ms | `4b00ea735f77858e6c549c8116eac277a392cbbab0a845abc3e892e50291c577` |
| Gateway | `managed` | `playwright-library` | 4/4 | Verified | Clean | 147,165 ms | `8976d9377a7b3a5c18b37d0f13953f6ad6a50f4457ff388b212c25690142e2ee` |
| Companion | `managed` | `provider-lifecycle` | 6/6 | Verified | Clean | 243,113 ms | `bff581910a67ca007c2da55929e6cdac799ee2a1d806a4d6ba08f8371189f2c3` |
| Companion | `ephemeral` | `ephemeral-cleanup` | 9/9 | Verified | Clean | 344,381 ms | `7b13fddb3921f1ddfb4b131cad9bafd04fd840ab2d637d3a40c09cab260d674a` |
| Companion | `managed` | `playwright-library` | 4/4 | Verified | Clean | 156,693 ms | `ab7653160dfdabf109fce6701072362e3d564fc777db7e88f0fc9f24cf567786` |

The final reports are retained in owner-only evidence directories under the
`p2-18b759c8` label. They contain safe predicates, counts, durations, and
hashes, not page content, credentials, cookies, storage values, endpoints,
native browser IDs, or private driver arguments.

Rejected attempts remained fail closed and were not counted as evidence. One
pre-correction live report could not correlate a valid child trace even though
the browser work completed; the root-turn correction fixed that evidence gap.
One provider-lifecycle prompt created a separate capability-probe session; the
single-session correction made that invalid workflow impossible. One companion
operator invocation supplied a node label where a browser profile alias was
required and was correctly denied by policy. Its later `managed` and
`ephemeral` runs passed.

## Deployment Corrections

Live deployment exposed four general lifecycle or evidence issues rather than
site-specific workarounds:

- launchd companions did not inherit a path containing Node.js and npm, so the
  installed service now receives a deterministic standard runtime path;
- cancellation during a first sidecar close could poison all later cleanup,
  so shutdown is idempotent and retryable with its own bounded cleanup context;
- live result collection assumed that parent and child session hashes were
  identical, so the fallback now requires the same workspace, exact root turn,
  and request start bound; and
- a smoke stage could satisfy capability proof in a second session, so proof
  and the prescribed workflow must now occur within the same delegation and
  browser session.

Each correction has focused regression coverage and passed the final live
matrix. None broadens browser authority, introduces site-specific behavior, or
changes the model-facing browser schema.

## Deployment And Health

The closeout identities are:

| Artifact | Revision or SHA-256 |
| --- | --- |
| Gateway source and final smoke runner | `18b759c8b0f70dd5c2ae27cab08d01f539d290f8` |
| Gateway runtime source | `615c2c2053e0619e639551d495ae8d444f9aee9d` |
| Gateway binary | `24d5001ad5226c90ad80af2a2678d8664a1e6ce9b291232593636e8b08428791` |
| Darwin companion runtime source | `ce218fb5d3711fb4de3ba4a8be946b1438f6a10a` |
| Darwin companion binary | `50a421099189965004955b8ed2267b042a4e60049a3a6ecff97c26f4804ed7fe` |
| Smoke runner | `6cce65d22e066606ca70847cdd61b69e92409f427605e119ce7c9ed2babdf9d0` |
| Direct sidecar | `0c53c6bd5af9fa860c55d20f4633bdaa8dd66e790202d48f1eb0eabc2733bd89` |
| Dependency lock | `b367b9c6d9e1a1012e4d6f7e5af92feb6137e3f5a6e463b888263128b9c66885` |
| Gateway configuration | `dfcfd237542a04e71464b555b9d96260be54b407a664cf75b4ad1bb060bb3314` |
| Darwin companion configuration | `4f646b6dfc4cf650902925de51eddc81e2e77b46df159b32ea2f00f4bb3226d2` |

The later source-only smoke correction did not require rebuilding either
runtime. The gateway main service, web launcher, SSH forwarding path, and
Darwin companion were active after validation. The companion was connected
with its approved catalog. Gateway health reported no product, user, global,
or system failed unit and no error-level entry in the final ten-minute window.
The companion service had not exited. No direct sidecar, MCP worker, fixture,
nonterminal session, prepared invocation, or profile lock remained after the
matrix.

## Rollback And Retention

The checksum-verified immediate pre-closeout recovery sets are:

```text
/home/server/mintclaw-recovery-browser-phase2-live-evidence-pre-20260914T193845Z
/home/server/mintclaw-recovery-browser-phase2-final-pre-20260914T185124Z
/Users/ab/mintclaw-recovery-browser-phase2-final-companion-pre-20260914T185124Z
```

Earlier phase and correction recovery sets remain retained. The gateway set
contains the binary, configuration, launcher, service definitions, source
identity, version, service map, and service state. Rollback must restore a
matching binary and configuration only after stopping the affected service and
must not overwrite newer mutable browser, invocation, or profile state without
a forward recovery review.

The supported first rollback is configuration-only: increment the affected
profile revision while selecting `playwright_mcp`, then redeploy and rerun the
canonical smoke matrix. Silent driver fallback is forbidden.

## Phase Conclusion

Every Phase 2 acceptance criterion is satisfied. The direct official
Playwright-library driver implements the currently advertised first-party
browser contract; gateway and companion pass conformance and real managed and
ephemeral canaries; lifecycle, cancellation, bounds, approval, freshness,
durable outcome, privacy, and cleanup contracts remain intact; driver changes
are revision-bound; and the direct driver is the selected production default
with MCP retained only as an explicit rollback path. Phase 3 may now add the
separately configured, bounded `browser_execute` capability.
