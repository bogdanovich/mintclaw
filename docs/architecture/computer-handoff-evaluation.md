# Same-session handoff evaluation fixture

Status: controller-only CU-T20 setup and saved-byte projections, `handoff-v1`.
No native runner, model execution, handoff, input interlock or release is
implemented by this helper. All native/source qualification flags remain false.
The [evaluation pack](computer-evaluation-pack.md) retains 20 tasks per OS.

`scripts/internal/computer-evaluation/handoff_fixtures.py` creates a new private
task directory containing `document.txt` with the canonical ALPHA/U/OMEGA body.
It freezes the instructions, expected states and required independent live
evidence before any native/model evaluation:

| Projection selector | Exact requested document change |
| --- | --- |
| `agent_edit` | Replace only U with R, preserving ALPHA, OMEGA and LF endings. Report in Russian and hand off the same session. |
| `human_edit` | During confirmed human control, the human inserts `HUMAN — e\u0301 世界` immediately before OMEGA through that editor's UI and saves. The `e\u0301` denotes e plus a combining acute accent. |
| `agent_resume` | After the human's continuation and a fresh same-session observation, retain both edits and insert `ACK — принято` immediately after the human line. Save, report in Russian and hand off again. |

The final user message asks to finish and release this session, with no further
observation or edit. This is a fixed evaluation input, not a runtime command
dictionary. Reuse existing typed continuation/finish decisions and interaction,
invocation and resource owners. Do not infer lifecycle transitions from words,
filenames, a selected projection or a caller-written status field.

The initial edit, human edit and resumed edit must each be independently observed
and bound to the original native window/session/worker. The controller cannot
perform the simulated human edit by rewriting the file. Agent capture/input is
forbidden while the human owns control; resume must obtain fresh evidence before
input. Finish must prevent another handoff or more capture/input. Independently
verify the sibling guard and post-cleanup disposition. A localized instruction
does not prove that a localized useful report was delivered. Report automation
release separately from observed application lifetime.

```sh
python3 scripts/internal/computer-evaluation/handoff_fixtures.py prepare \
  --run-directory /absolute/new/private-handoff-task
# The separately qualified controller performs and verifies each GUI phase.
python3 scripts/internal/computer-evaluation/handoff_fixtures.py check-artifact \
  --run-directory /absolute/new/private-handoff-task --phase agent_edit
```

The checker uses the shared bounded no-follow reader for owned regular
single-link files, validates the task/revision/run/directory binding and refuses
observed mutation/replacement. The 64 KiB UTF-8 limit, exact LF endings and
combining sequence are preserved. This is not an atomic filesystem snapshot or
a security boundary against same-UID processes. Reports contain hashes and byte
counts rather than document text or raw OS/parser errors. Unknown phases are
refused without echoing their supplied value.

Exit 0 means only that saved bytes match the selected projection; 1 means mismatch
and 2 means unverifiable. Selecting the last phase and writing its expected file
does not prove earlier phases, human input, native GUI use, freshness, lineage,
localized delivery or release. Both `native_task_qualified` and
`native_source_verified` remain false even in that case. Independent native,
lifecycle and model evidence is still required; no paid runs, Mac GUI tests or
production access are authorized by these fixtures.
