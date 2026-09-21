# Local Coding Agent P7.4 Exit Record

Roadmap packet: [P7.4 — Channel-to-coding task handoff](local-coding-agent-roadmap.md#p74--channel-to-coding-task-handoff).

Status: complete on 2026-09-20.

The merge containing this record closes P7.4. An authorized Telegram user can
now create a durable remote coding task against an operator-approved project
alias, receive its task ID immediately, answer a worker question, inspect or
steer the task, cancel it, and receive one bounded final result. Investigation
stays read-only. Mutation runs in one retained linked worktree owned by the
paired development machine; the gateway never receives a repository path or
touches the checkout.

The production canaries exercised the native MintClaw worker. Codex and ACPX
remain optional adapters and were not used as the remote execution contract.

## Merged implementation

| Packet | Evidence | Result |
| --- | --- | --- |
| Admission | [#1174](https://github.com/bogdanovich/mintclaw/pull/1174), merge `f4e0b130` | Froze authority, identity, lifecycle, rollout, evidence, and stop boundaries. |
| P7.4a task domain and node host | [#1208](https://github.com/bogdanovich/mintclaw/pull/1208), merge `ef8f4108` | Added the deny-by-default project catalogue and idempotent node-local host that composes the existing thread, worker, worktree, and companion ledger owners. |
| P7.4b paired-node commands | [#1221](https://github.com/bogdanovich/mintclaw/pull/1221), merge `b6835415` | Added versioned project discovery and task start, status, steer, and cancel commands over the existing authenticated WSS transport. |
| P7.4c gateway handoff | [#1227](https://github.com/bogdanovich/mintclaw/pull/1227), merge `387fa8d2` | Added the owner-scoped `coding_task` tool and projected existing task, interaction, invocation, and delivery state without a second coding-task store. |
| P7.4d interaction and portable proof | [#1235](https://github.com/bogdanovich/mintclaw/pull/1235), merge `e918ac12` | Completed durable question/answer handoff, bounded results, Linux/macOS real-process coverage, and the operations runbook. |
| Production start-time repair | [#1254](https://github.com/bogdanovich/mintclaw/pull/1254), merge `bb4b1cf1` | Gave isolated-worktree preparation its admitted 60-second start budget while retaining short control-operation timeouts. |
| Trusted worker-scope repair | [#1279](https://github.com/bogdanovich/mintclaw/pull/1279), merge `147ec5c3` | Made the supervisor-selected project/worktree the worker's explicit execution scope and kept outer orchestration criteria out of the worker objective. |

The implementation PRs passed their required repository checks, exact-head
automated review, and owner merge approval. The admission PR used the
repository's docs-only path. The portable proof runs the actual gateway,
authenticated WSS, companion, private worker, native coding thread, isolated
worktree, durable interaction, and channel-delivery boundaries on Linux and
macOS; it is not a mocked transport proof.

## Closed ownership and trust model

The production path has one owner at each boundary:

```text
Telegram requester
  -> gateway durable task, interaction, invocation, and delivery
  -> authenticated paired-node command
  -> node-local project grant and invocation ledger
  -> one private MintClaw worker and native CodingThread
  -> source checkout for investigate, or one owned worktree for mutate
```

The gateway admits only an exact requester, safe target alias, safe project
alias, descriptor revision, and allowed mode. The companion resolves the
operator-configured canonical repository and worker executable locally. A
model cannot supply a path, repository URL, credential, executable, provider,
worktree parent, branch prefix, retention policy, or cleanup policy.

One accepted task generation maps to one invocation, thread, worker
generation, and, for mutation, worktree/branch owner. The gateway task registry
remains the requester-facing lifecycle and delivery source of truth. The
companion ledger owns accepted remote invocation recovery. The native coding
thread owns transcript and compaction. P7.3 allocation and handoff records own
mutation isolation. No second task, question, transcript, or repository state
store was added.

## Production rollout

Rollout remained deny-by-default until the gateway binary, local node, worker,
project descriptor, pairing approval, and exact requester grant were aligned.
The canary grant exposed only:

- gateway project alias `mintclaw-dev`;
- target alias `ab-2` and node-local project alias `mintclaw`;
- `investigate` and `mutate` modes; and
- descriptor revision
  `80565fd1f15fc7c98be728ad310d20f844213839cb6d53c87a18b1bc27766c39`.

The final worker and admitted source checkout were pinned to merge
`147ec5c344bf4ab78010878ea856c8beb1e10f26`. During the final canary, the
gateway ran `45077fda`; its latest production-code ancestor and the active
node were `f9703ca3`, with only the later documentation merge between them.
The protocol remained compatible, and the descriptor continued to bind the
exact worker and repository authority used by the task.

## Telegram canary evidence

### Investigation and interaction

Task
`coding-1049443416a202ac61b3d293cacdc888de3fa69ff6f24f1aa11492fa2e638e95`
created native thread `547fc8b1-60d9-4ae6-ab6d-dacf21a0f204`. The gateway
returned the durable task ID before completion. The remote worker then created
the blocking two-choice question, accepted the correlated Telegram answer,
read only the selected file, and returned its first Markdown heading and Git
commit. The source checkout remained unchanged.

### Isolated mutation

Task
`coding-a586447a7c981181e42cc2bea91d23b26cb6f41e968661252e4fc9299c396e48`
created exactly one task generation, worker generation, native thread
`b9ecb9d0-23ba-4f9e-96e9-5386fab23124`, worktree
`wt-b9ecb9d023ba4f9e96e95386fab23124`, and branch
`mintclaw/b9ecb9d023ba4f9e96e95386fab23124` from `147ec5c3`.

The worker created only
`docs/operations/p7-4-production-mutate-canary.md`, with the requested heading
and result line. Independent post-task inspection proved:

- `git diff --check` exited successfully;
- `git status --short` contained only that untracked file;
- the handoff classified one untracked change and retained the worktree;
- the source checkout stayed clean at `147ec5c3`;
- the transcript contained one `apply_patch` and one validation `exec`, with no
  nested coding task;
- the node ledger reached `completed` for the same task, thread, generation,
  worktree, and branch; and
- the gateway recorded one transition to `delivered`, one completion identity,
  and no second task created after the canary request.

The bounded diagnostic trace
`trace-turn-68893887d90bd89af49532de` completed with schema
`mintclaw.diagnostic_trace.v1`, eight records, redacted content, the configured
redactor, and no truncation.

### Safe failures that informed rollout

Production qualification deliberately retained failed attempts instead of
deleting their evidence or silently replaying them:

- `coding-1e3db7fb73068d2caa2a66ab57da49e5e4cc01a6ce04a481a86eabaae2521fc0`
  stopped before worker launch when the old 30-second node
  start budget expired. It created no repository change; #1254 repaired the
  admitted start budget.
- `coding-f3e69b003bd4bcc2cf451b93531e2d992da838acb457524ce896fce71e2f20f6`
  launched a worker, but outer orchestration criteria in
  the objective caused the worker to decline the repository mutation. It made
  no change and exposed that process completion alone was insufficient
  semantic evidence.
- `coding-ab6cb160412f27d80fbd00e98a5cc491a9f33cd53ba29313907f2b5eb3c6560b`
  reported an explicit uncertain launch while gateway,
  node, worker, source, and grant revisions were being aligned. Its allocated
  worktree stayed clean and retained; no blind retry or second writer ran.
- #1279 separated gateway orchestration from the worker objective and made the
  already-selected execution scope explicit. The final canary then completed
  without expanding authority.

These attempts demonstrate the intended failure direction: uncertainty is
visible, state is retained, and recovery requires inspection or an explicit
new task rather than automatic replay.

## Health, backup, and rollback

After the final canary, all ten MintClaw user services were active, no product
or global unit was failed, no legacy process was present, and the node
LaunchAgent remained running with an established gateway connection. The
launcher and reviewer probes returned their expected HTTP responses. The main
gateway journal since task start contained no warning-or-higher entry. The
source checkout was still clean. The post-deploy doctor found no configuration
or load error (`error=0`); its non-zero result contained only the existing
policy posture findings (`fail=14`, `warning=8`, `info=7`) for plaintext
credential storage and explicitly admitted write/approval-bypass authority,
not a P7.4 runtime failure.

Retained rollback evidence includes:

- gateway config, launcher, binary, units, and doctor output in
  `/home/server/mintclaw-p7-4-context-deploy-20260920T210737Z`;
- the earlier grant rollout snapshot in
  `/home/server/mintclaw-p7-4-grant-20260920T203042Z`; and
- local node config, launch definition, and prior binaries under
  `~/.mintclaw-node/local-test/backups/p7-4e-runtime-20260920T203042Z`.

Rollback first removes the gateway `remote_coding_projects` grant so no new
task is admitted. It then restores the matched gateway/node/worker artifacts
or renews pairing without the five coding commands, restarts the companion and
gateway, and verifies health and absence of replay. Retained tasks, threads,
ledgers, worktrees, and handoffs must not be deleted to manufacture a clean
state.

## Done-criteria matrix

| P7.4 criterion | Evidence |
| --- | --- |
| Authorized chat handoff and immediate task identity | #1227 persists the task and idempotency identity before dispatch; both production canaries returned a durable task ID immediately. |
| Read-only investigation and isolated mutation | #1208 and #1235 enforce mode-specific tools; the live investigation left source clean and the mutation changed one file only in its owned worktree. |
| Questions, steering, cancellation, and bounded result | #1235's Linux/macOS vertical proof covers all control paths; the live investigation additionally exercised a correlated Telegram question and answer. |
| One task, thread, worker, worktree, and delivery | Node ledger, allocation, handoff, transcript, gateway registry, and delivery events agree on the final canary identities; no second task or delivery exists. |
| Restart, disconnect, uncertainty, and no replay | #1208, #1221, #1227, and #1235 cover durable reconciliation and races; retained production failures stopped explicitly without blind replay. |
| Deny-by-default path and credential authority | The exact descriptor revision and requester grant were required; the gateway saw aliases and bounded results, never the configured repository path or credentials. |
| Linux/macOS and existing coding compatibility | #1235 runs real processes on both platforms and preserves local `code`, `resume`, P8a/P5a node features, browser commands, and optional ACPX/Codex adapters. |
| Production health and rollback | Final service, connection, trace, journal, checkout, backup, and disable-first rollback evidence is recorded above. |

## Next boundary

P7.4 stops here. It does not let a local coding session invoke paired companion
capabilities, publish or merge Git changes, accept arbitrary projects or
paths, add a generic remote agent RPC, bootstrap remote machines, or remove
Codex/ACPX compatibility.

The next optional roadmap packet is P7.5, which may expose fresh, bounded
paired-node capabilities to an existing local coding session. It must reuse
the live gateway's node connection and typed command policies; it must not
create a competing companion connection or a model-to-model relay.
