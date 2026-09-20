# PDF4A XFA Feasibility And Admission Goal

## Status

Admitted for bounded qualification only. PDF0A through PDF3 are complete prerequisites on `linux/amd64`.

PDF4A does not ship, enable, or advertise XFA mutation. It decides whether one precisely declared XFA subset has a
viable open-source implementation path inside MintClaw's existing document architecture. Its correct terminal result
may be `detection-and-refusal-only`.

## Operator outcome

At the end of PDF4A an operator and maintainer can answer, with reproducible evidence:

1. which XFA representations and rendering modes MintClaw detects today;
2. whether any pinned backend can safely mutate one declared subset;
3. whether an independent XFA-capable renderer proves that the requested value is visibly present after mutation;
4. which scripts, actions, layout features, security states, and platforms remain unsupported; and
5. whether a later PDF4B implementation goal is justified.

The decision is binary:

- `supported-subset-candidate`: one bounded subset passes every required qualification gate and receives a separate
  PDF4B implementation goal; or
- `detection-and-refusal-only`: no candidate passes, so production mutation remains unavailable and existing typed
  refusal is the supported behavior.

Dataset mutation without independent visible-render proof is always `detection-and-refusal-only`.

## Existing boundary to preserve

PDF4A begins from these shipped guarantees:

- `pkg/document` is the sole owner of document semantics and worker execution;
- inspection recognizes catalog-reachable XFA in stream or packet-array representation, reports static, dynamic, or
  unknown rendering when proven, and applies a bounded decoded-payload limit;
- PDF1A rendering refuses every XFA document instead of treating Poppler output as authoritative;
- PDF2 AcroForm fields/fill refuse pure and hybrid XFA instead of falling through to ordinary form handling;
- untrusted input is parsed only in the existing one-shot document worker with bounded input, output, time, memory,
  descendants, cancellation, and scratch cleanup;
- the public CLI and deferred agent tool call the same document service; and
- PDF3 protected values, approval, journal, artifact, outbox, and privacy contracts cannot be weakened by an XFA
  backend.

PDF4A may add qualification fixtures, disposable harnesses, and evidence. It may not add a daemon, broker, permanent
browser session, second document tool, parallel writer, alternate delivery path, or production backend.

## Candidate questions

Every candidate is evaluated for a specific role rather than as a general "PDF library":

| Candidate family | Qualification question | Not assumed |
| --- | --- | --- |
| `pdfer` | Can a pinned Go package parse XDP packets/schema and update datasets with typed XML-safe serialization? | Layout, scripting, rendering, flattening, or production maturity |
| `pdf-xfa-tools` | Can its declared XFA extraction/update operations be reproduced on licensed fixtures, and what XFA subset do they actually preserve? | Independent visible rendering or dynamic layout |
| `pikepdf`/qpdf | Can packet-level inspection, replacement, structural checks, and source/output identity support a qualification oracle? | XFA semantics, layout, appearance generation, or safe script execution |
| PDF.js | Can a pinned build render the declared static subset deterministically with scripting/actions disabled and bounded resources? | Reliable XFA editing/save or authoritative dynamic behavior |
| XFA-enabled PDFium | Can a pinned open-source build render the declared subset headlessly with a bounded embedder boundary? | Stable public form-edit API, lightweight packaging, or acceptable sandbox cost |
| Commercial/Acrobat-compatible adapter | Is an optional licensed integration architecturally possible without becoming a silent or required dependency? | Open-source qualification or default availability |

The research records repository/release identity, license, maintenance state, supported language/runtime, packaging
size, startup behavior, static/dynamic/foreground claims, scripting behavior, mutation semantics, rendering behavior,
Linux feasibility, macOS feasibility, and integration cost. Claims require primary project documentation, source, or
release evidence.

## Candidate subset definitions

PDF4A evaluates static XFA first. A candidate static subset must be stated positively and must exclude everything not
proven. The initial candidate may include only documents that satisfy all of these facts:

- catalog-reachable XFA packet stream or well-formed name/stream packet array;
- complete template and datasets packets with a deterministic field/data binding;
- static rendering explicitly declared or independently proven;
- fixed page count and fixed widget/layout geometry;
- no FormCalc, JavaScript, event, submit, URL, file, attachment, launch, import, or external-data action;
- no repeating subforms, page growth, flowed dynamic layout, locale-dependent calculation, or unresolved font;
- no encryption, signature, certification, timestamp, Reader Extensions, DocMDP, FieldMDP, or hybrid AcroForm
  authority conflict; and
- all requested values fit the declared XML/schema types and visual bounds.

Static, hybrid, foreground, dynamic, script/action-dependent, repeating-group, page-growth, signed/restricted,
encrypted, malformed, and over-limit cases remain separate fixture classes. Passing one does not grant authority for
another. Dynamic or foreground support requires a later separately frozen subset even if a renderer can display a
sample.

## Fixture and provenance matrix

Qualification uses only deterministic MintClaw-generated fixtures or third-party fixtures whose redistribution and
test use are documented. No personal, government, tax, immigration, legal, medical, financial, or customer document
may enter the repository or PR evidence.

The matrix must cover at least:

| Class | Required assertion |
| --- | --- |
| Ordinary AcroForm | Existing PDF2 path remains selected and unchanged |
| Static XFA-only | Exact representation/rendering facts; candidate mutation and visible proof or explicit no-go |
| Static hybrid XFA/AcroForm | Never falls through to AcroForm; authority conflict is explicit |
| Foreground XFA | Detected when distinguishable; no inferred static authority |
| Dynamic XFA | Declared dynamic and refused unless separately qualified |
| FormCalc and JavaScript | Presence detected or conservatively unknown; execution disabled and mutation refused |
| Submit/URL/file/launch/import action | No network, host file, process, credential, or environment access |
| Repeating group/page growth | Refused unless layout and page-count behavior are independently proven |
| XML metacharacters and Unicode | Typed round trip with no entity injection, truncation, or normalization drift |
| Malformed/truncated/oversized packets | Bounded typed failure with cleanup |
| Signed/restricted/encrypted | Inspection-only typed refusal |

Each fixture record includes a stable ID, filename, SHA-256, source or generator, license, XFA representation,
expected rendering mode, packets/features present, allowed operation, expected result, and evidence command.

## Independent visible-render gate

A candidate mutation passes only when all of the following are true:

1. source bytes and digest remain unchanged;
2. the output is a new deterministic operation artifact with a recorded digest;
3. XDP/XML is parsed and serialized without string substitution or unsafe entity expansion;
4. packet structure and expected dataset value round-trip through an independent structural reader;
5. an XFA-capable renderer independent of the mutator renders every affected page after the edit;
6. the expected text or state is visible in the intended field/region, with no blank page, clipping, overlap,
   displaced layout, or unexpected page-count change;
7. a before/after oracle distinguishes the intended visual change from unchanged background content;
8. a second ordinary PDF viewer can display any flattened candidate output, but cannot substitute for the
   XFA-capable oracle; and
9. failure at any stage publishes no final artifact and cannot be reinterpreted as success.

Authoritative goldens are acceptable only when their generating XFA renderer/version and licensing are pinned and the
golden binds the exact fixture/output digest. A screenshot from an uncontrolled desktop viewer is not sufficient.

## Security and isolation gates

Qualification runs in disposable environments and does not install permanent production dependencies. Untrusted
document handling must reuse the existing one-shot worker ownership model or prove a replaceable external sandbox
primitive; PDF4A does not implement custom namespaces, seccomp, container orchestration, or a general sandbox manager.

For every executable candidate:

- network is denied or demonstrably unused;
- arbitrary host filesystem, process launch, environment, credential, clipboard, and UI access are denied;
- XML external entities, DTD fetching, schema fetching, and decompression expansion are disabled or bounded;
- FormCalc, JavaScript, document actions, submit, import, and external data are disabled by default;
- input, decoded packets, XML depth/nodes, pages, pixels, memory, CPU/wall time, descendants, output, and logs are
  bounded;
- cancellation terminates the full process group and cleanup removes private scratch deterministically; and
- backend diagnostics are bounded and cannot retain document content or secrets in ordinary logs/traces.

If safe disablement cannot be proved, the affected class is unsupported. A backend's claim that scripts are normally
unused is not a security guarantee.

## Architecture decision rule

The architecture review compares these paths:

1. keep `detection-and-refusal-only`;
2. add one replaceable dataset mutator behind the existing `pkg/document` backend registry and verify it with a
   separate renderer;
3. use one renderer only as a qualification oracle, not a production dependency;
4. expose an optional licensed adapter without making it the default or silent fallback; or
5. defer until upstream tools expose a stable, packageable, sandboxable interface.

`supported-subset-candidate` is allowed only when the selected design keeps `pkg/document`, the current worker,
operation journal, artifact transaction, CLI/tool surface, PDF3 approval, and outbox ownership intact. A solution that
needs browser-authoritative DOM filling, desktop automation, keystroke injection, an always-on browser, another
service/tool, arbitrary shell commands, unsafe script execution, or a second completion/delivery state machine is a
no-go regardless of fixture success.

## Pull-request sequence

Use one dedicated autonomous worktree. Each dependent PR starts after its predecessor merges.

1. **Admission:** this goal plus roadmap status. Documentation only; no capability claim.
2. **Qualification harness and fixtures:** reproducible pinned candidate setup, provenance manifest, synthetic fixture
   extensions, current-refusal baseline, mutation/render experiments, bounded cleanup, and machine-readable results.
   Production runtime remains unchanged.
3. **Evidence and architecture decision:** source-cited comparison, executable results, security/packaging analysis,
   selected terminal decision, and architecture review.
4. **Next boundary:** if and only if the decision is `supported-subset-candidate`, merge a separate PDF4B
   implementation goal with its exact production behavior, dependencies, migrations, tests, deployment, and rollback
   gates. Otherwise update the roadmap to `detection-and-refusal-only` and name the missing evidence required to
   reconsider it.

A prerequisite exposed by executable evidence may become a smaller PR, but it may not broaden product behavior.
Four substantive review/fix cycles or growth outside the qualification/document-test boundary triggers the autonomous
architecture checkpoint.

## Completion criteria

PDF4A is complete only when:

- the admission, qualification evidence, architecture decision, and roadmap update are merged;
- every technical, license, release, and capability claim is linked to a primary source;
- every fixture has documented provenance, license, SHA-256, expected classification, and evidence command;
- current XFA detection/refusal and ordinary AcroForm behavior remain executable and green;
- each candidate has an explicit qualified, rejected, oracle-only, or deferred result with a bounded reason;
- any candidate supported subset passes source-unchanged, XML-safe mutation, independent structural readback,
  independent XFA-visible render, malicious-action denial, limits, cancellation, and cleanup gates;
- static, hybrid, foreground, dynamic, script/action, repeating/page-growth, malformed, signed/restricted, and
  encrypted classes have explicit outcomes;
- the architecture review confirms that no parallel document control plane or unsafe runtime dependency is needed;
- the roadmap records either `supported-subset-candidate` plus the exact PDF4B boundary or
  `detection-and-refusal-only` plus the reconsideration evidence; and
- disposable environments, generated outputs, renders, caches, and staging are removed after evidence capture.

No production deployment is required when PDF4A changes only documentation, qualification scripts, manifests, and
test fixtures. Any unexpected runtime change expands the completion gate to normal production deployment, rollback,
health, live smoke, trace, privacy, and cleanup proof.

## Hard stops and non-goals

Stop with `detection-and-refusal-only` when no pinned backend can both mutate the declared subset and produce
independently verified post-edit rendering inside the mandatory security and supported-platform bounds.

Out of scope are production XFA fill/flatten support, dynamic XFA without a separately proven subset, execution of
PDF JavaScript or FormCalc, PDF1B provider-native transport, PDF1C passwords/decryption, PDF5 transformations, PDF6
companion placement, macOS runtime implementation, signed/encrypted/restricted modification, browser-authoritative
filling, form-specific business rules, and legal or business correctness.
