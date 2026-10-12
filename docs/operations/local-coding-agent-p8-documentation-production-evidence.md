# P8 Documentation And Operator Guidance Production Evidence

Packet: [H5 / P8.4](../architecture/local-coding-agent-p8-hardening.md#h5--p84--user-documentation-and-migration).

Status: complete on 2026-10-12. H6 was still active at this packet's close;
the subsequent [P8 exit](../architecture/local-coding-agent-p8-exit.md)
records the complete integrated qualification and stable-scope boundaries.

## Merged guidance

- [Core docs #1627](https://github.com/bogdanovich/mintclaw/pull/1627), merge
  `e2542e051eee720880a8d50885a9fd23b0c3cead`, documents onboarding,
  instruction precedence, personal/chat versus coding ownership, companions,
  autonomous profiles and symptom-first diagnostics.
- [Trace-debug package #1628](https://github.com/bogdanovich/mintclaw/pull/1628),
  exact head `09baf3489dd94597a02f5a859b7fcacdadf904cc`, merge
  `5164b78eebd72f67bab41e7b32e38ecf8c2039b1`, passed all 17 checks, clean
  exact-head review and owner rocket approval.
- Private `mintclaw-workspaces` PR #40, merge
  `28e0627ed41e7545f3455afd7b8ce49c8e76bf07`, aligned shadowing workspace
  packages under the owner's review-free workspace policy.

The [coding guide](../guides/coding-agent.md) is the user entrypoint. The
[upgrade guide](coding-state-upgrades.md) requires stopped-owner,
checksum-verifiable state backups, exact schema/path admission and matched
binary/config rollback. It does not restore mutable transcripts blindly or
claim legacy aliases. H5 required no schema or data migration.

The skill-creator workflow shortened the skill entrypoint and moved advanced
correlation details into a conditional reference. Metadata-first queries,
exact owner joins, bundle verification, private-rich gates, no replay, ordinary
regressions and fresh fixed traces remain covered. Existing steering/handoff
guidance and UI metadata were preserved.

## Deployment and preservation

The H5 deployment used then-current merged main
`3d1afa5573e7448a0d46169143e03be0bf4e182b`; intervening source changes were
inspected. The server source remained clean. No live config override, canonical
state or companion grant was replaced.

Rollback set `/home/server/mintclaw-p8-h5-backup-ZBX9kc` contains previous
binaries, units/drop-ins, active configs/run scripts, authentication snapshot,
source revision and service states. Its verified manifest SHA-256 is
`ff5073fe12167bdecd053070ee86cd96ac3965faa1c8cc0559286c4f831a4df5`.

Installed server binary SHA-256 values were:

- core: `6c47ce14def327718e05f60c3de8e63449c12a19b935e1d4a7c9832e642f83d1`;
- node: `d1c37b2a5364184175f35076952f56cee24d14ead179d49d2e25727f53c89e10`;
- launcher: `a266ff3c6fc815674c208de83dd1d4aaabea79e61e299c16a33ef8956449fa0f`.

All five managed profile packages resolve to generation
`a0b3ec80c64952a81a2bc78cfa72b99bfa7d8531da7fa9ef6bc6231b4e685d8a`.
Six shadowing packages were replaced only after unchanged-path checks and a
separate backup, preserving unrelated dirty config and byte-identical UI
metadata. The live workspace checkout remained at `328b0ec` with intentional
package-only overlays; it was not reset or wholesale fast-forwarded.

The deployed entrypoint SHA-256 is
`d6b47778aff1e23ac1cbd8f662f19b450f457a45dc7f3060cb514a17879b4b2f`;
the reference SHA-256 is
`cca93efde819fde11e0ff22c1e647de9d32d75877a21acba5a60106c92f49aab`.
The skill rollback set `/home/server/mintclaw-p8-h5-skill-backup-mrovLn` has
verified manifest
`894befdf66a8a0bdad4c6ef58ab1c91e65b06be41b4b00df3bfc8aa4bda62077`.
The installed Codex package was also validated, with its prior package retained
in `/tmp/mintclaw-p8-codex-skill-backup-vLwzIW`.

## Runtime and diagnostic evidence

At 03:16:34 UTC, more than ten minutes after restart, all ten expected units
were active, failed units and legacy processes were absent, the error-level
journal window was empty, and HTTP probes returned the expected 302/404.
Both rollback manifests were reverified.

Fresh stateless trace `trace-turn-29b390a20f2857ebb671c734` recorded the exact
deployed commit, completed outcome, six records, redacted-content capture,
no tools and no truncation/drops. An exact query scanned one 17,537-byte trace
with zero corruption/incomplete/drop counts. The 16-byte response SHA-256 was
`f7c8464ce944fd1c0c521cbac6272bc8650fa2f785c1d178c4389778df6377b7`.

Doctor loaded all five configs without new load/error/fail IDs. Existing policy
fail counts remained main 14, family 7, nutrition 7, reviewer 8 and spouse 9;
these concern plaintext credential storage and broad automation/write/approval
policy. H5 does not claim to remediate those pre-existing deployment findings.
Private before/after receipts remain in
`/home/server/mintclaw-p8-h5-validation-ae8HDW`.

The subsequent H6 exit records the final integrated matrix, exact-source
release snapshot, stream-failure and local-task correlation repairs.
