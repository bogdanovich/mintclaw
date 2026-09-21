# Remote Coding Tasks

Remote coding tasks let an authorized chat user delegate repository or
machine work to a paired Linux or macOS development machine. The gateway
remains the live agent and channel owner. The companion remains a thin
executor: it does not run a second Telegram bot, gateway, agent router, or
general-purpose remote agent.

## Architecture

```text
Telegram
   |
   v
MintClaw gateway and live agent
   |  durable task, interaction, delivery, target policy
   v
authenticated WSS invocation
   |
   v
mintclaw-node on the development machine
   |  scope catalogue, invocation ledger, task host
   v
mintclaw _worker
   |  native CodingThread and optional isolated worktree
   v
operator-approved project or machine scope
```

Install both `mintclaw` and `mintclaw-node` from the same release on the
development machine. `mintclaw-node` is the only always-running companion
process. It starts `mintclaw _worker` as a private child for an accepted task;
the internal worker command is not a public transport or daemon.

The gateway owns the requester-facing task, question, progress, and final
delivery. The companion owns accepted invocation recovery and the node-local
scope grant. The native coding store owns the transcript and compaction. An
investigation runs against the validated source checkout with read-only tools.
A mutation creates one linked worktree and returns a retained branch and
handoff; it does not modify the source checkout or publish a pull request.
`project-yolo` uses the same isolation but may commit, push, open or update a
pull request, release, and deploy when the objective explicitly requests it.
Its final report contains bounded node-observed external-effect receipts.
`machine-yolo` instead starts in one configured directory without requiring
Git and runs with the full authority of the companion service account. It may
create repositories, install user-level packages, use the network, and manage
user processes or services. It has no automatic rollback, and grants neither
root nor sudo authority.

## Incompatible v4 cutover

The scope migration is intentionally not a rolling compatibility upgrade.
Version 4 rejects v3 node commands, task projections, companion task records,
and worker protocol. Do not deploy the v4 gateway or companion against
retained v3 coding task state.

The production rollout must first settle or cancel every coding task, stop the
gateway and companion, and back up both the gateway workspace state and the
companion `state_dir`. It must then install matching v4 binaries, replace both
configuration halves atomically, approve the five v4 commands, and initialize
fresh invocation and task state before admission is enabled. Retained thread
and worktree directories stay archived for inspection; they are not evidence
that a v3 task can be resumed through the v4 transport. The P7.7 rollout phase
owns the exact backup paths, rollback commands, and production canary record.

## Companion configuration

Create every configured root, MintClaw state directory, and required
worktree-parent directory before loading the configuration. They must be
canonical direct directories rather than symlinks. Each root must be directly
beneath its `source_parent`; a project worktree parent must not overlap the
source checkout or MintClaw coding state.

The following example admits read-only investigations, isolated mutations,
explicit isolated publication/deployment, and a separate companion-user
machine scope. Paths and the model are examples and must be replaced locally.

```json
{
  "gateway_url": "wss://mintclaw.example.com/nodes/v1/ws",
  "state_dir": "/Users/operator/.mintclaw-node",
  "policy": {
    "revision": "remote-coding-v1",
    "allowed_commands": [
      "coding.scopes.v4",
      "coding.task.start.v4",
      "coding.task.status.v4",
      "coding.task.steer.v4",
      "coding.task.cancel.v4"
    ],
    "maximum_risk": "write",
    "max_timeout_seconds": 60,
    "max_output_bytes": 262144
  },
  "coding_scopes": {
    "mintclaw": {
      "revision": "operator-v1",
      "kind": "git_project",
      "source_parent": "/Users/operator/devel",
      "root": "/Users/operator/devel/mintclaw",
      "allowed_profiles": ["investigate", "mutate", "project-yolo"],
      "worker_executable": "/usr/local/bin/mintclaw",
      "worker_protocol_version": 4,
      "mintclaw_home": "/Users/operator/.mintclaw",
      "credential_source": "native",
      "provider_profile": "default",
      "model": "gpt-5.6-sol",
      "provider": "openai",
      "worktree_parent": "/Users/operator/devel/mintclaw-worktrees",
      "branch_prefix": "mintclaw",
      "max_concurrent_tasks": 1,
      "task_timeout_seconds": 3600,
      "retention_seconds": 604800,
      "cleanup_policy": "retain"
    },
    "operator-machine": {
      "revision": "operator-machine-v1",
      "kind": "machine",
      "source_parent": "/Users/operator",
      "root": "/Users/operator/automation",
      "allowed_profiles": ["machine-yolo"],
      "worker_executable": "/usr/local/bin/mintclaw",
      "worker_protocol_version": 4,
      "mintclaw_home": "/Users/operator/.mintclaw",
      "credential_source": "native",
      "provider_profile": "default",
      "model": "gpt-5.6-sol",
      "provider": "openai",
      "max_concurrent_tasks": 1,
      "task_timeout_seconds": 3600,
      "retention_seconds": 604800,
      "cleanup_policy": "retain"
    }
  }
}
```

Keep `policy.max_timeout_seconds` at 60 or higher when remote coding is
enabled. The internal start command may use up to 60 seconds to create and
validate an isolated worktree; status, steering, and cancellation retain the
shorter 30-second control timeout.

This slice accepts `kind: "git_project"` with `investigate`, `mutate`, or
`project-yolo`, and `kind: "machine"` with `machine-yolo`. It requires
`provider_profile: "default"`, `credential_source: "native"`,
`worker_protocol_version: 4`, and `cleanup_policy: "retain"`. Omitted resource
bounds receive conservative defaults. Only a scope with `mutate` or
`project-yolo` may configure `worktree_parent` and `branch_prefix`; a machine
scope must omit both. `machine-yolo-root` remains unadmitted.

Authenticate the selected provider in the configured `mintclaw_home` before a
task is accepted. Credentials stay on the development machine and are never
read or forwarded by the gateway. For OpenAI device authentication, for
example:

```bash
MINTCLAW_HOME=/Users/operator/.mintclaw \
  mintclaw auth login --provider openai --device-code
```

Validate the full local policy and print its safe descriptor:

```bash
mintclaw-node coding-scopes --config ~/.mintclaw-node/config.json
```

The output contains aliases, scope kinds, allowed profiles, effective limits,
and a generated SHA-256 `revision`; it never contains paths, executables,
providers, models, or credentials. Copy this generated revision into the
gateway grant. It changes when effective scope authority, repository identity,
worker build, or relevant policy changes. Run the command and update the
gateway grant after a binary upgrade, source revision change, or policy edit.
The human-readable `operator-v1` value is an input to the hash, not the gateway revision.

## Gateway grant

Bind a safe target alias to the paired node, then map a model-visible scope
alias to the node-local scope and generated descriptor revision. Requesters
are exact `(agent, channel, sender)` grants; empty lists and wildcards grant
nothing. Telegram sender IDs are decimal Telegram user IDs represented as
strings.

```json
{
  "nodes": {"enabled": true},
  "execution": {
    "targets": {
      "dev-mac": {
        "type": "node",
        "node": "operator-mac"
      }
    },
    "remote_coding_scopes": {
      "mintclaw-dev": {
        "target": "dev-mac",
        "scope": "mintclaw",
        "revision": "COPY_GENERATED_SHA256_REVISION_HERE",
        "profiles": ["investigate", "mutate", "project-yolo"],
        "requesters": [
          {
            "agent": "main",
            "channel": "telegram",
            "sender": "123456789"
          }
        ]
      },
      "operator-machine": {
        "target": "dev-mac",
        "scope": "operator-machine",
        "revision": "COPY_MACHINE_SCOPE_SHA256_REVISION_HERE",
        "profiles": ["machine-yolo"],
        "requesters": [
          {
            "agent": "main",
            "channel": "telegram",
            "sender": "123456789"
          }
        ]
      }
    }
  },
  "agents": {
    "defaults": {
      "target_policy": {"allowed_targets": ["dev-mac"]}
    }
  }
}
```

An agent-specific target policy replaces the defaults policy. The effective
grant is the intersection of this gateway entry, the exact requester, target
policy, pairing approval, live node catalogue, node-local scope policy, and
requested profile. None of these layers falls back to a local gateway path.

## Pairing and activation

Start the companion with its scope configuration while the gateway grant is
still absent. A new or changed command catalogue remains unusable until the
gateway operator explicitly approves it:

```bash
mintclaw nodes list --state pending_pairing
mintclaw nodes describe node_<fingerprint>
mintclaw nodes approve node_<fingerprint> \
  --alias operator-mac \
  --display-name "Operator Mac" \
  --allow-command coding.scopes.v4 \
  --allow-command coding.task.start.v4 \
  --allow-command coding.task.status.v4 \
  --allow-command coding.task.steer.v4 \
  --allow-command coding.task.cancel.v4
```

Only after the exact node is paired should the operator add the gateway
`remote_coding_scopes` entry and restart or safely reload the gateway. This
order keeps rollout deny-by-default.

From an allowed Telegram account, ask the live agent to investigate the
configured alias. The `coding_task` model tool returns a durable task ID
immediately. The gateway monitors it in the background, presents a worker
question through the existing Telegram interaction controls, forwards an
exact correlated answer, and sends one deduplicated final report. A later
message can ask for status, steer the same task, or cancel it by task ID.

## State and recovery

State is deliberately split by authority rather than copied into one large
database:

- the gateway workspace owns task, interaction, invocation, and outbound
  delivery records;
- the companion `state_dir` owns device identity and its accepted invocation
  ledger, including the bounded coding-task binding;
- `mintclaw_home/coding/threads/<thread-id>` owns the native thread, one JSONL
  transcript, and one Seahorse SQLite database for that thread; and
- the configured worktree parent and MintClaw worktree records own mutation
  isolation and retained handoff evidence.

For `project-yolo`, Git and provider credentials remain node-local. A verified
receipt means the runtime observed a successful effect command or changed Git
HEAD; it is evidence rather than an exactly-once guarantee. A canceled,
timed-out, or incomplete external command is reported as `uncertain`, and the
worker must inspect local and remote state before retrying.

For `machine-yolo`, the configured directory is an initial working location,
not a sandbox. Shell commands may use every path, network endpoint, process,
and user service available to the companion account. Completion, failure, and
cancellation do not imply rollback. Package, process, service, repository, and
publication receipts are bounded evidence; an interrupted effect remains
uncertain until the same worker inspects machine or remote state.

A WSS disconnect does not authorize another start. The gateway reconciles the
original invocation and task identities. A companion crash never relaunches
an ambiguous prompt; retained state reports interrupted or uncertain work. A
released thread remains visible to the normal local `mintclaw resume`
catalogue, while an active writer lease prevents a second writer.

Expected failures are explicit:

- `offline`: the target is not connected; retry status after connectivity is
  restored, not start with a different identity;
- `stale`: the gateway revision differs from the node descriptor; rerun
  `coding-scopes`, review the change, and update the operator grant;
- `busy`: the scope concurrency limit is reached;
- `uncertain`: acceptance or an effect could not be proven; inspect retained
  status and the local checkout before giving new mutation instructions; and
- `forbidden` or `unavailable`: at least one exact authorization layer is
  absent; do not work around it with a model-authored path or shell command.

## Rollback

Disable admission first by removing the gateway
`execution.remote_coding_scopes` entry. This prevents new tasks without
claiming that accepted work vanished. Inspect or cancel retained task IDs,
threads, and worktrees with the still-compatible binaries. Then renew pairing
without the five coding commands or remove them from node-local policy and
restart the companion.

Do not delete a retained worktree, thread, gateway task, or companion ledger
to force a clean state. The first slice uses `cleanup_policy: "retain"` so
dirty, committed, conflicted, replaced, uncertain, or user-owned state stays
available for inspection.

## Portable proof and compatibility

The focused real-process proof builds actual `mintclaw` and `mintclaw-node`
binaries and crosses the production TLS/WSS, gateway invocation, companion,
native worker, task, interaction, and channel-delivery boundaries. The proof
covers investigation with a blocking question, exact answer, isolated
mutation with steering, project-yolo commit/push/pull-request/deploy against a
local bare remote and fake CLIs, machine-yolo non-Git project/package/service
work, and cancellation with descendant cleanup. No real provider credential
or remote service is used by this proof.

```bash
scripts/run-go-integration-tests.sh
```

The integration gate includes
`TestNativeMintClawWorkerProjectsAndAnswersDurableQuestion` and
`TestRemoteCodingTaskTelegramToNativeCompanionVerticalSlice`. CI runs the
native real-process matrix on Linux and macOS.

| Surface | Linux | macOS | Boundary |
| --- | --- | --- | --- |
| Local `mintclaw code` and `resume` | Supported | Supported | Same native coding core and thread store |
| Remote investigation | Supported | Supported | Validated source checkout, read-only tools |
| Remote mutation | Supported | Supported | One isolated linked worktree and retained handoff |
| Remote project-yolo | Supported | Supported | Isolated worktree, node-local credentials, bounded external-effect receipts |
| Remote machine-yolo | Supported | Supported | Direct configured directory, companion-user machine authority, no rollback |
| Question, answer, steer, cancel | Supported | Supported | Existing durable interaction and typed worker control |
| Browser, files, jobs, services | Unchanged | Unchanged where already supported | Separate node command families and policies |
| Second gateway on companion | Not required | Not required | `mintclaw-node` plus child `mintclaw _worker` only |

This portable proof does not itself claim a production Telegram deployment.
The completed [P7.4 exit record](../architecture/local-coding-agent-p7-4-exit.md)
records backup, disabled-first deployment, the explicitly granted investigate
and isolated-mutation Telegram canaries, health, rollback, retained failures,
and the stop boundary before P7.5.
