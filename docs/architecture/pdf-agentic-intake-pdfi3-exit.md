# PDFI3 Semantic Form Planning And Natural Dialogue Exit

## Decision

PDFI3 is complete for its bounded planning and dialogue scope. The frozen admission is the
[PDFI3 goal](pdf-agentic-intake-pdfi3-goal.md); the continuing program is the
[agentic intake roadmap](pdf-agentic-intake-roadmap.md). Qualification ran on 2026-10-04 against deployed
`931c3eff70c7e48f0ba494f50c673d045058aba1` on `server@oc`, not an unmerged branch.

This is not comprehensive large-form qualification or proof that the original I-134 Telegram incident is fixed.
PDFI4 and the conditional PDFI5 remain outside this exit.

## Merged Packets

| Packet | PR | Merge commit |
| --- | --- | --- |
| Admission, matrix, budgets, and stop gates | [#1456](https://github.com/bogdanovich/mintclaw/pull/1456) | `61dc7d4bfc6f75923d514a0ac2f3ddb7f1e23cbb` |
| Bounded field windows and value-free progress | [#1460](https://github.com/bogdanovich/mintclaw/pull/1460) | `e0c1b54b596d89d7af1f6f617d4f9d46a1527225` |
| Semantic dialogue, evidence, and coherent controls | [#1465](https://github.com/bogdanovich/mintclaw/pull/1465) | `1299057e4ed58c69ec237a2440dca6f2848a8352` |
| Qualification-discovered empty-value correction | [#1468](https://github.com/bogdanovich/mintclaw/pull/1468) | `931c3eff70c7e48f0ba494f50c673d045058aba1` |

Every code packet received actual exact-head review, all 16 CI checks green, no actionable unresolved threads,
and an owner PR-level rocket before merge. Final reviewed heads were `7dc83f49`, `aa3e90a6`, and `5fc21522`.
For #1468, reviewer comment `5977938984` names the full immutable head; approval reaction `543817648` followed it.
The admission and this documentation-only exit use the standing docs-only review exception.

## Architecture

The agent and bundled PDF skill own purpose, applicability, question order, ordinary intent clarification,
and correction. Native code still owns complete source/schema identity, stable IDs, protected values, deterministic
normalization, review, approval, verification, and delivery. No semantic field-name inference, persistent planner,
new daemon, secret store, or delivery queue was introduced.

Explicit page/offset windows project the existing schema into at most eight candidates. Counts remain global;
selection totals, truncation, and the next offset describe the selected window. Progress distinguishes preserved,
confirmed, missing, conflicting, and intentionally blank fields without decrypting values. Explicit browsing remains
available at review readiness for correction; default review-ready behavior still enforces review.

The retained owner-authorized source supports bounded current-call text or rendered evidence without a new upload.
Text reads at most two pages/4,000 characters; rendering uses one page at 72 DPI with a 1,024-pixel edge cap.
The initial value-free summary/plan budgets remain 512/768 characters. Notes live in conversation/tool context,
not an authoritative native semantic plan. Evidence does not acquire write authority or get delivered as a page image.

Material intent can be clarified in ordinary chat before collection. Personal answers remain protected. Authored
checkbox questions require paired labels in the user's language; native normalization binds them to booleans.
Bounded value-free question controls survive native Clarify/Back snapshots. No natural-language equivalence engine
was added. Protected receipts, correction invalidation, and the existing transaction remain authoritative.

## Qualification Fixtures

The [fixture generator](../operations/pdfi3-fixtures.py) uses ReportLab `4.4.10`, invariant output, and synthetic
data only. These are equipment-loan/inspection forms, not I-134-specific production rules.

| Fixture | Pages / fields | SHA-256 |
| --- | --- | --- |
| Small loan form | 2 / 5 | `f9100e47a0d3346cf2e7edebce927ffcaa43b3ee4c164186f781b08b93fc94ec` |
| Conditional borrower form | 2 / 3 | `f8f72ee3f81c00d4c0c3a529c5789f4891a2df494fe0adbebb8863f8d3668bb1` |
| Large inspection form | 10 / 100 | `0d179b4cf276c59eef0e960cd1120cfe7924adc13b65a900094fbdad79ace7b5` |

Generate them with:

```sh
uv run --with reportlab==4.4.10 python docs/operations/pdfi3-fixtures.py /tmp/pdfi3-fixtures
```

Small and large fixtures were visually inspected after Poppler rendering; all fixtures were independently checked
with pypdf. Field inventory and
current-value presence agreed between deployed discovery and pypdf `6.1.1` on all three fixtures plus the existing
eight-field `acroform-fields.pdf`: counts were 5/3/100/8, with 3/0/0/6 nonempty current values.
The focused oracle emitted `MINTCLAW_PDFI3_FIELDS_ORACLE_OK`.

## Live Agent Evidence

An ephemeral client used the existing authenticated loopback MintClaw WebSocket protocol and separate session IDs.
It resolved credentials locally without printing them. Prompts described the operator outcome, not tool actions
or field IDs. Protected test values were random, held in a private temporary file, and supplied by key; they were
not shell arguments or pasted into this report. Typed navigation used the normal interaction/prompt identity binding.

### Small Form

Session `pdfi3-small-20261004-c`, job `form_job_09697a5549f37c053fcf1aa7b6f378d2`:

- The agent inspected the immutable source, extracted pages 1-2 at 4,000 characters, discovered the complete schema,
  prepared the job, and chose the requested name correction. The summary/plan were 204/269 characters, explained
  protected questions, and appeared before the first value question.
- Clarify retained the focused name question. Back from the contact question reopened the preceding name question
  with a new interaction identity. Navigation did not append answers; answers entered only through protected receipts.
- The checkbox asked whether to return equipment by mail, with matching Russian checked/unchecked labels. Clarify
  retained both labels. The checked label normalized correctly; the optional delivery note used typed Skip.
- The first value-free review reported 3 supplied fields, 1 preserved equipment description, 1 intentional blank,
  and no blockers. It stopped without creating a file.
- Ordinary correction intent selected the name in the same job without a new attachment or a field ID request.
  Accepting the replacement cleared the previous review revision and assignment digest. A fresh review followed.
- A separate finish message reached the native approval boundary. One typed `allow_once` produced one delivered PDF.
  Native commit reported source unchanged, 8 structural assertions, 4 visual assertions, and 3 written fields.
- Independent pypdf readback checked all five fields, including the replacement name, contact, checked checkbox,
  empty note, and unchanged equipment. The source SHA remained unchanged. The job reached `completed` and erased
  terminal protected field state.

The downloaded 7,500-byte artifact SHA was
`f4c7b0f0b8905febffa4205dad326953c5ec151e135690ab7840d31b23c0f86d`.
The independent marker was `MINTCLAW_PDFI3_SMALL_VERIFIED`, with delivery count 1 and response leaks 0.

### Conditional Form

Session `pdfi3-conditional-20261004`, job `form_job_d4dcef77c31f09dfeff205da61d039d4`:

The agent explained the own-request versus representative sections and called the ordinary intent checkpoint before
personal input. It asked an either/or question in Russian without generic Yes/No protected controls. The ordinary
own-request reply resumed the same prepared job and asked only the applicable borrower fact, retaining a summary/plan.
It did not invent a routing code or request the attachment again. The test canceled before values: ledger revision 0,
terminal state `canceled`, and no artifact. This proves planning/resume, not completion of every conditional branch.

### Large Form

Session `pdfi3-large-20261004`, job `form_job_6a2695a3b7d3195b2f0707da232744ca`:

The operator requested only Safety. The agent read pages 1-2, explicitly read pages 7-8 in another bounded 4,000-character
request, and discovered/prepared a pages-7-8 view. Its 183/265-character summary/plan identified the ten-page inspection
and the 20 Safety findings. The first question selected Safety 61, not an Inventory field.

The native view reported 100 global missing fields, selected total 20, limit 8, eight candidates, `truncated: true`,
and `next_offset: 8`. The test canceled before values; ledger revision 0 and no artifact. Further windows and retained
render/text evidence have deterministic tests. Completing the 100-field form is deliberately PDFI4, not this evidence.

### Trace And Privacy Record

All cited traces use `mintclaw.diagnostic_trace.v1`, `redacted_content`, and no truncation flags:

| Case | Trace |
| --- | --- |
| Small initial plan/question | `trace-turn-f3c33e1a802cf94b5c73be84` |
| Small Clarify / Back | `trace-turn-a1ffb8d11876a836a9e4e388` / `trace-turn-8c2e6573cc12f42334183432` |
| Natural correction / fresh review | `trace-turn-7dba401acc2cfb5a351183c1` / `trace-turn-18a13129b3975781a8abf57f` |
| Approval / commit | `trace-turn-b624f6869b5771b8cbbf8580` / `trace-turn-9d5727f311173b8af8f51c5e` |
| Ordinary intent / same-job resume | `trace-turn-98baf6da0f98195f0b16c138` / `trace-turn-538ced0b6c36a6ebd889c179` |
| Large bounded plan/question | `trace-turn-ad569291318dffb55b3e7e4f` |

The final protected-sentinel scan covered 4,665 files / 654,210,837 bytes in main's state, logs, jobs, memory,
workspace state/diagnostics, sessions, memory, transcripts, and jobs. It found zero matches. A separate main-service
journal scan since 07:20 UTC, capped at the last 10,000 lines, also found zero matches. Client response checks found
zero leaked sentinels. The deliberately private test input file and the delivered PDF were not treated as history leaks.

## Qualification Discoveries

1. A private, untracked workspace PDF skill shadowed the updated bundled skill. It contained only obsolete read-only
   instructions. Its exact hash was verified and the directory was moved, not overwritten, into the first deployment
   recovery set. Fresh turns then loaded the bundled form workflow. Skill overrides must be inspected during deployment.
2. ReportLab emits `/V ()` for empty text fields. Discovery had counted key presence as a preserved answer, hiding missing
   contact/note fields and all 100 blank large-form fields. #1468 fixes this with typed current-value presence, inherited
   empty overrides, separate defaults, and fail-closed malformed values. See the
   [empty-value update boundary](pdfi3-empty-value-fix.md); obsolete jobs fail stale-schema checks rather than rebinding.

## Validation And Operations

Required local formatting/lint, affected document/tools/skills suites, and agent/interaction regressions passed.
For #1468, full macOS suites took 129/86/62 seconds; focused regressions also ran successfully in an isolated
Linux/amd64 test binary on the host, which was removed without installing unmerged production code.
Exact-head CI covered tests, race, lint, frontend, security, platform compilation, macOS portability, three PDFium WASM
platform checks, integration, and browser Windows. No test, memory cap, or CI gate was weakened.

Core, node, and launcher were built from merged main and atomically installed with checksum checks. Six affected
core/launcher units restarted. The exact-revision isolated smoke returned `MINTCLAW_PDFI3_FIX_RUNTIME_OK` in 11.851 seconds
and produced `trace-turn-6eec5637525a420d9b64b682`. All 12 expected units were active; failed units, legacy processes,
and error-level journal entries over the observation window were zero. HTTP probes returned expected 302/404/401.

Recovery sets are `/home/server/mintclaw-pdfi3-recovery-20261004.eO80l3` (pre-#1468) and
`/home/server/mintclaw-pdfi3-recovery-20261004.iSoLBG` (preceding binary/config and retired skill override).
They contain checksum-verified distinct effective binaries, unit mappings, configs, SHAs, and restore instructions.
Capacity checks passed with the estimated peak plus 5 GiB headroom. Own staging and temporary clients/oracles/private
inputs were removed after qualification; runtime traces, sessions, verified delivery, and recovery sets were retained.
Redacted client response/navigation evidence is retained under
`/home/server/.mintclaw/main/evidence/pdfi3-20261004`, separately from recovery backups.

Rollback restores only the affected binaries/configs and units from the chosen verified set. Do not restore old
mutable jobs, histories, media, or ledgers over new-runtime writes. Jobs whose empty-value schema classification differs
must restart from the unchanged source. Restoring the retired workspace override would deliberately restore its obsolete
read-only policy, so it is not a normal rollback step.

## Limits And Follow-Up

- Headless free-text answers used `/answer` and typed choices, not a human Telegram reply/callback. The original
  I-134 exchange remains unverified. Repeat that channel case with synthetic data before claiming its resolution.
- Full conditional/large-form collection, composite answers, all conditional branches, and forced live
  restart/compaction are not claimed. Restart/compaction, owner isolation, stale approval, ambiguity, and evidence
  paths have deterministic regression coverage; comprehensive live qualification remains PDFI4.
- An optional text question was phrased as a blank-confirmation question; the test used typed Skip, not a free-text
  "yes". Natural-language blank interpretation is not a native guarantee.
- `scripts/document-fields-oracle.sh` passed its standard eight-field oracle but then failed a pre-existing
  `hybrid_discovery_only` expectation: Linux now reports the already-admitted `hybrid_print_ready` mode. No full pass
  of that older script is claimed. Its hybrid assertion needs separate maintenance; PDFI3's focused four-fixture
  pypdf oracle passed. XFA capability work is not reopened here.

The next roadmap milestone is PDFI4. PDFI3 does not automatically admit or start it.
