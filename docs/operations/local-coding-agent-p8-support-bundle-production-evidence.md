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

This closes the deployed H1c support-bundle qualification. Authoritative task,
interaction, delivery, invocation, and coding-thread owner snapshots remain a
separate H1 follow-up; this canary does not infer owner state from trace prose
or replay runtime work.
