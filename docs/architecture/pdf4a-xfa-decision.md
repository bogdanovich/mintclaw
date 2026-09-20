# PDF4A XFA Feasibility Decision

## Status

Complete. PDF4A ends with `supported-subset-candidate` for one narrow static-XFA class. This is an architecture and
feasibility result, not shipped XFA support. Production MintClaw continues to detect and refuse every pure or hybrid
XFA document until the separate [PDF4B implementation goal](pdf4b-xfa-static-implementation-goal.md) passes all of
its gates and has deployed evidence.

The admission contract is the [PDF4A feasibility goal](pdf4a-xfa-feasibility-goal.md). Qualification code and the
first evidence set landed in [PR #1282](https://github.com/bogdanovich/mintclaw/pull/1282); the portable dependency
setup and Linux rerun landed through [PR #1284](https://github.com/bogdanovich/mintclaw/pull/1284).

## Decision

MintClaw may proceed to a bounded PDF4B implementation for a PDF only when all of these facts are positively proven:

- the catalog-reachable XFA value is a well-formed packet array with complete `config`, `template`, and `datasets`
  packets;
- the document is pure XFA, has `/NeedsRendering true`, and has no authoritative AcroForm fields;
- the configuration declares static rendering and the template has fixed positions and a fixed page count;
- every requested field has one deterministic template-to-dataset binding with bounded scalar text content;
- the document has no script, event, action, external-data connection, repeat, overflow, page-growth, signature,
  certification, usage-right, restriction, password, or encryption state; and
- a separately packaged, bounded XFA renderer can prove the changed value is visible in the emitted artifact.

Everything else remains `unsupported_xfa`, with a more specific typed reason when inspection can establish one.
Unknown is refusal, never admission.

The candidate mutation path is a pinned `pdfer` packet update behind MintClaw-owned policy, XML serialization,
readback, artifact transaction, and independent visible verification. PDF.js is the qualified independent renderer,
not the mutator and not an interactive filling authority. PDF4B begins with a packaging and isolation gate for that
runtime renderer. If the gate cannot be met without an always-on browser, script execution, ambient network or file
authority, a second document service, or unbounded dependencies, the candidate reverts to detection and refusal.

PDF4B does not admit flattening. Delivering an editable static-XFA artifact is the only candidate write outcome;
portable flattening, if ever needed, belongs to a separately admitted transformation milestone.

## Reproducible Evidence

Run the qualification harness from a clean checkout:

```sh
scripts/document-xfa-qualification.sh --output /tmp/mintclaw-pdf4a-evidence
```

The output directory must be empty. The harness generates the corpus, installs exact disposable dependencies under a
private scratch directory, runs all gates and probes, records four evidence artifacts, and removes the scratch tree.
It does not add a runtime dependency or change a production package.

Two complete runs passed:

| Platform | Result | Before PNG SHA-256 | After PNG SHA-256 | Evidence |
| --- | --- | --- | --- | --- |
| Darwin/x86_64, Python 3.14.3 | `supported-subset-candidate` | `49cb949357ed6fbfc3e51f740b42bf6e4506826733c5f81daaa0cd74bb69600b` | `9b43d790bf10f5a6572b08fa00f7885b591deab432aa9d896c770742c953f3d7` | [`result.json`](evidence/pdf4a/result.json), [`manifest.json`](evidence/pdf4a/manifest.json) |
| Linux/x86_64, Python 3.12.3 | `supported-subset-candidate` | `300b62cea5e5d2a60f75507624dd41c3dc88a801a2817b39a6b0e00f171dea4f` | `12ee653576addda7f4cb07c9c4799341c4a1076fa16563c3652de1388f187341` | [`result.json`](evidence/pdf4a/linux-amd64/result.json), [`manifest.json`](evidence/pdf4a/linux-amd64/manifest.json) |

The Linux run checked out head `617ec80d2ba95c044f6ebda64c43295cbc6a14c3`, later merged as
`e12373de5e8537840b10149b8b313afbe3d16752`, into a private `/tmp` repository on the deployed host and used an unused
loopback port because the default qualification port was already owned by MintClaw:

```sh
MINTCLAW_XFA_QUALIFICATION_PORT=28794 \
  scripts/document-xfa-qualification.sh --output /tmp/mintclaw-pdf4a-evidence
```

Different PNG hashes across platforms are expected rasterization evidence, not semantic drift. Both runs visibly show
the same fixed layout and the value transition from `MINTCLAW_XFA_BEFORE` to `MINTCLAW <&> café 😀`. The machine
results additionally prove:

- the source hash remains unchanged and two safe mutations produce the same output hash;
- MintClaw-owned XML serialization preserves Unicode and XML metacharacters;
- direct unescaped `pdfer` value substitution is a negative control and produces malformed XML;
- PDF.js identifies both artifacts as pure XFA and reads the requested before/after value;
- the render path instantiates no scripting manager and observes no request outside its private loopback origin;
- the existing `CGO_ENABLED=0 go test ./pkg/document` XFA refusal baseline passes; and
- dynamic, foreground/hybrid, repeating, scripted, malformed, signed/restricted, and encrypted fixtures all refuse
  with their expected typed class.

The corpus is generated by repository code and licensed under the repository MIT license. The encrypted negative
fixture is an existing MintClaw test fixture with its SHA-256 recorded in each manifest. No personal or government
document is used.

## Candidate Review

The source review uses immutable commits or releases so the decision does not depend on a moving default branch.

| Candidate | Finding | PDF4A outcome |
| --- | --- | --- |
| `pdfer` | MIT-licensed pure Go XFA packet support can enumerate and rebuild forms, but its own gap analysis excludes complete FormCalc/JavaScript interpretation and important dynamic binding behavior. Its raw updater substitutes strings without XML escaping. Sources: [README/forms](https://github.com/benedoc-inc/pdfer/blob/fda4cc14c67f72bdebfddba26dba36a5f5cba39b/README.md), [gaps](https://github.com/benedoc-inc/pdfer/blob/fda4cc14c67f72bdebfddba26dba36a5f5cba39b/GAPS.md), [XFA updater](https://github.com/benedoc-inc/pdfer/blob/fda4cc14c67f72bdebfddba26dba36a5f5cba39b/forms/xfa/xfa.go), [license](https://github.com/benedoc-inc/pdfer/blob/fda4cc14c67f72bdebfddba26dba36a5f5cba39b/LICENSE). | Conditional packet updater only, pinned to `fda4cc14c67f72bdebfddba26dba36a5f5cba39b`; never trusted for policy, escaping, or visible completion. |
| `pdf-xfa-tools` | A small pikepdf-based packet extractor/replacer whose last qualified commit is from 2022; the repository has no declared license file and supplies no independent renderer or security boundary. Sources: [README](https://github.com/AF-VCD/pdf-xfa-tools/blob/da2e899e7ea3520a2ad85b2a041db5841b23b1ed/README.md), [implementation](https://github.com/AF-VCD/pdf-xfa-tools/blob/da2e899e7ea3520a2ad85b2a041db5841b23b1ed/xfaTools.py). | Rejected for production; retained only as a disposable packet-level comparison probe. |
| `pikepdf` | MPL-2.0 and useful for structural oracle work, but its documentation explicitly says it treats XFA as opaque and does not parse, render, or fill it. Sources: [XFA boundary](https://github.com/pikepdf/pikepdf/blob/910e06b7d547e4e040780748f8ecc0fb7eba7181/docs/topics/interactive_forms.md#xfa-forms), [license](https://github.com/pikepdf/pikepdf/blob/910e06b7d547e4e040780748f8ecc0fb7eba7181/LICENSE.txt). | Qualification oracle helper only; not a runtime XFA backend. |
| PDF.js | Apache-2.0, supports pure-XFA parsing/rendering behind `enableXfa`, and constructs an XFA display layer. XFA is disabled by default and the display layer deliberately does not execute embedded JavaScript. Sources: [API flag](https://github.com/mozilla/pdf.js/blob/1c8020a7d4e43668ac287a3ecf9a8dbea17e4c56/src/display/api.js), [document path](https://github.com/mozilla/pdf.js/blob/1c8020a7d4e43668ac287a3ecf9a8dbea17e4c56/src/core/document.js), [XFA layer](https://github.com/mozilla/pdf.js/blob/1c8020a7d4e43668ac287a3ecf9a8dbea17e4c56/src/display/xfa_layer.js), [license](https://github.com/mozilla/pdf.js/blob/1c8020a7d4e43668ac287a3ecf9a8dbea17e4c56/LICENSE). | Independent qualification renderer; PDF4B must separately prove production packaging and isolation before any write is enabled. |
| XFA-enabled PDFium | Open-source PDFium contains XFA integration, but its official build defaults XFA off while V8 defaults on, and the XFA/V8 build substantially expands packaging and sandbox cost. Sources: [build defaults](https://pdfium.googlesource.com/pdfium/+/4a9b3d668e1ac05625b6576c0c1862e12cbe2ed4/build_overrides/pdfium.gni), [conditional build](https://pdfium.googlesource.com/pdfium/+/4a9b3d668e1ac05625b6576c0c1862e12cbe2ed4/BUILD.gn), [license](https://pdfium.googlesource.com/pdfium/+/4a9b3d668e1ac05625b6576c0c1862e12cbe2ed4/LICENSE). | Deferred; unjustified for the admitted static subset. |

No candidate is accepted as a complete XFA stack. The selected composition is narrow on purpose: MintClaw owns
classification, authority, serialization, resource limits, artifact state, and completion; a pinned library updates
only the admitted packets; a different pinned engine establishes visible truth.

## Architecture And Security Review

PDF4B must extend the existing `pkg/document` operation and worker boundaries. It may add an internal backend adapter
and packaged renderer assets, but not another model-visible tool, daemon, broker, queue, database, or delivery path.
The existing document service remains the authority for source identity, cancellation, artifact transactions,
operation journals, approval, and outbox delivery. The PDF3 protected ledger remains the only owner of protected form
answers.

The renderer is a one-shot child inside the existing document worker. It receives only the private operation scratch
directory and packaged read-only assets; scripts, actions, evaluator support, external URLs, `file:` URLs, arbitrary
browser navigation, inherited secrets, and persistent profiles are forbidden. Process, memory, pixel, page, output,
and time limits are mandatory. Cancellation or worker loss removes scratch output and never publishes an artifact.

The mutation sequence is source identity check, strict feature classification, typed XML-safe update into a new
scratch artifact, structural readback, independent visible render/readback, artifact commit, and optional existing
outbox delivery. Failure at any stage leaves the source unchanged and publishes no successful result. A screenshot
or extracted value alone cannot substitute for verification of the emitted PDF bytes.

This keeps one control plane and rejects the alternatives considered during PDF4A:

- browser-authoritative DOM filling or desktop automation;
- executing XFA JavaScript or FormCalc;
- an always-on browser/document service;
- accepting dataset mutation without a per-output visible render;
- using ordinary Poppler output as XFA authority;
- treating CI goldens as proof for an arbitrary runtime artifact; or
- silently falling back to dynamic, foreground, hybrid, signed, restricted, encrypted, or unknown XFA.

## Residual Gaps And Exact Next Boundary

PDF4A proves feasibility on a synthetic fixed-layout fixture; it does not prove the production renderer package,
field-enumeration contract, real-world producer compatibility, operational footprint, or agent/channel integration.
The next work is therefore PDF4B, beginning with a hard renderer packaging gate on `linux/amd64`.

Only after that gate passes may PDF4B implement the strict classifier and updater. The milestone must still pass unit,
backend, oracle, malicious-input, cancellation, CLI, agent-loop, channel, real-process, deployed, privacy, rollback,
and cleanup evidence before production support is advertised. If the renderer gate or any later visible-verification
gate fails, PDF4B stops and MintClaw keeps its current detection-and-refusal behavior.

macOS remains detection-and-refusal-only. It requires a separate platform qualification using the same contract after
Linux PDF4B is complete; the general roadmap records that parity lane.
