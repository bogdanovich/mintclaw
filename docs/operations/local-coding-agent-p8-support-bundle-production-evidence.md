# Local Coding Agent P8 Support Bundle Production Evidence

Date: 2026-10-11

This record qualifies the P8 H1c read-only diagnostics and metadata-only
support-bundle path on the deployed Linux gateway. It contains identifiers,
hashes, counts, modes, and health facts only; no prompt, response, source,
command output, credential, environment value, or rich trace preview is copied
into this document.

## Revision and rollback point

- implementation PR: `#1580`, head
  `f47484a121663bf8bcdb65a199cf95c935231ef4`;
- clean exact-head automated review: no remaining must-fix findings and no open
  review threads;
- merge commit and deployed core revision:
  `dcc4a1a861ff7d02599073bddf472fc85fa35f3c`;
- prior deployed revision:
  `70e8f8b45163ff75cdf32244430c791adf835e56`;
- pre-deploy backup:
  `/home/server/mintclaw-core-backup-20261011T205031Z`;
- backup files: 32; and
- SHA-256 of the backup checksum manifest:
  `993ee61cdd2495b71974d9963761dba22c926872ff76be17ce4bc795106befa7`.

The server checkout was clean, on `main`, and configured with the expected
`bogdanovich/mintclaw` origin before a fetch and `--ff-only` update. Core,
node, and launcher binaries were rebuilt before the affected launcher and
gateway services restarted. Rollback restores the backed-up binaries and
systemd state, restarts only the affected services, and leaves mutable runtime
state untouched.

## Runtime and trace canary

A profile-scoped stateless turn used the deployed main profile and its declared
shared authentication-store override. It returned the exact harmless marker
and produced this passive trace:

| Fact | Evidence |
| --- | --- |
| trace ID | `trace-turn-f6afea4f0c1436c2200293be` |
| schema | `mintclaw.diagnostic_trace.v1` |
| source commit | `dcc4a1a861ff7d02599073bddf472fc85fa35f3c` |
| status | `completed` |
| records | 6 |
| content mode | `redacted_content` |
| redactor | `mintclaw.config_filter.v1` |
| truncated | false |
| dropped records | 0 |

The exact-ID `diagnostics show` request used `exact_trace_ids` scan mode. It
selected one candidate, scanned one 17,535-byte file, returned one valid trace,
and reported zero corrupt, expired, incomplete, problem, or dropped records.
The safe timeline contained turn start/end, model request/response, the
observed fallback-attempt fact, and a context snapshot. Correlation used the
turn, model-attempt, event, and hashed session/target identities rather than
prompt text or timestamps.

## Metadata-only bundle

The exact trace was exported to the new operator-selected directory
`/home/server/mintclaw-p8-support-bundle-20261011T2054Z`.

| Fact | Evidence |
| --- | --- |
| manifest schema | `mintclaw.diagnostic_support_bundle.v1` |
| privacy mode | `metadata_only` |
| automatic upload | false |
| manifest SHA-256 | `5b1703e2c6f5ad4bd9cdbccb236565830db7d8d906c2087ed5e82c2354c1da9f` |
| payload files | 2 |
| selected traces | 1 |
| directory modes | `0700` |
| payload/manifest modes | `0600` |

The manifest-listed SHA-256 digest for every payload was recomputed from disk
and matched. Trace payload paths include the store identity, so identical
trace IDs from distinct stores cannot collide. The operator also verified that
neither the exact smoke marker nor `/home/server/` appeared anywhere in the
bundle. The manifest declared omission of absolute paths, channel
destinations, commands/output, credentials/environment, prompts/responses/rich
previews, source/diff content, tool arguments/results, service logs, and
unrelated traces.

As a negative control, a rich export using only a one-hour time window was
rejected with `an exact identity selector is required`, returned exit status
1, and created no output directory. Rich export therefore fails closed before
collection unless the local operator selects one exact trace.

## Post-deploy health

After the runtime, exact-query, safe-export, digest, permission, privacy, and
negative-control checks:

- all ten expected `mintclaw-*` user services were active;
- product and global failed-unit counts were zero;
- the ten-minute error-level journal count was zero for every expected unit;
- legacy process count was zero;
- launcher HTTP returned the expected redirect; and
- reviewer webhook GET returned the expected 404.

This closed the initial deployed H1c support-bundle qualification. The
authoritative-owner follow-up below qualifies the dependent coding-thread and
presentation surface; neither canary infers owner state from trace prose or
replays runtime work.

## Authoritative-owner follow-up

PR `#1597`, exact head
`a800c8fe5aec8ac2a70e130e8e8f831eecf63c88`, passed the complete required CI
matrix and an exact-head automated re-review with both prior findings resolved.
It merged as `fa1027a4b6180a222e65d014efdf65e8a2f9f7a0` and was deployed from a
clean fast-forwarded checkout. The previous production revision was
`9070153b3d15ad4abe54cf2a6fd437490116b433`.

The checksum-verifiable rollback snapshot is
`/home/server/mintclaw-core-backup-20261011T222953Z`. It contains 35 files,
including the prior core, node, and launcher binaries plus user units,
drop-ins, service states, modes, owners, sizes, symlinks, and source revision.
The SHA-256 of its checksum manifest is
`f4d16fd8e4129deab9876d99c0b6a153583f393931cbb4b1d57765b69d411f35`.
Installed core, node, and launcher digests after deployment are respectively
`47d1db84f4f4603654a4ee82891aea5234eccf5360b9c52a50154e2aea6b06b5`,
`69c4c2fc453933f24e25ac535ff0ea3ddc23f4836ddafb3d9acaa73045e3b490`,
and `52e30b713c2325788c63b2d61cc78d55909a749f4111c28935b699fd9f951345`.

Two harmless headless local-coding turns exercised terminal truth and the
authoritative projection without tools or repository mutation:

| Outcome | Thread | Trace | Owner/presentation evidence |
| --- | --- | --- | --- |
| completed | `497b2ee8-176e-420e-8b44-5dd051b4291c` | `trace-turn-63bec86ef7969cab359658a6` | exact lookup `found`; transcript revision/count `2/2`; presentation revision `22`, activity `idle`, last outcome `completed`, two completed items |
| provider authentication failure | `70b27232-dacb-4190-885b-6a887b4ddba1` | `trace-turn-3b33d96a49400375db9611e2` | exact lookup `found`; transcript revision/count `1/1`; presentation revision `11`, activity and last outcome `failed`, two failed error items |

Both traces name the full deployed source commit, carry the same privacy-safe
project hash, retain explicit `completed` or `error` terminal truth, and report
no truncation. The failed canary exposed an unavailable refresh credential for
the server's default OpenAI model; an explicit configured `deepseek` model
completed the positive canary. This is an operator credential finding, not an
owner-snapshot ambiguity: the failure is represented consistently in the
trace, canonical history, and content-free presentation state.

The successful exact selector exported a metadata-only support directory at
`/home/server/mintclaw-p8-owner-snapshot-20261011T2234Z` with manifest SHA-256
`756c4e923c8720064159ed920dfc07be7336c78410a8369e6929353ccf5deb70`.
Its three manifest-listed payloads are `health.json`, `owners.json`, and one
trace summary. Every payload digest was recomputed successfully. `owners.json`
contained the exact `found` lookup, full source commit, bounded transcript
counts, and completed presentation checkpoint, while excluding the source
checkout path, canary response text, provider model identifier, transcript
content, and unrelated owner records. The source checkout remained clean.

After the ten-minute observation window, all ten expected `mintclaw-*` user
services remained active, product and global failed-unit counts were zero, no
legacy process was present, launcher and reviewer-webhook probes returned their
expected statuses, and every expected unit plus the aggregate error-level
journal scan reported zero entries. H1c is therefore complete for the admitted
local coding and support-bundle surface. H6 retains the integrated remote-task,
companion, retention, and deletion canaries rather than broadening this slice.
