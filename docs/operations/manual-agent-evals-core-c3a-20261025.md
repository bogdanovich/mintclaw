# Core C3a Fixed Qualification

Date: 2026-10-25. This records the read-only recovery packet in the
[execution ledger](../architecture/agent-evals-core-execution.md), not completion
of C3 or the overall goal. One qualification and one fixed three-trial batch were
run; no attempt or judge was retried.

## Result And Selection

| Scenario | Qualification | Fixed batch | Selection |
| --- | --- | --- | --- |
| `general.tools.recovery` | Pass | 1 pass, 2 errors (`judge_call_failed`) | Pending/non-default |

All four native target conversations and every required hard check passed. Two
batch judge calls returned `provider_unavailable` without a response or usage.
That is not a semantic rejection, but it is not a passing judgement either. The
predeclared independent hard-check-plus-judge 3/3 criterion was not met. The full
campaign is **2 pass, 2 error**, not a green recovery baseline.

The role receipt retains no finer provider error class. The isolated stderr has
no HTTP 429, resource-exhaustion or timeout evidence; do not infer quota, a local
limiter, an authentication error or a timeout from the generic code. A later,
separately authorized campaign needs an available judge and known returned usage;
sanitized provider-error classification would improve diagnosis. Neither missing
evidence nor a later offline regrade may rewrite this fixed campaign as 3/3.

There remain 20 definitions: six active/default General, pending isolation and
recovery, six pending PDF and six pending browser. Even if later qualified,
recovery's specialized read-only profile remains on-demand/non-default absent an
explicit profile-composition decision. No combined default run or deferred domain
qualification was performed. Original/C1/C2 definitions and results, including
the red [fourteen-case baseline](manual-agent-evals-baseline-20261024.md), are unchanged.

## Immutable Candidate And Bounds

- Source: `2853ede5aaed00d970ebb8d35b330f111b22e31c`, reviewed/merged in
  [#1895](https://github.com/bogdanovich/mintclaw/pull/1895), merge
  `b6c0f768957653b6d99ad144fd71fa562b690f6c`.
- Before merge: all 14 CI checks green, substantive exact-head re-review, owner
  PR-root rocket and no actionable unresolved/new feedback. The early-marker
  oracle fix has two negatives that failed on the old oracle and passed after
  the fix; native recovery tests and race history controls passed locally.
- Linux AMD64 binary built locally from a clean source archive, `CGO_ENABLED=0`,
  `goolm,stdjson`, full source SHA in build metadata. No server build/deploy.
- Binary SHA-256:
  `9c41ae16e711d37ff3f6a2d30c0059d28f7a5814532ae1283310e3a53ca4bc36`.
- Source archive SHA-256:
  `ac6e242f093e727f679989c1b12e144e971aa2f5c531c4b79cc79890fd68e727`.
- Corpus archive SHA-256:
  `6d8a1d52381a8f622daff5873d358654d599fc42053666dbb9fb2e58e669a4f6`.
- Retained seed helper binary/source SHA-256:
  `7eddb5ac0de796ef19b712bb709cc797345508529e5dcea61fda4e25cbcfbfd7` /
  `2b5d609320e6e974d80cb35be8d3e4ac9700ebdc960b82279438dc068dabcf35`.
- Case SHA-256:
  `58906a951c87734b42ef35f3952d90209ee8c2aa3e2fdea2ea013e8ecfb7ae48`.
- Fixture SHA-256:
  `8a0c13cee498b7b386c2f2ee27c1deb803d314f9190eb6f3ddd62923f07dcbf8`.
- Plan SHA-256:
  `c0a346489a2cff32179a6e1a484fef579c3e1b4a82b4390b53f3e3245f6827ad`.
- Target and judge: native Gemini `gemini-3.5-flash-lite`, isolated alias
  `core-eval`, using the previously authorized deployed reference through native
  secret resolution. No production conversations/config were copied. No key
  values are in this report; no LLM-user calls.
- Two fresh profiles prepared before paid work. Three scripted inputs per
  trial, only `read_file` allowed, pinned synthetic fixture, no question/MCP/
  write/exec/browser/PDF authority. One judge per eligible trial.
- Each owned run: at most 24 target calls, 131072 serialized input bytes,
  2048 requested output tokens, 100k reported tokens and two in-flight calls,
  enforced by the shared native budget. Two-minute turns, five-minute case,
  six-minute qualification/eighteen-minute batch suite bounds.
- Isolated config pins native RPM 3, 90-second provider request timeout, one
  parallel turn, 16384 context and four tool iterations. Configured RPM is not
  evidence of a shared target/judge provider-quota limiter. A predeclared pause
  of at least 65 seconds separated qualification and batch, not an oracle/retry.

## Retained Evidence

Private root on `server@oc`:
`/home/server/mintclaw-core-evals-c3a-20261025.kgYhOSic` (0700). It retains the
plan/result metadata, seed helper source/binary, verified candidate/corpus,
prepared profiles, stdout/stderr and complete private native reports. Superseded
preflight binaries were never live candidates. Public records contain metadata only.

| Attempt | Run ID | Result |
| --- | --- | --- |
| Qualification | `cf5354d5-0b31-45b5-883d-1a3f4196828d` | Pass |
| Fixed batch | `7d5c6e4e-c3a3-43a6-9e91-206d6f24e783` | Pass, error, error |

The four native histories contain exactly one failed missing-path read, one
successful corrected read and same-session recall. Before/after fixture hashes
and complete one-file censuses agree. No early invented marker, extra read,
orphan result or absent continuation was accepted. All four trials have observed
recovery proof, eligible evidence and all required hard checks passing; only two
have a completed passing judge. The other two preserve unknown judge usage.

There were **20 admitted/completed logical target calls, 42.2k known reported
target tokens**, no unknown target usage and no in-flight calls at shutdown.
Four judge calls were attempted: two returned 1.6k input / 251 output, 1.8k total
known reported tokens; two returned no usage. These are not billed totals or
complete provider-internal retry accounting. Optional native model telemetry
keeps its narrower `all_call_coverage: unknown` contract.

Both owned children exited zero without forced termination or truncated logs;
their budgets were sealed/settled and private censuses observed no leak. The
production main PID stayed `911387`, start identity `5106452847193`; no production
service/config/deploy changed. Each attempt passed the 1 GiB free-space reserve.
Continue the six remaining C3 contracts, starting with native stop/reuse, not
additional recovery attempts or provider/runtime repair expansion.
