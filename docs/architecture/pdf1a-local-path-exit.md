# PDF1A Authorized Local Path Exit Record

## Decision

The PDF1A authorized local-path follow-up is complete for `linux/amd64`. An authorized user can name an exact PDF
already on the gateway host, the existing deferred `document` tool can inspect it only after both current-message
selector matching and workspace/read-policy admission, and later extraction or rendering uses a turn-owned immutable
`media://` snapshot rather than reopening the mutable host path.

This follow-up adds no second document tool and no generic filesystem search. Attachments retain their existing exact
current-turn reference authority. macOS runtime support and PDF2 AcroForm work remain separate roadmap milestones.

## Merged revisions and review

- Local-path admission: [PR #1192](https://github.com/bogdanovich/mintclaw/pull/1192), final reviewed head
  `89b22c20b6d42e38209067cd6297cd7e6176e7df`, merge commit
  `637335bcffc91de4e63319f76f86a56be1ca1810`, merged on 2026-09-13 UTC.
- Deployed-regression hotfix: [PR #1196](https://github.com/bogdanovich/mintclaw/pull/1196), final reviewed head
  `672ca35b48d4f2449c8a5514350934a24368035a`, merge commit
  `290f1cb797b0c8415ac9707bb87530ddcca4e078`, merged on 2026-09-14 UTC.
- Both PRs passed all ten required CI checks, had no unresolved actionable thread, received an exact-head automated
  review with no remaining findings, and had an owner PR-level rocket before merge.

The review of #1192 found that workspace policy alone would let a model substitute another PDF from the same allowed
directory. The final implementation carries exact local PDF selectors from the current user message in transient tool
context and checks an exact match before filesystem resolution. Regressions deny a different in-policy PDF and an
equivalent absolute/relative alias when the user did not supply that exact selector.

## Delivered contract

- A current message containing a local `.pdf` selector activates the existing PDF workflow when the optional skill is
  available. Quoted selectors support whitespace.
- Selector provenance is captured independently of optional skill activation. The skill controls prompt guidance, not
  filesystem authority, so an otherwise permitted tool call does not fail merely because the workspace omitted the
  skill.
- Only `document inspect` accepts `path`, and it accepts exactly one of `path` or `source`. `extract` and `render`
  require the temporary source ref returned by a successful inspect.
- The exact selector must also pass the configured agent workspace and `tools.allow_read_paths` policy. Symlinks,
  special files, non-PDF bytes, traversal, and resource-limit violations remain fail-closed.
- Inspection snapshots immutable bytes, rechecks size and SHA-256 while registering the temporary source, and never
  reopens the original path during extraction or rendering.
- Raw local paths are replaced by one-way digest tokens in durable tool arguments and omitted from tool logs and
  diagnostic previews. Safe reports retain only bounded facts, digests, page selections, and opaque refs.
- Terminal turn cleanup releases the temporary ref, its private media file, and protected document scratch.

## Validation and deployed evidence

The implementation and hotfix passed repository formatting, docs lint, changed-package lint, focused unit tests, race
coverage, Linux cross-build, all repository CI jobs, and exact-commit Linux integration on `server@oc`. The integration
covered both the pre-existing Telegram attachment vertical slice and the new local-path tool slice.

The final merged main was deployed on 2026-09-14 UTC:

- source/runtime revision: `290f1cb797b0c8415ac9707bb87530ddcca4e078`;
- version: `mintclaw v0.1.0-p8a.2-1815-g290f1cb7`;
- first rollout recovery bundle: `/home/server/mintclaw-pdf1a-backup-20260913T233832Z`;
- hotfix rollout recovery bundle: `/home/server/mintclaw-pdf1a-hotfix-backup-20260914T001204Z`;
- active-workspace skill recovery bundle: `/home/server/mintclaw-pdf1a-skill-backup-20260914T001821Z`.

Every rollout used a clean `bogdanovich/mintclaw:main`, fast-forward only, built core, node, and launcher before
restart, installed matching binaries, and restarted only main-web plus the five gateways. All five configs loaded;
doctor exit 2 represented existing policy findings rather than a schema or load error. Final status reported all ten
expected units active, zero product/global failed units, zero error entries in ten minutes, zero legacy processes,
launcher HTTP 302, and the expected reviewer-webhook HTTP 404.

The checked-in deployed smoke returned:

```text
core_sha=290f1cb797b0c8415ac9707bb87530ddcca4e078
state=succeeded
scratch=clean
agent_channel=passed
marker=MINTCLAW_PDF1A_AGENT_CHANNEL_OK
marker=MINTCLAW_PDF1A_LOCAL_PATH_OK
marker=MINTCLAW_PDF1A_DEPLOYED_OK
```

## Live regression and trace evidence

The first real `agent live` smoke after #1192 failed closed with `source_not_authorized`. Passive trace
`trace-turn-b48a02cdea4cd7fd5e48e3d7` proved that the protected tool-call digest matched the exact user path, while the
active workspace lacked the optional PDF skill. The code incorrectly captured path provenance only when the full
skill workflow was available. #1196 separated authority provenance from skill activation and added the deployed-shape
regression.

After deploying #1196, the same active workspace without the skill successfully inspected and extracted the local
fixture. The response returned `MintClaw text fixture` on page 1. The model first attempted zero-based page 0, failed
safely, and corrected itself; trace `trace-turn-730017e7df7c5f608d0a83b8` records the complete bounded sequence.

The exact checked-in PDF skill was then installed in the active main workspace under the config/data upgrade workflow.
A second live request (`88d1daf8-c7ed-4b25-90eb-99935f70d788`) completed directly with inspect followed by
extract page 1 and returned the same marker and page. Its trace is
`trace-turn-084c7ce100c5a63c230ddfce`: schema `mintclaw.diagnostic_trace.v1`, outcome `completed`, 29 records, no
truncation, and exactly two successful document calls.

The final trace contains neither the raw local path nor extracted marker text; turn input preview is omitted and the
path-bearing inspect arguments are redacted. The live fixture and document scratch were absent after the turn, and the
temporary `media://` source was absent from the persistent media index.

## Operator use

Put the PDF inside the target agent workspace, or configure an exact `tools.allow_read_paths` boundary. In the current
message, give the exact path and quote it if it contains whitespace. For example:

```text
Прочитай локальный PDF "/absolute/workspace/Tax Form.pdf" через document tool.
Верни нужное значение и укажи номер страницы. Не используй shell.
```

The agent must inspect that exact selector first and use the returned opaque ref for selected one-based pages. It must
not search by filename, substitute another path, or repeat the host path in the final answer. The attachment workflow
remains preferable when the file is on the user's workstation rather than the gateway host.

## Rollback

For a hotfix-only rollback to the reviewed #1192 runtime, stop only the six affected units, restore the previous core,
node, and launcher bytes from `/home/server/mintclaw-pdf1a-hotfix-backup-20260914T001204Z`, then restart and run the
status and document smoke scripts. The backed-up local-bin core can also restore
`/home/server/src/mintclaw/build/mintclaw-linux-amd64`; the corresponding launcher binary can restore
`build/mintclaw-launcher-linux-amd64`. Preserve merged source and mutable runtime state for diagnosis.

To roll back the complete local-path feature, use `/home/server/mintclaw-pdf1a-backup-20260913T233832Z` for the
pre-feature binaries. The PDF skill was absent before deployment. Remove it only after stopping main, verifying that
its current bytes still match the deployed merged source, and moving the legacy deployed PDF skill directory to a
new quarantine path; do not overwrite or delete unrelated active workspace skills. The skill snapshot and its
checksums are retained in `/home/server/mintclaw-pdf1a-skill-backup-20260914T001821Z`.

No configuration schema or persistent application-data migration was introduced. Do not roll back mutable session,
media, trace, or workspace data over newer state.

## Residual boundary

- Runtime extraction and rendering remain qualified only on `linux/amd64`; macOS parity stays in its dedicated lane.
- PDF1A does not add password handling, OCR, provider-native PDF transport, form interpretation or writing, XFA
  mutation, signing, or trust validation.
- A path is useful only on the gateway host. A workstation-only file must be attached or transferred through an
  independently authorized mechanism.
- The active PDF skill improves page-selection guidance but is not an authority grant and is not required for the
  runtime to preserve an exact user selector.

The authorized local-path follow-up stops here and does not start PDF1B, PDF2, or macOS implementation.
