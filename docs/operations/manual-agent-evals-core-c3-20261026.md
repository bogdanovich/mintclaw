# Core C3 Fixed Qualification

Campaign date: 2026-10-26. Published: 2026-11-01 UTC. This records the final C3 packets from the
[core execution ledger](../architecture/agent-evals-core-execution.md).
All twelve roadmap contracts and their native prerequisites are implemented and
merged. This result record and the three on-demand catalog promotions close
the bounded roadmap; completion is not a claim of a completely green live suite.

## Fixed Results

| Scenario | Qualification | Immutable Three-Trial Batch | Selection |
| --- | --- | --- | --- |
| `general.control.stop` | Fail; execution inconclusive, missing route identity and one unknown native usage | 3 pass | Active/on-demand; failed qualification retained |
| `general.approval.denied` | Pass | 3 pass | Active/on-demand |
| `general.history.restart` | Pass | 3 pass | Active/on-demand |
| `general.skills.selection` | Pass | 3 fail; strict first unselected control | Pending/non-default |
| `general.context.compaction` | Fail; incorrect recall after native compaction | 3 fail; later unknown native usage invalidates shared-run proof | Pending/non-default |
| `general.delivery.final-once` | Not run | Not run | Pending: native durable-delivery mode disables configured streaming |

The five completed campaigns contain **20 trials: 12 pass, 8 fail**. All twelve
passing trials independently passed required native hard checks and a required
tools-disabled judge. The eight failed trials received no judge call. There was
no retry, source/model substitution, oracle relaxation or offline regrade used
to rewrite these outcomes. Promotion follows the predeclared independent 3/3
batch criterion, not a claim that every qualification attempt passed. The
existing six General defaults remain unchanged; specialized profiles stay
on-demand rather than being composed into an unsafe universal profile.

The corpus has 26 definitions: nine active General cases (six defaults and
three explicit on-demand cases), five pending General cases and twelve deferred
PDF/browser cases. No historical case definition, digest or result was rewritten.

## Twelve-Contract Exit Matrix

| Contract | Implementation | Qualification And Selection |
| --- | --- | --- |
| Correction | #1872 | C1 fixed 3/3; active/default |
| Ordinary answer/resume | #1881 | C1 fixed 3/3; active/default |
| Steering boundary | #1892 | C2 fixed 3/3; active/default |
| Steering order/modes | #1892 | Both C2 mode batches 3/3; failed qualification retained; active/default |
| Two-session isolation | #1893 | C2 batch inconclusive after HTTP 429/unknown usage; pending |
| Read recovery | #1895 | C3a batch one pass/two judge provider errors; pending |
| Native stop/reuse | #1898 | C3b batch 3/3; active/on-demand |
| Exact approval denial | #1900 | C3c qualification and batch pass; active/on-demand |
| Final-once delivery | #1902 | Native durable streaming guard blocks paid qualification; pending |
| Owned restart | #1903, #1907 | C3e qualification and batch pass; active/on-demand |
| Compaction | #1911 | C3f fixed campaign fails; pending |
| Pinned skill selection | #1904, #1906 | C3g first unselected controls fail; pending |

Earlier immutable results remain in the [C1](manual-agent-evals-core-c1-20261025.md),
[C2](manual-agent-evals-core-c2-20261025.md) and
[C3a](manual-agent-evals-core-c3a-20261025.md) reports. They were not rerun.
Pending runtime/model improvements require separately authorized future work,
not another automatic attempt in this closed campaign.

## What The Red Results Mean

Stop qualification observed the native abort, but its reuse response lacked
route identity. The shared native receipt retains one unknown-usage call;
judging was refused. Owned shutdown exited zero and privacy was observed. The
retained metadata and finite error-class scan do not identify a finer provider
failure class: do not infer quota, authentication or a timeout. The later fixed
batch on exactly the same candidate passed all three native stop/reuse checks
and judges. The earlier failure remains a failure, not a fourth pass.

Skill selection qualification passed. In its fixed batch, all three actual
selected turns returned `EVAL_SKILL[CEDAR]`; native revision witnesses matched
the pinned skill, and all following unselected controls returned `cedar`.
The first unselected controls instead produced uppercase `CEDAR`, a plan to
read the skill, or pseudo-XML `read_file` metadata. These are emitted text, not
authorized tool executions. All three fail `skill_selection_behavior_invalid`.
This is a strict unselected-control/instruction-following failure, not evidence
of sticky native skill selection or an invented native snapshot. The judge
cannot override it. A future separately authorized investigation can examine
tool-free skill discovery and instruction following; this campaign is closed.

Compaction qualification completed the native eight-message compaction barrier
before recall but returned `c3f-qualification-20261026`, not the corrected marker.
The frozen hard check fails `compaction_behavior_invalid`; no judge was called.
This proves an incorrect recall under this small numeric test profile, not the
exact internal cause of information loss. In the fixed batch, the first two
conversations completed but failed corrected recall. The last recall response
lacked route identity and one call returned unknown usage. The run-wide native
budget therefore seals as unknown and refuses the required proof for all three
trials, including earlier ones. All three remain fail and receive no judge.
There was no quota/authentication/timeout classification in these receipts.
Profile sizes, model, candidate and oracle were not changed after the first call.

Streaming final-delivery is not paid-tested: the native durable transaction
guard intentionally disables streaming in this gateway mode. The harness
does not remove that guard or relabel its nonstreaming model-free control as
streaming proof. PDF and browser cases remain deferred and were not run.

## Immutable Four-Scenario Candidate

- Source: `c769faf3d0e7065908058719133d5aba6fbeb88c`, complete restart/selection
  support reviewed and merged in [#1907](https://github.com/bogdanovich/mintclaw/pull/1907).
- Merge: `e0a558f0b367ca85951b39e85c7afd3059dd179b`. All fourteen exact-head CI
  checks passed; substantive SHA-bound review `6408758499`, owner PR-root rocket
  `597377887`, and no actionable unresolved/new feedback preceded merge.
- Linux AMD64 binary built from a clean Git archive, CGO disabled,
  `goolm,stdjson`, full source SHA in build metadata. No production deployment.
- Binary SHA-256:
  `3b88c3943d7347da976374a6de0ece7f2a11708564433a421097bb8a080880fc`.
- Source archive SHA-256:
  `43a1c977fdb950491cced5418db7451dd68a1f05df657c4b25bd4ea31dfe606a`.
- Corpus archive SHA-256:
  `a84f81ce77006da10d8d0f522e1a9d50591a672745d78cd1422815faf44bc262`.
- Seed helper binary/source SHA-256:
  `cda622a9df5f320f8ff8a2ac78fbb2b5abb78888d56439569a48dc62784b21fc` /
  `124bcd7e9b672a8d62f6a3c84a06998cfb6bd6c2db08e92d4879aaf180a78b7a`.
- Frozen four-scenario plan SHA-256:
  `ad0625f4bd5e0b910650fc2df08dbe5f408ee30da01151c6bc9e5af041819401`.
- Retained metadata projection SHA-256:
  `4a9449a495e7b67d068d414f9845a43efabb4a2dddd47afbcdd452cb2879161c`.

The earlier `25201640a` artifact/plan was preflight-only and never a paid
candidate. The four already-merged contracts were frozen separately on
`c769faf3d`, allowing bounded live work while compaction review continued.
All four case/fixture identities are unchanged between those source trees.

| Case | SHA-256 |
| --- | --- |
| Stop | `f850914757a9ef4cf39d90be44ab4c16dd2a8b40e0b5ab17237800b1b73a0096` |
| Denial | `401acf3c3165d57634fee2a8ec6043e259036c4e371d2ea487ca1bc3eab3f745` |
| Restart | `f7e027e5fb32bcae436453a0635cf10108de07b3e8b7cc8753671fd871a6ea12` |
| Selection | `0ef049e5c35ff9a993f407eb6cc7e384314cbe04f8b2a152ff6f1b53a55417c4` |

## Compaction Candidate

- Source: `46bb85e94268a142ea77d2809f9f990499da7694`, reviewed and merged in
  [#1911](https://github.com/bogdanovich/mintclaw/pull/1911); merge
  `31e4096016d35444952b8f9acf4f5e9927c21b9e`.
- All required exact-head CI checks passed; substantive SHA-bound review
  `6409905701`, owner PR-root rocket `597502472`, no actionable unresolved/new
  feedback and clean mergeability preceded the match-head merge.
- Binary SHA-256:
  `4436c19079eddd1a28665e168cfee0b830a12bce174d1a03744c7e8986b0d471`.
- Clean source archive SHA-256:
  `58989aa341295de736deaf6b432eaffedf016415c77f9b18c50a36007bead817`.
- Corpus archive SHA-256:
  `e4daabd915263ad9d96e34ba155c6445ed8935677b3168a0284c5efd4e2702bf`.
- Case SHA-256:
  `a37fd4638e720c76a7d47c5be3963c201cd62eb032dbfac65d4326a358fbbb4b`.
- Frozen plan SHA-256:
  `5ac7a0b75459a9e909cb2cf3d77707b447040f1c36ef94b7d403a684a6eb14a5`.
- Retained metadata projection SHA-256:
  `4b2773fc41aef2800d287fa63be38a18a6e4e226977d00460929c460349fe167`.

The same clean-archive/Linux build protocol and scoped credential model apply.
The selected native real-CLI repeat-three/frozen-grading control also passed
with this exact binary before paid qualification. Synthetic controls are not
substituted for the four failed live trials.

## Bounds And Accounting

Each scenario received one qualification attempt and one fixed repeat-three
batch, using two fresh synthetic profiles prepared before paid work. Model
target and judge are native Gemini `gemini-3.5-flash-lite`, isolated alias
`core-eval`, resolved from the previously authorized deployed main credential
reference. Only the selected credential was resolved into private secret
files; no production config/workspace/conversation was copied or printed.
Users are scripted; there are no model-user calls.

Each of the first four scenarios' owned runs allows at most 24 logical target calls, 128 KiB serialized
input per call, 2048 requested output tokens, 100k reported tokens and two
in-flight calls. Target turns pin 512 output tokens, 16384 context, one parallel
turn and no fallback. Turns are bounded to two minutes, cases to five minutes,
qualification runs to six minutes and batches to eighteen minutes. Native
request timeout is 90 seconds and configured RPM is three, not a proof of a
shared target/judge/provider quota limiter. A predeclared 65-second pause
separates attempts; it is pacing, not an oracle or retry. Every attempt passed
the 1 GiB free-space reserve before admission.

Compaction uses five scripted inputs, history/fresh-tail budgets 128 tokens,
summary budget 256 and an explicit zero recent tail. Target output is capped at
128 tokens; the shared native budget allows at most 32 logical calls and 4096
requested output tokens per call so auxiliary summaries fit. Its 100k reported
token/two-in-flight bounds and all time limits above are unchanged. Only this
profile retains 64 native trace files; ordinary profiles retain sixteen.

There were **36 admitted/completed logical target calls, 70.1k known reported
target tokens and one unknown-usage call**. Twelve judges returned **9.1k
reported tokens**, with no unknown judge usage. These are not billed totals or
provider-internal retry accounting. Restart totals sum the actual decreasing
generation receipts rather than only the last child. Optional wire-final
usage is absent for denial, and its batch's optional model projection observes
five of six responses; neither replaces its complete known native budget.

Compaction adds **24 logical target/summary calls, 35.3k known reported tokens
and one unknown-usage call**, with no judge calls. Across the five new campaigns:
**60 logical calls, 105.4k known reported target tokens, two unknown-usage calls,
12 judges and 9.1k judge tokens**. Unknown usage is not zero and these totals
do not claim all provider-internal/billed work.

## Retained Runs

Private root on `server@oc`:
`/home/server/mintclaw-core-evals-c3-20261026.uW2o8rDp` (0700). It retains clean
source/corpus archives, verified binaries, plans, seed helper, prepared private
profiles, all stdout/stderr, complete frozen native reports and metadata-only
projections. Failed/incomplete model-free development runs are not paid passes.

| Scenario | Qualification Run | Batch Run |
| --- | --- | --- |
| Stop | `c0e693f8-62a6-43dd-a669-5cb4d06713e9` | `dc94f89b-f2ed-4fa6-be42-1e145de06d25` |
| Denial | `efbf6b3d-2d30-4110-97ac-6044d6646b20` | `a065d11d-b365-4d5a-bd4a-89fd2e2786b3` |
| Restart | `0dbe50f8-5a25-4481-8051-5a59bac59724` | `4debd5ee-eba7-4927-986c-a5db9496a96c` |
| Selection | `f75cca92-573d-48dc-9c9d-1346b617d80d` | `e3952ec7-b550-4ebb-8c9f-cc57e4a16dc6` |
| Compaction | `e0967fa7-136d-4542-9e2e-f752d0a3bb6f` | `4fe6e590-363b-4b6e-a245-25d724f0a27a` |

All actual owned generation receipts record clean zero exits and observed
private surfaces. The conservative `domain_resources_unknown` run label is
not rewritten into a claim of whole-runtime quiescence. Native scoped hard
proofs establish only their declared cancellation, no-effect, history,
selection and delivery boundaries. No production service/config/binary was
mutated by this campaign.

The closing read-only health check on 2026-11-01 found all ten production units
active, zero failed units/errors in the ten-minute observation window and no
remaining process from the private eval root. Main retained PID `2600169` and
start identity `5187294249414`, matching the pre-campaign snapshot. No deployment
or restart was performed to publish this report.
