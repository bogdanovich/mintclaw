# Local Coding Agent P5.2 Workspace Re-Anchoring

Status: implemented

Roadmap packet: P5.2 in
[`local-coding-agent-roadmap.md`](local-coding-agent-roadmap.md)

## Decision

Every coding provider request places the model-generated compacted summary
before the dynamic coding runtime block. That runtime block ends with a fresh,
bounded, deterministic workspace snapshot, making live repository state the
last and authoritative statement about the project.

Personal-agent prompt ordering and summary wording are unchanged.

## Runtime contract

- Coding message assembly refreshes the workspace observer before composing
  the provider request, including the first request after resume.
- The compacted summary is labeled as model-generated historical context, not
  live repository state.
- The summary and runtime snapshot remain separate structured system parts
  with distinct prompt sources.
- Branch, HEAD, status, changed paths, and diff stat come from the observer;
  conflicts with narrative history resolve in favor of that observation.
- Relevant tool writes and `exec` calls retain the existing immediate refresh
  path, while external changes are discovered on the next request or resume.
- Capture errors and unavailable fields are rendered explicitly inside the
  same 24 KiB default snapshot bound. File contents and full diffs remain out
  of the automatic prompt.

The canonical transcript and Seahorse derived state do not store the snapshot.
Compaction therefore cannot freeze repository claims into the authoritative
state: every later provider request re-anchors the compacted context to the
workspace as it exists at that moment.

## Done evidence

Regression coverage changes the branch and creates a dirty path outside
MintClaw between coding prompt builds, then proves that a resumed request keeps
the stale compacted claim earlier and the fresh branch/status/path observation
later. It also verifies distinct summary/runtime structured parts, visible
bounded capture failure, and unchanged personal prompt ordering and wording.
