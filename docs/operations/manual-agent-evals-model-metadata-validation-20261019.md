# Owned Model Metadata Admission Validation

## Scope And Merge

[PR #1801](https://github.com/bogdanovich/mintclaw/pull/1801) merged explicit, default-off metadata capture for
fresh owned eval profiles. This is an admission/privacy prerequisite, not a trace collector, live model pilot,
all-call usage census or effective loaded-input qualification. The
[operator contract](manual-agent-evals.md#opt-in-model-metadata-capture) records the exact native limits and exclusions.

- Final head: `d8c468cdef24dadc9ec3ab55555118c65d8377f3`.
- Confirmed merge: `01ad550b19372c5c142149b6c6f4e02043459980`, 2026-10-19 03:38:57 UTC.
- Exact-head CI `39193584099`: ten successful jobs and two policy skips (Frontend and PDFium WASM).
- Actual reviewer comment `6259012924`, writeback `881f1cb84fcfb4967732`, 03:28:43 UTC: prior finding resolved,
  no new must-fix findings. Owner PR-description rocket `578476588` followed at 03:28:45 UTC.
- Ready, clean mergeability and no actionable unresolved feedback were confirmed at 03:38:43 UTC before merge.

One substantive review/fix cycle addressed a real privacy gap. A child could write capture files before readiness
failed, while the old deferred scan depended on a successfully admitted target. The child now retains its validated
owned profile independently of admission; one existing raw scanner covers success, startup failure and deferred
cleanup after stop/reap. A known startup leak returns a static privacy error and incomplete observations, never
fabricated admission or a canonical successful run.

Production scope remained six files in `internal/eval`, +86/-44 versus the first ready head's +56/-25; no runtime
projector, provider, approval or CLI code changed. Much of the initial diff was struct alignment. Two existing
permission-negative test fixtures additionally use explicit `Chmod` so a caller's restrictive umask cannot silently
turn their intended `0644` negative into a valid `0600` file. No production permission rule was relaxed.

## Validation Results

| Observation | Result |
| --- | --- |
| New pre-readiness leak control on old production code | Expected failure, 2.591 seconds; reproduced the review finding |
| Corrected expanded macOS race controls | Pass, 16.685 seconds |
| Replacement-head Linux race controls under `umask 077` | Pass, 23.295 seconds |
| Full Linux `internal/eval` race suite, replacement head | Pass, 747.952 seconds |
| Full Linux gateway client / CLI eval race suites, replacement head | Pass, 2.038 / 22.390 seconds |
| Replacement-head CI macOS Portability | Pass, includes all `internal/eval` tests without race instrumentation |
| Root formatting, affected lint, post-commit formatting/lint, docs lint, diff checks | Pass; zero lint findings |

The final isolated Linux command finished at 2026-10-19 03:25:25 UTC. It used Go 1.26.9, `CGO_ENABLED=1`,
`-race -tags goolm,stdjson -count=1`, a fifteen-minute full-package test limit, an empty private test home and
an explicitly cleared environment. Model calls and production credentials were not used; protocol/process/domain
controls were stubs or loopback fixtures. This is source validation, not native browser/PDF/model qualification.

Preserved non-passes matter:

- The initial Linux full race suite failed in 693.328 seconds on the two mode fixtures under `umask 077`.
  Its client and CLI suites passed in 1.957 and 25.861 seconds. The permission fixtures were corrected before the
  replacement-head run; the old result is not rewritten as a pass.
- Initial local macOS controls passed in 8.527 seconds. Later broad/focused/stripped invocations stayed before
  `main` at `_dyld_start`, with 12 KiB footprint and zero CPU. Only those owned children were terminated after
  sampling. The initial broad client suite passed in 2.019 seconds; interrupted packages are not local passes.
  Corrected focused tests and independent CI macOS coverage are separate observations, not relabeling that attempt.

## Evidence And Operations

Private archives, source copies and logs remain under
`/home/server/mintclaw-eval-metadata-validation-20261019.M993uM85`; final logs are `focused-v2.log` and `test-v2.log`.
The initial `test.log` and source archive are preserved. Archive SHA-256 values:

- Initial: `e0324a715d7ed175282f08d0e7a931a71e8e666545b056952a7e3889a51872f4`.
- Replacement: `a216e9ceeefdffd8e94614d8cbb3638e95c2e6901c7c3cbfdcea27bdb58e01b6`.

These identify operator archive-pinned test source, not an independent native runtime build attestation. Shared Go
compiler caches were used; no production configuration, credential, unit or service was modified by this validation.
Both health snapshots found all ten services active, and the post-run ten-minute error window was empty. Production
core advanced externally from `cf28ed1a071c797fc03aee110ab7adb837cf4937` to
`77c79cd50e61ce50d75b8fe0e4b60a63f56b5fda` during the work; this packet issued no deployment or restart.
About 3.5 GiB disk remained free. Future builds must recheck their disk admission rather than remove shared caches
or another agent's evidence. The validation root itself occupies about 250 MiB and contains no copied production keys.

No production rollback is needed. New profiles can omit the opt-in and keep native capture disabled; never restart a
used profile. Actual trace collection, exact native identity/completeness joins, available all-call observations,
per-trial profile isolation, remaining domain/adaptive cases and the final exit record remain mandatory unfinished
work in the full E0-E5 goal. No live case or historical unknown is newly qualified by these tests.
