# Local Coding Agent P0.3 Owner-scoped Stores

Status: implemented

Roadmap packet: P0.3 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

A `CodingRuntimeProfile` owns one construction-time `CodingRuntimeStoreFactory`.
The factory opens both the canonical JSONL session store and the derived
Seahorse engine from paths supplied by the already validated `CodingRuntimeLayout`.
Agent and context construction no longer rediscover those paths from the
execution workspace.

The ownership unit for a local coding session is the coding thread. Its files
are rooted together but remain outside the source checkout:

```text
<coding-thread-state>/
  sessions/              canonical JSONL transcripts
  context/
    seahorse.db           derived context for this coding thread
```

Consequently, two resumable coding threads have two different Seahorse SQLite
files. MintClaw does not accumulate all coding history in a process-global
database. Personal gateway context remains owned by the separate gateway
workspace lifecycle.

## Authority and Recovery

JSONL is the durable conversation authority. Seahorse ingests and reconciles
from that transcript, so `seahorse.db` is disposable and rebuildable. The
coding constructor reads only its admitted thread state root and rejects a
configured custom Seahorse `dbPath`.

Coding profiles admit only the stateless `none` manager and the thread-scoped
`seahorse` manager. Other registered context managers remain rejected before
store construction until they define an equivalent state-root contract.

The state preflight checks `sessions`, `context`, and `memory` before opening
the first store. It also rejects a symlink or non-regular existing
`context/seahorse.db`, preventing SQLite from escaping the admitted state root.

## Construction and Rollback

Store factories are injected into the profile before registry construction.
Nil and typed-nil factories are rejected at profile admission.
Successful factory calls must also return non-nil products: nil or typed-nil
canonical stores and nil Seahorse engines fail construction through the normal
rollback path.
Each agent receives its canonical store before it can enter the registry. A
failure while opening a later owner closes every earlier instance. The runtime
profile itself is installed on the loop before context-manager resolution, so
Seahorse uses the same injected factory and layout-derived path. A context
construction failure closes the partial context manager and the complete
registry, including every canonical store and internally owned provider.

`NewAgentLoopChecked` remains the current gateway constructor. It does not
instantiate a `CodingRuntimeProfile` or share coding-thread storage policy.

## Done Evidence

Focused tests prove that:

- a coding loop can run the default Seahorse context manager without creating
  its execution root;
- canonical JSONL and Seahorse are created below their respective external
  state directories;
- two coding threads resolve to different `context/seahorse.db` files;
- context directories and database targets are preflighted fail-closed;
- a custom coding-profile Seahorse path is rejected;
- an unsupported context manager is rejected before any store is opened;
- nil and typed-nil canonical store products are rejected immediately;
- a nil Seahorse engine product closes the canonical store through normal
  rollback;
- failure of a later session-store factory closes the earlier store exactly
  once and returns no partial loop; and
- Seahorse factory failure closes the canonical store exactly once and returns
  no partial loop; and
- failure of a later Seahorse runtime closes the already opened engine and all
  canonical stores instead of leaking a partial context manager.

P0.4 selects the isolated coding tool profile without changing gateway
persistence authority or adding another gateway storage path. See
[`local-coding-agent-p0-tool-profiles.md`](local-coding-agent-p0-tool-profiles.md).
