# Core C2 Fixed Qualification

Date: 2026-10-25. This closes the fixed C2 campaign in the
[execution ledger](../architecture/agent-evals-core-execution.md), not C3 or the
overall goal. All outcomes are retained; no attempt was rerun.

## Result And Selection

| Scenario / native mode | Qualification | Fixed batch | Selection |
| --- | --- | --- | --- |
| `general.steering.boundary` / one-at-a-time | Pass | 3/3 pass | Active/default |
| `general.steering.order` / one-at-a-time | Fail: HTTP 429, unknown usage | 3/3 pass | Active/default |
| `general.steering.order` / all | Pass | 3/3 pass | Active/default |
| `general.session.isolation` / one-at-a-time | Pass | 3 inconclusive, 0 pass | Pending/non-default |

The predeclared promotion criterion was an independently hard-checked and judged
3/3 batch per scenario/mode; order required both modes. Its failed qualification
is not erased or relabeled. The full campaign is **12 pass, 1 fail, 3 inconclusive**;
the fixed batches alone are **9 pass, 3 inconclusive**, not an all-green C2 suite.

There are now 19 definitions: six active/default General, pending isolation,
six pending PDF and six pending browser. Original definitions/digests, C1 results
and the red [fourteen-case baseline](manual-agent-evals-baseline-20261024.md)
remain unchanged. The combined six-default suite was not paid-rerun here.

## Immutable Candidate And Bounds

- Source: `49f7fc7e6ac8cbf5b23128b8d99f05eb7b5411ed`, reviewed/merged in
  [#1893](https://github.com/bogdanovich/mintclaw/pull/1893), merge
  `08918db1307b96a17ad5decb46088e89bf3067cc`.
- Native fixture/profile/steering prerequisites: #1890, #1891 and #1892. The final
  isolation head had all 14 CI checks green, substantive exact-head review, owner
  PR-root rocket and no actionable unresolved/new feedback before merge.
- Linux AMD64 binary built locally from a clean source archive, `CGO_ENABLED=0`,
  `goolm,stdjson`, full source SHA in build metadata. No server build/deploy.
- Binary SHA-256:
  `6c22f343c056951d96651ce89cef7261cde5f42a5026330429fa5b4efee7ead7`.
- Source archive SHA-256:
  `f4673076f9ea79749c7e5a78da1174f80b07680b23fec7d9f13694216a282b3c`.
- Seed helper binary SHA-256:
  `d68095fba836f92ddeda1dc922be013f4f3e276703a4cb136a8ef21b99d100be`;
  retained helper source SHA-256:
  `58dcef89ae9858952fadc78692a0575d135cbe8a144f13ae7739adf2abdd6bcf`.
- Target and judge: isolated native Gemini `gemini-3.5-flash-lite`, using the
  previously authorized deployed model reference through native secret resolution.
  No key values, production conversations or production config were copied into
  this document. No LLM-user calls.
- Eight fresh prepared profiles allocated before any paid call: one qualification
  plus one serial three-trial batch for each of four variants. Same candidate,
  case digests, prompts and model throughout; no switches after failures.
- Each owned run: maximum 24 target calls, 131072 serialized input bytes, 2048
  requested output tokens, 100k reported tokens and two in-flight calls, enforced
  by the shared native budget. Conversation limit five minutes, turn two minutes;
  suite limit six minutes for qualification, eighteen minutes for a batch.
- Native capacity pins: one for boundary/order, two for isolation, at both existing
  native config locations. Fixture intrinsic hold limit 45 seconds. One judge per
  eligible trial; no judge may override missing hard evidence.

Case SHA-256 identities:

| Case | SHA-256 |
| --- | --- |
| Boundary | `a362d984dccc1001d5bbfad64879517943981263a09b9fb6b04447818f47c170` |
| Order | `2718aa487cc2523a8e3c0b935e5d812c8814ef85c30cd399f7865be580cc7442` |
| Isolation | `e7e56af3cfbb07162e6f468adf2d164a3050e462c1f3252fac78d0f9f817cfb2` |

## Retained Runs

Private artifact root on `server@oc`:
`/home/server/mintclaw-core-evals-c2-20261025.cDhCKwhA` (0700).
It retains `qualification-plan.json`, `qualification-result.json`, helper source,
verified binaries/corpus, prepared profiles, eight stdout/stderr pairs and complete
private reports/native observations. Public records contain metadata only.

| Attempt | Run ID |
| --- | --- |
| Boundary qualification | `87ed4afd-6429-4f6c-98de-470d423ff039` |
| Boundary batch | `cf67f229-26ee-4b24-a5d2-e40f5dcb06be` |
| Order one-at-a-time qualification | `dd864ff0-4065-48d2-904c-13c3fe6906a4` |
| Order one-at-a-time batch | `306f039f-a007-40c4-b733-624baea0d70f` |
| Order all qualification | `38edd5e0-58e7-491a-80c7-dd30d4a476c8` |
| Order all batch | `d58f0d21-a8fa-4934-9fd9-56c34dae2647` |
| Isolation qualification | `a8d1f37e-6f8d-4899-8b2d-ef3d7565dc28` |
| Isolation batch | `40930737-7d61-453c-a8c6-0554aa913766` |

## Evidence And Blockers

All 12 passing trials have eligible evidence, all required hard checks passing,
observed native steering proof and a passing judge. Isolation qualification also
has observed peer proof and fixture ordering `[1,2,4,5]`: A entered, B entered,
B settled, A released. Each scope retained its own history/trace/delivery identity.

The order qualification's initial turn returned the final corrected marker, but
its follow-up hit Gemini HTTP 429 (`rate_limit`, retry-after 28 seconds) without
usage. Native budget stopped at `unknown_usage`; the error final lacked normal
route identity. The trial remains fail/ineligible with no judge. This is not
evidence that FIFO steering itself failed, nor permission to call it a pass.

Isolation batch trials 1/2 completed their six-call conversations with enclosed
reads (`[1,2,4,5]`, `[7,8,10,11]`). Trial 3's peer follow-up hit another Gemini
HTTP 429 without usage. That one shared run-wide budget became unknown, so all
post-stop hard assessments remained unavailable and all three judges were refused.
The third held read was canceled during owned cleanup; no sequential substitute
or partial two-trial promotion was used. Isolation remains pending. A later,
separately authorized campaign needs available provider quota and known usage;
this campaign does not expand into quota/runtime repair or retry-until-green.

There were 58 admitted logical target calls (60 nominally planned), **146.1k known
reported target tokens plus two calls with unknown usage**. Twelve judges used
11.0k input / 1.1k output, 12.2k total reported tokens. These are not billed-token
or provider-internal retry totals. Optional model telemetry is narrower: isolation
projects the main scope, while the independent native budget covers both scopes.

All eight native children exited zero, with fixture hosts closed, untruncated
logs, settled/sealed budgets and observed leak-free private censuses. Usage was
unknown in the two failed/unavailable runs; clean cleanup does not make them pass.
The main production PID stayed `911387` with unchanged start identity; no services,
production files or deployment were changed. Free space remained above the 1 GiB
pre-attempt reserve (about 1.5 GiB after the campaign).

## Next

Default General runs now need an admitted active-tool profile in one-at-a-time
mode, plus native `bounded_calls`, metadata capture and a judge. Explicit order
`all` remains a separately qualified profile variant. Pending isolation needs its
two-turn pin and explicit opt-in; domain suites remain deferred. Continue C3's
seven contracts, not additional C2 paid attempts or production deployment.
