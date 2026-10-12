# P8 Local Coding Remote-Task Correlation

Parent packet: [P8 integrated qualification](local-coding-agent-p8-hardening.md#h6--integrated-qualification-production-canaries-and-exit).

## Qualification finding

A fresh local coding thread successfully started and observed one read-only
remote task through `remote_coding_task`. The durable gateway task retained its
task ID and generation; the local parent trace retained the tool call but not
those identities. An exact task selector could inspect the authoritative task
but could not join that parent trace. Successful execution alone therefore did
not satisfy the P8 diagnostic-closure contract.

## Smallest correction

The validated tool-result producer now copies a known, retained task generation
into the existing `ToolResult.Control` identity fields. The existing tool-end
event and diagnostic projector already carry those fields. No new registry,
event family, transcript owner, lookup heuristic or model instruction is added.

An uncertain admission with only a derived task ID does not claim a generation.
Invalid results and unretained admission failures do not acquire a task join.
A later failed/stale control result may retain the previously observed exact
generation, so the operator can investigate its durable owner without replay.

The setter does not mark the local tool call asynchronous or change delivery,
permissions, task execution, history or recovery behavior. Remote task state
remains authoritative in its existing gateway and companion owners.

## Validation and production gate

Regression coverage checks start/status identity, a retained stale control
result, observed/uncertain generations, unknown and denied admissions, invalid
projections and unchanged execution/delivery directives. Existing event and
projector contracts cover transport of those identity fields.

H6 must repeat a fresh read-only task after deployment and prove that an exact
task-plus-generation lookup joins its local parent trace and durable owner.
This record does not claim that production requalification has already passed.
