# Local Coding Agent P0.3 Owner-scoped Stores

Status: implemented

Roadmap packet: P0.3 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

A strict `RuntimeProfile` owns one construction-time `RuntimeStoreFactory`.
The factory opens both the canonical JSONL session store and the derived
Seahorse engine from paths supplied by the already validated `RuntimeLayout`.
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
database. A personal runtime similarly resolves one context database per
personal-agent state owner; admitting the complete personal tool bootstrap on
the strict constructor remains the dependent P0.4 cutover.

## Authority and Recovery

JSONL is the durable conversation authority. Seahorse ingests and reconciles
from that transcript, so `seahorse.db` is disposable and rebuildable. The
strict constructor does not read or migrate legacy workspace session paths and
rejects a configured custom Seahorse `dbPath`; deployment is responsible for
moving and verifying existing personal state once, without indefinite fallback
reads.

Strict profiles admit only the stateless `none` manager and the owner-scoped
`seahorse` manager. Other registered context managers remain rejected before
store construction until they define an equivalent state-root contract.

The state preflight checks `sessions`, `context`, and `memory` before opening
the first store. It also rejects a symlink or non-regular existing
`context/seahorse.db`, preventing SQLite from escaping the admitted state root.

## Construction and Rollback

Store factories are injected into the profile before registry construction.
Each agent receives its canonical store before it can enter the registry. A
failure while opening a later owner closes every earlier instance. The runtime
profile itself is installed on the loop before context-manager resolution, so
Seahorse uses the same injected factory and layout-derived path. A context
construction failure closes the partial context manager and the complete
registry, including every canonical store and internally owned provider.

The legacy `NewAgentLoop` path retains its existing migration and fallback
behavior. That keeps the deployed personal gateway operational until its
explicit P0.4 profile and one-time deployment migration are ready.

## Done Evidence

Focused tests prove that:

- a coding loop can run the default Seahorse context manager without creating
  its execution root;
- canonical JSONL and Seahorse are created below their respective external
  state directories;
- two coding-thread owners resolve to different `context/seahorse.db` files;
- context directories and database targets are preflighted fail-closed;
- a custom runtime-profile Seahorse path is rejected;
- an unsupported context manager is rejected before any store is opened;
- failure of a later session-store factory closes the earlier store exactly
  once and returns no partial loop; and
- Seahorse factory failure closes the canonical store exactly once and returns
  no partial loop; and
- failure of a later Seahorse runtime closes the already opened engine and all
  canonical stores instead of leaking a partial context manager.

P0.4 can now select explicit personal and coding tool profiles without changing
the persistence authority or adding another storage construction path.
