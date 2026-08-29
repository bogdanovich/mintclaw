# Session cutover converter

This temporary command prepares the normal-session portion of the Z1
stopped-state cutover. It is an external deployment tool: MintClaw does not
import it, startup does not call it, and the runtime keeps only the current
persisted contract.

The command is copy-only. It requires a new output path and never writes to a
configured workspace. For every active workspace named by the supplied
current configuration files, it inventories only top-level `*.meta.json` and
paired `*.jsonl` files in `sessions/`:

- opaque keys with an exact current scope-v2 document go under `retained/`;
- every other key or scope goes under `archived/` without byte changes;
- retained metadata loses only the registered root `aliases` member;
- retained old nested tool calls become the current flat tool-call shape; and
- every retained metadata, scope, and JSONL record passes the runtime's exact
  current decoder before the output is published.

The converter rejects orphan histories, filename/key mismatches, unknown or
duplicate metadata fields, unfinished history mutations, count mismatches,
non-canonical JSONL framing, malformed old tool calls, ambiguous signatures,
source changes during the pass, symbolic-link inputs, and output overlap. It
does not infer identities, upgrade scope versions, repair messages, or
translate archived state.

Run it only with an explicit source root, a new output path, and every active
profile configuration:

```sh
go run ./scripts/sessioncutover \
  --source-root /home/server \
  --output /absolute/new/candidate \
  --config /home/server/.mintclaw/main/config.json \
  --config /home/server/.mintclaw/family/config.json
```

Repeat `--config` for all active profiles. `manifest.json` records config
digests, the deduplicated session directories, cohort totals, and source and
output SHA-256 for every emitted session file. Files unrelated to the normal
session metadata/JSONL pair, including Seahorse databases and manual backup
directories, remain outside this tool and are covered by the full deployment
backup.

A live copy-only run is useful as a rehearsal, but only a pass made after all
writers stop is eligible for deployment. The converter and its tests must be
deleted from the repository after the Z1 deployment, rollback exercise, and
evidence capture succeed.
