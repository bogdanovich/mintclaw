# PDF1A Authorized Local Path Admission

## Decision

The existing deferred `document` agent tool may inspect a PDF already present on the MintClaw host. This is a
PDF1A usability follow-up, not a new PDF engine or a generic filesystem capability. The attachment workflow,
document service, one-shot worker, report schema, and model-facing tool remain shared.

A path written in chat is only a selector. It grants no authority. MintClaw first requires the model's inspect call
to repeat one exact local PDF selector parsed from the current user message, then authorizes it through the same agent
workspace/read policy used by first-party local file tools. It opens the admitted file through immutable document
acquisition and replaces the mutable path with a turn-owned opaque source reference. All parsing still happens in
the isolated worker against the immutable snapshot.

## Admitted flow

```text
current user message containing a local *.pdf path
                  |
                  v
       activate the existing PDF skill
                  |
                  v
 document inspect(path) -- the only path-bearing action
                  |
       exact current-message match
                  |
        workspace/read-policy check
                  |
      immutable acquire + worker inspect
                  |
     verify size and SHA-256 while copying
                  |
                  v
 turn-owned media:// snapshot source_ref
          |                         |
          v                         v
 document extract(source)    document render(source)
                  |
                  v
        terminal turn cleanup
```

The source path is resolved relative to the configured agent workspace. With
`agents.defaults.restrict_to_workspace: true`, only paths inside that workspace or paths matched by
`tools.allow_read_paths` are eligible. With restriction disabled, the common file-tool policy permits other absolute
host paths; relative paths still resolve beneath the workspace. Existing tool and skill allowlists remain
authoritative.

## Security and lifecycle invariants

- `inspect` accepts exactly one of `source` or `path`; `extract` and `render` accept only an opaque `source`.
- A local `path` must exactly match one selector parsed from the current user message. MintClaw does not normalize the
  selector into an equivalent alias before this check, so the model cannot invent another in-policy PDF or substitute
  an absolute path for a user-supplied relative path. Paths containing whitespace must be quoted in the message.
- Selector provenance is captured independently of optional PDF skill activation. A missing or profile-disabled skill
  can suppress its prompt guidance but cannot erase the current-message authority needed by an otherwise permitted
  `document` tool call.
- The path must resolve through the common read policy. Document acquisition additionally requires an existing
  regular, non-symlink PDF and rejects directories, symlinks, FIFOs, devices, unsupported bytes, and resource-limit
  violations with typed outcomes.
- Only operator-configured `tools.allow_read_paths` entries extend the workspace boundary. The internal global media
  temp allowance used by generic presentation tools is deliberately excluded; document attachments remain accessible
  only through their owner-bound current-turn refs.
- After successful inspection, the inspected immutable bytes are copied while rechecking their exact size and
  SHA-256. A mutable host path is never reopened by a later operation.
- The generated source reference is bound to the same workspace, agent, actor, route, session, and logical turn. It
  cannot be reused by a different turn and is released at terminal cleanup.
- Existing exact-current-turn attachment references keep their current authority and behavior unchanged.
- Safe document reports contain only state, opaque refs, sizes, digests, page facts, selected pages, artifact facts,
  warnings, and typed failures. They never contain a host path, filename, document bytes, or extracted text.
- A local path in a model-authored tool call is replaced by a one-way digest token in durable tool history. Tool logs
  and ordinary diagnostic content omit it. A turn-start trace retains only input hash and length when the user message
  contains a local PDF path. The PDF skill instructs the model not to repeat the path in its final response.
- Local snapshot storage uses private managed media and deterministic turn cleanup. Failure during copy, registration,
  owner binding, extraction, rendering, cancellation, or cleanup cannot publish a partial source as successful.

## Completion gates

This follow-up is complete only when all of the following are true:

1. Exact current-message relative and absolute selectors, configured allowed paths, and the existing unrestricted
   mode follow the common read policy; an invented in-policy PDF, a normalized alias, traversal, and
   outside-workspace access fail closed.
2. Symlink, special-file, non-PDF, mutation, descriptor mismatch, duplicate-name, cancellation, and resource-limit
   coverage proves immutable acquisition remains authoritative.
3. A real Linux worker inspects a local fixture, returns a temporary source ref, and extracts or renders from the
   admitted bytes after the original path is replaced.
4. Agent discovery activates the existing PDF skill from a local `.pdf` path without eagerly exposing another schema,
   while URL and unrelated turns do not activate it.
5. Durable history, logs, reports, diagnostic traces, artifact metadata, and terminal cleanup pass focused privacy and
   lifecycle tests; exact current-attachment tests remain green.
6. Format, targeted tests, changed-package lint, repository CI, focused review, authorized merge, exact deployment,
   service health, rollback evidence, a synthetic deployed local-path agent smoke, and passive trace inspection are
   complete.

## Stop boundary

This admission does not grant arbitrary filesystem access, filename search, older-attachment lookup, persistent
document jobs, provider-native PDF transport, passwords or crypto, OCR, form filling, XFA mutation, transformations,
companion execution, or macOS runtime support. macOS remains a separately gated parity lane in the PDF roadmap.
