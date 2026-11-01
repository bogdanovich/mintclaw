# Manual PDF Evals: Remaining V1 Pilots

## Results

Three required cases without an earlier documented attempt in this goal ran serially once each on fresh profiles, using
identified code after merged #1853. All returned **inconclusive**, not PDF qualification.

| Case | Canonical code | Run / trial duration (ms) | LLM-user calls |
| --- | --- | --- | --- |
| `pdf.loan.correct` | `domain_evidence_unavailable` | 56175 / 55860 | 3 returned |
| `pdf.conditional.intent` | `missing_route_identity` | 12223 / 12011 | 0 |
| `pdf.large.section` | `missing_route_identity` | 11298 / 11128 | 0 |

Correction continued within its one fresh client session through three finals, three separate LLM-user responses
and a fourth-turn ordinary question. No protected PDF owner/census was established; domain input/evidence could
not be admitted. Conditional and large each received an unrouted final before an eligible protected question.
These facts do not establish correct correction, applicability, partial-draft output, native logical settlement
or verified PDF delivery. Their specific target root causes are not inferred from ordering alone.

All required privacy/field/structure/delivery checks remained inconclusive with missing evidence. All reports have
`evidence_eligible: false`; judge placeholders have no call. No judge, regrade or comparison was attempted.
No failed model request, used profile or case was retried. Tasks, facts contracts, personas, rubrics and budgets
were unchanged. These actual negative results must not be upgraded by later code or offline grading.

## Runtime And Isolation

The code, Linux AMD64 static binary and archive are the same identified packet as the
[ambiguity v1 pilot](manual-agent-evals-browser-ambiguity-v1-pilot-20261020.md):

- Reviewed head `9186025acbd0c7835cd4dc023053bff3dddc46ec`; confirmed merge
  `a57f230dcfd7d40e98adac2e5dbbccad9a64e4e3`, with independently equal reviewed/merged trees.
- Go 1.26.9, CGO disabled, `goolm,stdjson`, recorded build time `2026-10-19T23:54:00Z`.
  Version `eval-browser-ambiguity-v1` is an operational build label, not a PDF qualification claim.
- Binary SHA-256 `30714cc3ccc5515bf4eac613c96f66cd8c7dd2792844fcd441cadd30adc49738`;
  source archive `602d16565e149ec1d8afd5d2daa15d1790075f35a047dfa014d0624be2152471`.
- The prior packet records exact-head CI, actual review, authorized PR-level rocket and confirmed merge.
  Full-SHA injection with `buildvcs=false` is not independent VCS attestation.

Each run received a separate native-prepared profile, fresh session/source fixture, neutral pinned instructions,
empty native write journal and explicit metadata-only capture. The operator used a clean environment, private
HOME/temp and the existing bounded native seed provisioner. Only scoped deployed-main model references were
privately resolved; production history, memory, browser state and workspace were not copied.

Only native BM25 discovery, `document` and `request_user_input` were admitted. The existing preparation path
preserved native document authority; no new PDF, approval, secret or delivery implementation was introduced.
Native document acquire/inspect/field capabilities and each exact manifest validated before model work.
Preparation and capability support are not evidence the model successfully performed a form operation.

## Frozen Attempts

All times are UTC on October 20, 2026.

| Case | CLI window | Run | Trial |
| --- | --- | --- | --- |
| Correction | 00:18:54-00:19:51 | `fa389b95-8fb3-4c07-8187-573e82bc8859` | `be75b9cc-d2ba-4937-bd37-9702a62cf1d5` |
| Conditional | 00:20:35-00:20:48 | `db12ef42-2b7e-43b7-ab41-039bd47f0faa` | `0d6897c5-a4b0-4b9a-8ed9-d1eb5d9a2fd8` |
| Large | 00:21:34-00:21:46 | `f5ee43c3-56f8-4939-be06-13493d00bac1` | `7fc4546a-a890-4087-a418-7136a1b031e3` |

| Case | Case SHA-256 | Source fixture SHA-256 |
| --- | --- | --- |
| Correction | `41ef1f3066d80a2889fb8164979bfb6bd31b68ba4cc950f6841a47905883f1ba` | `f9100e47a0d3346cf2e7edebce927ffcaa43b3ee4c164186f781b08b93fc94ec` |
| Conditional | `b7c5510bc2f1bd0a705daa6da9c50606b4b1051f8f71c524ab55858309002867` | `f8f72ee3f81c00d4c0c3a529c5789f4891a2df494fe0adbebb8863f8d3668bb1` |
| Large | `f884b25c0affa2c473b7a0c3fe7c3ac59f28e4f1a80a79ffda5e7f832cbe74e8` | `0d179b4cf276c59eef0e960cd1120cfe7924adc13b65a900094fbdad79ace7b5` |

## Usage, Privacy And Cleanup

Correction's canonical native observation covers 15 requests/responses with **170.9k input / 1.6k output** tokens
(raw 170926/1600). Its three returned user calls expose **2.7k input / 59 output** (2749/59). These are separate
role/native scopes, not all-host billing. Conditional and large have unavailable canonical native-model coverage;
their zero-valued counters do not mean no model work or zero cost. Provider-internal attempts, broader all-call
usage and costs remain unknown.

| Owned raw-profile observation | Correction | Conditional | Large |
| --- | --- | --- | --- |
| Files / bytes | 60 / 845870 | 50 / 515386 | 50 / 619815 |
| Authorized document files | 2 | 1 | 1 |
| Public system files | 26 | 26 | 26 |
| Opaque files / known leaks | 0 / false | 0 / false | 0 / false |
| SQLite rows / text / index values | 240 / 624 / 100 | 118 / 330 / 67 | 122 / 330 / 71 |
| SQLite value bytes | 220299 | 132520 | 142069 |

The supplemental literal credential-copy guard inspected 81/71/71 files and 916369/537307/641747 bytes respectively,
with exactly five authorized copies per run and no known literal leak. All fifteen copies were removed and their
paths confirmed absent. Scoped raw/native and literal observations do not certify all encodings, the host or
provider traffic.

Owned gateway PIDs 750986, 754694 and 756117 exited natively with code 0, without force or truncated logs; each was
independently absent afterward. The reports deliberately retain source fixtures and
`owned_fixture_retained; native_settlement_unknown`. No received media was bound to an authoritative PDF result.
Physical shutdown, authorized document-file counts and empty transport state are not logical no-write/output or
complete resource-cleanup proof; do not delete retained evidence to manufacture success.

Production public/security hashes stayed unchanged in every run. Before/after checks found all ten expected
services active, zero failed units and zero ten-minute error entries. Production core stayed at
`cf8bf5dc5beff1a0ef92487077f981e3ca3072ee`. No production deployment, restart, configuration, harness or workspace
change occurred; no rollback is needed.

## Private Evidence

Private roots:

- Correction: `/home/server/mintclaw-eval-pdf-remaining-v1-20261020.ThdFGDr1`,
  profile suffix `eval-70a27ef2-461b-4735-a433-c7a321d38628`.
- Conditional: `/home/server/mintclaw-eval-pdf-remaining-v1-20261020.UuhxPKAk`,
  profile suffix `eval-5276421d-bdf2-4bd9-9a07-9387d8350020`.
- Large: `/home/server/mintclaw-eval-pdf-remaining-v1-20261020.A9bPHX2W`,
  profile suffix `eval-e98a83ca-ebcd-4cc4-a220-8c675bd26a63`.

Canonical run JSON SHA-256, in that order:

- `02f0131a6fdd824fa451e270793f3f6187a70ce759a60404a23e2ddd85a542f8`
- `1292c4eac2b634012f02fe34c693a9c863b93e9160e50320e1a3514eb9b0b4b6`
- `a69d77ed7766b1cb9c1829099f985dc220c62b3760516eeadc0d73c72a1c30ca`

The minimum fourteen corpus cases now each have an actual retained attempt across the pilot ledger, including
failures and inconclusive results. That is attempted coverage, not fourteen qualified successes or complete
PDF/browser support. E5 per-trial resource work and the final requirement-by-requirement exit audit remain mandatory.
Product failures/unknowns are preserved for separate bounded triage, not an
open-ended repair loop inside this eval implementation goal.
