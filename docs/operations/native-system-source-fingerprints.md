# Native System Source Fingerprints

## Scope

When native diagnostic capture is enabled, a model request can include `system_sources` with scope
`runtime_labeled_system_content_v1`. This identifies filtered content actually present in runtime-labeled system
sections immediately before the caller-mediated provider boundary. It does not read workspace files afterward,
reconstruct a prompt from a transcript or infer instructions from model output.

The three groups are workspace instructions (`workspace.definition`), the rendered skill catalog (`skill:index`),
and active skill instructions (`skill:active`). Each group retains its ordered content fingerprint and section
count. The source is the actual request after runtime assembly, media/candidate shaping and hook processing, not a
new filesystem inventory. Provider-specific serialization remains outside this observation.

This native producer is a prerequisite for eval evidence. The existing eval report projection does not yet retain
these source groups; its six aggregate request hashes and instruction/skill unknowns remain unchanged until the
consumer is implemented. No historical report or trace is backfilled.

## Assembly And Cache

Runtime prompt assembly retains internal byte ranges and source labels alongside system blocks. The default cache
publishes cached text and its source ranges under the same existing lock/generation checks, returning detached
copies. Both automatic source-file invalidation and explicit cache invalidation refresh the pair. A cache hit does
not reread files to guess which content produced an older prompt.

The established text, separators, system-block grouping, cache controls and provider JSON do not change. The
default static prompt still occupies one provider block. Internal ranges are omitted from JSON, stripped from
provider-visible hook clones and canonical comparisons, and deep-copied with requests. A hook gets no authority to
invent provenance; ranges are restored only for unchanged block type/text. Rewritten/appended unstructured blocks
make source coverage unavailable, even if an old label remains at the same position.

## Results And Bounds

- `observed`: every system block has complete, contiguous runtime ranges, valid text/placement and a matching
  whole-message representation. Group hashes cover the ordered, filtered decoded section strings encoded as JSON.
  A group with zero sections has the explicit hash of `[]`; this is observed absence in this scope, not unknown.
- `unavailable`: protected system evidence, missing/unstructured metadata, invalid ranges/placement, a whole-message
  mismatch or more than 64 system source sections prevents qualification. Only a bounded static reason is emitted;
  no partial hashes or raw producer error are substituted for complete coverage.
- Absent `system_sources`: capture was disabled or the producer did not supply this version. Absence is unknown,
  never an observed empty group. Native legacy payloads remain readable.

Known configured credentials are filtered from decoded section strings before JSON encoding/hashing, including
quotes and backslashes. Existing native protected-evidence classification can withhold the complete system-source
witness. No source text, file paths, skill names, arguments or private values are added to diagnostic payloads.
Malformed producer fingerprints are replaced by static unavailable metadata rather than copied into traces.
Capture-off requests do not compute these hashes; internal source metadata does not alter model-visible content.

The optional fingerprint is metadata, not approval, policy authority or a cryptographic authenticity certificate
for operator-edited files. A matching filtered hash is not proof of identical unredacted content. These hashes do not
attest individual skill manifests/helper files, all user/system skill roots, instructions in historical transcripts,
provider wire bytes, hidden retries, compaction, delegation or every model call. Catalog content may remain unchanged
when an unused skill's implementation changes; the witness must not invent drift that was absent from the request.

## Validation

Deterministic controls cover the collapsed cache, automatic/explicit invalidation, concurrent/detached reads,
separate workspace/catalog/active drift, decoded credential filtering, UTF-8 byte ranges, established renderer/JSON
compatibility, hook rewrites, missing/protected/contradictory/oversize metadata, legacy absence and invalid frozen
fingerprints. A normal native AgentLoop turn additionally checks that its request event carries an observed
witness. These are source/contract tests with stub providers, not live eval or full E0-E5 qualification.
