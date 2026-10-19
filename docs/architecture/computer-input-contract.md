# Bounded computer-input contracts

Status: side-effect-free foundation only. No input driver, host, companion
command, public tool or grant is registered by these types. Existing computer
capabilities remain read-only. This independent slice does not depend on the
private Linux Unicode-driver candidate.

## Shapes and protected payload

`InputAction` is a tagged union with exactly one arm matching its kind:

| Kind | Shape boundary |
| --- | --- |
| `click` | Exactly one host-issued element reference or original-image pixel point; named button and one/two clicks. |
| `insert_text` | Host-issued element reference plus nonempty separate protected UTF-8 text. |
| `replace_text` | Host-issued element reference plus separate protected UTF-8 text; explicit empty replacement is distinct from a missing payload. |
| `key` | A bounded named non-text key or modifier shortcut, with canonical unique modifiers. Plain printable text must use the protected text route. |
| `scroll` | A bounded nonzero pixel delta at a valid original-image point. |
| `drag` | Two distinct original-image points, named button and bounded duration. |

No action contains a raw native PID/window/display, application name, filesystem
path, arbitrary command, unbounded action batch or standalone held-key/button
operation. Element references are host-issued observation-local IDs, not vendor
tokens, labels, native accessibility paths or an authority source.

Text bytes are excluded from ordinary JSON and fixed `String`/`GoString`
diagnostic projections. A metadata-only round trip cannot validate as a text
request without an explicit ephemeral payload. This is not an encrypted secret
container or a guarantee against a trusted caller deliberately reading/logging
its in-memory bytes. Future integration must use the existing node ephemeral
envelope, bound payload commitment and protected argument/result projections;
it must not serialize text into a durable execution plan or trace.

## Evidence and authority

`InputEvidenceFromObservation` derives payload-free metadata only after the
source observation passes the existing validation. `Observation` and
`InputEvidence` share the same metadata validator for original full principal,
session, worker, policy, control generation, target/window incarnation, geometry
and host-clock capture/expiry bounds. Native input points use that original pixel
geometry, including Retina scales and negative desktop origins.

`InputRequest.Validate` checks the exact evidence ID/binding, action shape,
payload presence/encoding/byte bounds, agent control and freshness. A declared
drag must fit the remaining evidence lease. It does not authorize a model-authored
copy of evidence: the host must look up its own current record for the request's
original owner and consume/fence evidence before any dispatch.

Host-issued references, typed key names and model-declared effects are not proof
that an operation is harmless. For example, a shape-valid shortcut is not a
clipboard grant. Backend support, capture/transmission policy, effect approval
and any provider-required confirmation remain independent checks.

## Required execution integration

Before exposing input, subsequent slices must provide:

- Qualified exact native targeting and immediate geometry/focus revalidation;
  no geometry/title/PID-only fallback under an unproven narrower grant.
- Authoritative OS-desktop identity and local cross-process arbitration shared
  across profiles. The existing read-only in-process arbiter does not establish
  this guarantee.
- The existing durable invocation/ephemeral channel, full principal and
  generation fencing, cancellation/emergency stop, held-state cleanup and no
  blind replay of dispatched or uncertain effects.
- Bounded execution budgets and independently verified effect/post-action
  observations. A driver acknowledgement or changed pixels alone do not prove
  the requested business outcome.
- Typed same-resource human-control interlocks and latest-intent finish before
  any observation/input resumes, without finish-word dictionaries.
- Strict wire schemas and unknown-field rejection at the existing node/public
  tool boundaries; these Go validators are not a replacement parser or policy.

No legacy read-only API is widened by this foundation. The full requirements and
remaining native/model/pilot gates are tracked in the
[computer-use roadmap](computer-use-roadmap.md).

The separate [native ownership primitive](computer-input-ownership.md) starts
OS-local cross-process exclusion on Linux X11. It is not an executor or grant,
and its loss notification is not a held-key/button cleanup receipt.
