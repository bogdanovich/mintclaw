# Local Coding Agent P7.5 Exit Record

Roadmap packet: [P7.5 — Coding-session access to paired companions](local-coding-agent-roadmap.md#p75--coding-session-access-to-paired-companions).

Status: completed and production-qualified on 2026-10-03.

P7.5 gives an explicitly granted local coding thread two separate ways to use
a paired companion:

- `remote_capability` performs one bounded, typed operation through the live
  gateway and companion; and
- `remote_coding_task` links the local thread to one existing P7.4/P7.7 native
  remote coding task when the remote machine must own repository reasoning,
  edits, publication, or machine work.

The local thread never receives a remote current working directory, raw node
command, credential, channel history, or second writer for the remote
transcript. Remote access remains absent by default.

## Merged implementation

| Packet | Evidence | Result |
| --- | --- | --- |
| Admission | [#1386](https://github.com/bogdanovich/mintclaw/pull/1386), merge `dc3df949` | Froze same-user IPC, exact grants, capability/task ownership, recovery, rollout, rollback, and stop boundaries. |
| Broker foundation | [#1391](https://github.com/bogdanovich/mintclaw/pull/1391), merge `7f52994e` | Added the authenticated local broker, bounded discovery, exact authority intersection, and deny-by-default configuration. |
| Direct workspace capabilities | [#1416](https://github.com/bogdanovich/mintclaw/pull/1416), merge `f566b769` | Added typed remote read, search, write, patch, and direct-argv execution without a generic RPC or shell surface. |
| Durable jobs | [#1419](https://github.com/bogdanovich/mintclaw/pull/1419), merge `ad1e16b6` | Added invocation-owned job status, logs, cancellation, and bounded output. |
| Artifact import | [#1422](https://github.com/bogdanovich/mintclaw/pull/1422), merge `619f90a2` | Added owner-bound artifact description and ranged, digest-verified import into the coding attachment store. |
| Service adapters | [#1424](https://github.com/bogdanovich/mintclaw/pull/1424), merge `3e7c5acd` | Added closed typed service status, log, and action adapters with exact approval policy. |
| Browser adapters | [#1426](https://github.com/bogdanovich/mintclaw/pull/1426), merge `1ab16151` | Added bounded browser-profile discovery, lifecycle, observation, ordinary actions, artifacts, and handoff projection. |
| Task-scope discovery | [#1428](https://github.com/bogdanovich/mintclaw/pull/1428), merge `32dc3d81` | Projected only exact remote coding scopes backed by the complete approved coding catalogue. |
| Shared task coordinator | [#1430](https://github.com/bogdanovich/mintclaw/pull/1430), merge `96f0af06` | Reused the P7.4/P7.7 registry, node invoker, question, and terminal-result paths for local task ownership. |
| Local task links | [#1433](https://github.com/bogdanovich/mintclaw/pull/1433), merge `f435f80e` | Added `remote_coding_task`, canonical task links, compaction/restart reconstruction, fork disclosure, and shared frontend observations. |
| Discovery latency repair | [#1448](https://github.com/bogdanovich/mintclaw/pull/1448), merge `40afb816` | Bounded production discovery latency without weakening freshness checks. |
| Catalogue-hash repair | [#1451](https://github.com/bogdanovich/mintclaw/pull/1451), merge `3cc6f4eb` | Reused the already validated catalogue digest instead of repeating a slow companion round trip. |
| Write-schema guidance | [#1453](https://github.com/bogdanovich/mintclaw/pull/1453), merge `0ceb74b5` | Made create-versus-replace semantics explicit and retained `overwrite=false` as the safe creation default. |
| Trace settlement | [#1454](https://github.com/bogdanovich/mintclaw/pull/1454), merge `07981e4c` | Drained passive diagnostic traces cleanly at coding-runtime shutdown. |
| Reconnect progress repair | [#1463](https://github.com/bogdanovich/mintclaw/pull/1463), merge `d8a682a9` | Refreshed equal-revision nonterminal progress after reconnect while preserving locally terminal task state. |

The private production configuration was merged separately:

- [mintclaw-workspaces #38](https://github.com/bogdanovich/mintclaw-workspaces/pull/38),
  merge `5d0463d5`, persisted the exact local remote-task grant; and
- [mintclaw-workspaces #39](https://github.com/bogdanovich/mintclaw-workspaces/pull/39),
  merge `8c6e3459`, refreshed the admitted remote scope revision.

Those clean workspace PRs followed the operator policy of merging immediately
after their checks; they did not require code review. Core implementation PRs
passed their required formatting, lint, security, focused, race, portability,
CI, exact-head review, and owner-approval gates. Documentation-only core PRs
used the documented review-free fast path.

## Closed ownership and authority model

The production route keeps one owner for each state domain:

```text
local coding thread
  -> same-user Unix broker socket and exact local grant
  -> live gateway authority intersection and invocation store
  -> authenticated paired-node transport and companion ledger
  -> typed remote workspace operation

local coding thread
  -> same broker and grant
  -> shared P7.4/P7.7 task coordinator
  -> one native remote coding thread, worker generation, and execution root
```

The coding thread owns only its canonical local tool result and bounded link.
The gateway owns grant resolution, durable invocation/task projection, and
recovery. The companion owns the accepted invocation ledger and resolves
node-local paths, commands, and profiles. The remote coding thread remains the
only remote transcript writer.

The production grant is `local-development` at revision
`local-development-p7-5-v1`. Its discovery snapshot exposes only:

- capability `ab-canary-workspace`, revision
  `ab-canary-workspace-p7-5-v1`, target `ab-workspace-target`; and
- task scope `mintclaw-dev`, revision
  `2242afebfb3701943dfd9c3a3ee786712746af3493d1d4bf6a7ad76fece25e58`,
  target `ab-2`, profiles `investigate` and `mutate`.

The observed production discovery revision was
`discovery_7836685699aece8c749dad48e9fde5a3c19119c191190ab4bffa86d9224e8b44`.
The broker socket is an owner-only Unix socket with mode `0600`; the containing
runtime directory is owner controlled. No root profile, privileged helper,
owner shell, per-call approval bypass, arbitrary internal method, or generic
node command is projected through P7.5.

## Production deployment

The final gateway and macOS companion both run exact main
`57681e9498c318a1951e92c5d247c6aaa98dd4fe`. That revision contains the
reconnect repair merge `d8a682a9`. The server source checkout remained clean,
and the companion LaunchAgent restarted from PID `55208` to PID `11593` with
exit status zero and the expected version.

Before the gateway rollout, the operator created
`/home/server/mintclaw-p7-5-reconnect-backup-20261004TPJakcU`. It contains the
core, node, and launcher binaries, user systemd units and drop-ins, active main
configuration and run script, and service-state receipts. `SHA256SUMS`
verifies successfully; its digest is
`3525af1bc042eb1d9153bd7e5d488fa539a75e6267e62e41b8fff3d34275b30b`.

Before final companion alignment, the operator created
`/Users/ab/.mintclaw-node/local-test/backups/p7-5-final-align-20261003T2330.x5ZQpv`.
It contains the previous node binary, configuration, LaunchAgent, and
invocation ledger. Its manifest verifies successfully with digest
`da09c5b2683c82bd97d3a9fa1ec389b68908541ddd349bc82fb1fce8108ad97b`.

## Production canaries

### Direct bounded mutation and read

Owner coding thread `2d2fbc21-22a3-420f-bc79-886784a5bc35` created exactly
one canary file through `ab-canary-workspace.write_file` with
`overwrite=false`:

- relative path `p7-5-direct-capability-canary-20261003T2316.md`;
- gateway invocation
  `remote_capability_836821a76f731db6287c5edb8ac515c3ebd33a51cafc3fad2a2b90a200a14b43`;
- companion invocation
  `inv_f85fb8ff085c77c9957bc8050a6abdde9deca970bab0ff21dd3d47c3566737e0`;
- one `create` changed-path receipt, size 79 bytes; and
- SHA-256
  `6ca03c0864d689e53805be7c7fcc017b8db7aed77b5433f27ca7272c9ac706ef`.

A separate `read_file` invocation
`remote_capability_cb7c37d22fcc63548f3363e8bb2f0df2c8bfa666bc4f52d8b94b01314060f4e0`
mapped to companion invocation
`inv_c9ac9d05b8c016458582742a6eb5ff3477d18dd47920cd89be3b5b2abd0f1303`
and returned the exact two lines, size, and digest without truncation. After a
companion restart, the ledger still contained exactly one write and one read;
no reconnect replay occurred.

### Durable job and artifact ownership

The same thread started one short `echo` job through invocation
`remote_capability_639b6d558ec918427f9517c88ab54c2b7b4b8f38f6c1e94aac5d6f28febc6cec`.
Status reported exit code 0, 21 stdout bytes, no stderr, no truncation, and one
artifact. `job_artifacts` returned only owned reference
`jobart_6f01493151142ad8af98820876685acc`. `artifact_fetch` imported exactly 79
bytes into attachment
`media://coding-attachment/e3554287-6aa3-4cca-a5f7-13c7d3555aa8` with the
same SHA-256 as the direct read. No raw companion path was exposed.

### Remote task question, reconnect, and no replay

The thread started task
`coding-aa6bd3adf9ef9702f5eee760b754df7efadde82b4bf5ba78b342bac28f62cfd8`
against `mintclaw-dev`/`investigate`. It mapped to:

- task generation `5cb1b83d-dc00-4df5-ad96-a937002eeda9`;
- remote thread `d12d5f62-8e15-44c0-a39d-dd5d31e456fe`;
- worker generation `worker-a3238714-35fe-4e27-8a26-f992d85ade31`; and
- start invocation
  `coding_inv_eaa596548f82ebfa59bb5006662e8a51036bf0545ae3e57f5e8da2b3b9aa0985`.

The worker created blocking question
`interaction_292e829fb1e5e25a75e287e123896de7` at revision 3. Before restart,
status reported `waiting_for_input` and
`coding task is waiting for correlated user input`. The gateway was restarted
once. A status call from the same owner thread returned the same task,
generation, remote thread, worker, question, and exact progress instead of the
old transient `node unavailable` message.

Answer `Alpha` resumed that same worker. It read only `AGENTS.md`, reported
heading `# Local Working Notes`, commit
`a70bc520eb23ce71835a2c8ca0701ec35b7e1669`, and a clean source checkout,
then reached `completed`. The companion ledger contains exactly one
`coding.task.start.v5` for this task.

Earlier qualification tasks exercised the remaining control surface through
the same coordinator:

- `coding-07e4e9f8083eb98566b38c7cec32372b8d0d817f28b2df5182fa3221ae60dc8c`
  reached `succeeded` after status and correlated answer;
- `coding-e0c1098796a8b750a26f716d23699980e653d5c91cf18dbf6857e127e991e9ba`
  reached `cancelled`; and
- `coding-83e87d9ca0adb2476244faeef4d58983f60f724ddc0a9ef41232665ec8a3ecaa`
  accepted ordered steering and reached `succeeded`.

The older reconnect probe
`coding-4e6b66c0e1994af05bc3238b6b4441bc82e79814a4de699dc01b0fd4983c848e`
had already reached retained `cancelled` state before the repair deployment.
Post-deploy status observed that terminal state in 31 ms and did not cause the
cancel or dispatch a second start. Its historical interaction had aged out of
the bounded interaction registry, so no stronger timeout-cause claim is made.

### No-grant and compatibility

A temporary owner-only configuration copy set only
`coding.remote.enabled=false`; the active production configuration was not
modified. Fresh local thread `d0242b2d-4853-4952-97ca-c2ef9e5d7721`
completed normally with response `P7_5_LOCAL_NO_GRANT_OK`. Its canonical
transcript contains zero tool calls and zero `remote_capability` or
`remote_coding_task` references. The temporary configuration was removed and
the source checkout stayed clean.

P7.5 does not replace or synthesize the Telegram owner plane. Existing P7.4
Telegram investigation, question/answer, cancellation, and isolated mutation
evidence remains in the [P7.4 exit record](local-coding-agent-p7-4-exit.md).
Existing project-yolo, machine-yolo, and macOS root-denial evidence remains in
the [P7.7 exit record](local-coding-agent-p7-7-exit.md). P7.5 reuses their
coordinator and node protocol while keeping channel delivery and requester
ownership unchanged. Focused compatibility suites passed for those paths.

After the final rollout, live gateway session
`live-smoke-2edfc3d9-2d3f-4b7a-8ff3-c28e57fe1260` returned
`P7_5_GATEWAY_HEALTH_OK` with no tool call. This verifies the ordinary personal
agent pipeline independently of the local coding canaries.

## Validation and health

The implementation sequence passed focused config, broker, gateway, tool,
agent, coding runtime, TUI, transcript, compaction, node companion, real
process, race, Linux, macOS, and Windows compile-only gates. The final reconnect
repair additionally passed:

- `make fmt` and changed-file lint;
- the complete `TestRemoteCoding` suite;
- race repetitions of the remote-coding suite;
- the full `pkg/agent` suite with `goolm,stdjson`; and
- Linux and Windows compile-only checks.

The production gateway and companion were aligned to exact main after those
checks. Final health reported all ten expected services active, zero product
or global failed units, zero legacy processes, and zero error-level entries in
the ten-minute observation window. The owner-only broker socket remained mode
`0600`.

Passive trace `trace-turn-080895ca3381dfdd10a95065` used schema
`mintclaw.diagnostic_trace.v1`, completed with eight records, used
`redacted_content` with `mintclaw.config_filter.v1`, and reported no
truncation. Its one fallback-attempt record was a successful provider attempt,
not an error. Gateway journals, the companion log, and the trace contained no
raw direct-canary content or filename.

## Rollback

Rollback is disable-first and preserves durable evidence:

1. Disable or remove the `local-development` local-client grant so no new P7.5
   operation can be admitted.
2. Restart only `mintclaw-main.service`; verify the broker socket is absent and
   ordinary local coding, Telegram, and paired-node behavior remain healthy.
3. Observe or cancel already accepted invocation and task IDs through their
   retained gateway/node authorities. Never replay a mutation or delete state
   to manufacture a clean rollback.
4. If binary restoration is required, restore the server binaries and units
   from the verified server backup and the macOS node binary/config/LaunchAgent
   from the verified companion backup, then restart only the affected service.
5. Re-run service health, broker permission, node availability, no-replay, and
   redaction checks before re-enabling the exact grant.

## Residual limits and completion decision

P7.5 intentionally does not provide a network-accessible coding broker, a
remote filesystem mount, a sticky remote cwd, a generic RPC/exec surface,
privileged direct capabilities, copied remote transcripts, automatic
publication, or P7.6 filesystem rewind. A coding task can publish or perform
machine work only through a separately granted P7.7 profile and an objective
that requests it.

All admitted R1-R5 boundaries, production canaries, health checks, backups,
rollback evidence, and stop conditions are satisfied. P7.5 closes with this
record; broader remote authority requires a new focused admission.
