# PDFI4 Inbound Version Prerequisite

## Checkpoint And Scope

Protected free-text ingress needs a durable, value-free route guard before accepting an utterance. A replay after
closing or deleting its PDF job must not fall into ordinary conversation. The existing inbound queue is the right
place for this guard, but its reader currently ignores the record version. Simply writing a newer version would
not make an older reader refuse it.

This small prerequisite validates the existing version on read and write. Current version-1 records remain
readable; missing, retired, and future versions are rejected without returning message content or rewriting the
record. It does not introduce a new format, route guard, document UI, model call, store, or message admission path.

Options considered: silently ignore future metadata (unsafe for protected replay); encode the guard in retired
interaction keys (couples unrelated protocols); or establish normal format validation first. The last is chosen.
The generic queue owns format compatibility; the later document job and trusted ingress still own authorization.
This packet is independent of the private-provider PR and follows the merged native-intake prerequisite.

## Validation And Later Work

Regression tests cover unsupported versions, restart replay of the supported version, no content returned or echoed
on refusal, and unchanged records after failed persistence/release/failure/write operations. Normal bus and race
tests remain required.

Later ingress work must add the route guard, persist it before native admission, prevent clearing/rebinding it, and
intercept guarded replay before ordinary history. It must also introduce an explicit supported format for guarded
records and test its compatibility boundary. A rollback binary predating this validation is not safe for queues
containing that future format: drain those records or use a version-aware rollback build. No editable-hybrid backend
or PDFI4 completion claim is introduced here.
