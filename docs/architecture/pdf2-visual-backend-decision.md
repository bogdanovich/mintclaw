# PDF2 Form Appearance Verification Decision

## Decision

Every `linux/amd64` AcroForm fill candidate must pass two gates inside the existing one-shot
document worker before its artifact descriptor can be adopted:

1. `pdfcpu` `v0.15.0` reopens the candidate and verifies field structure, normalized values,
   widget states, unassigned-field preservation, and appearance-stream presence.
2. the pinned Poppler `24.02.0` executables render every affected page at 144 DPI and extract
   word bounding boxes. MintClaw verifies the expected text or choice inside each assigned widget,
   rejects intersecting glyphs outside its rectangle, requires each expected word box to contain
   its own visible annotation-pixel delta, rejects overlapping affected widgets, and requires a
   visible annotation-pixel delta for every non-empty text/choice field and selected button.

The visual gate uses the Poppler executables and hashes already admitted by PDF1A. It adds no new
runtime dependency, service, model tool, browser path, OCR path, or model judgment. Candidate bytes,
rendered pages, local paths, and field values remain private to the worker; the result exposes only
backend identities, digests, affected page numbers, and assertion counts.

`flatten` remains unavailable. `pdfcpu` `v0.15.0` has no qualified form-flatten operation, and
discarding annotations without proving that their appearances were incorporated into page content
would lose user data. MintClaw therefore advertises an explicit unavailable capability instead of
implementing an unsafe approximation. A later PR may admit flattening only with independent
structural and pixel evidence over the complete supported field matrix.

## Visible-value contract

The worker renders both the ordinary page and the same page with annotations hidden. It also writes
a bounded private verification copy with affected-page annotations removed and extracts that
copy's background word boxes. Text, date, combo, and list assignments must be extractable from the
assigned widget rectangle, the expected word boxes themselves must overlap the annotation-only
raster delta, and no background word may intersect those matched boxes. A border, static page text,
or unrelated changed pixel elsewhere in the widget cannot satisfy that assertion. Assigned widget
rectangles may not overlap any other page annotation, so assigned and unassigned annotations cannot
reuse the same visual evidence. List-box choices additionally require a horizontal selection-fill
band across the row containing every selected option; the presence of an unselected label is not
sufficient. Checkbox and radio assignments are state-verified structurally; selected widgets must
additionally create a minimum raster delta inside the conservative center half of the widget. This
interior assertion excludes the control border, so an empty checkbox or radio outline cannot prove
that the selected mark is visible. Unchecked widgets are permitted to have no visible mark.

The initial admitted geometry is an unrotated crop box. A rotated affected page fails visual
verification until its widget-to-render coordinate transform has dedicated fixtures and oracle
evidence. Before either Poppler render starts, crop dimensions are converted at the pinned DPI and
checked against the 4096-pixel edge, 16-million-pixel page, and 32-million-pixel operation limits;
the operation budget charges both the ordinary and annotation-hidden retained rasters.
The PNG header is decoded and checked against those preflight dimensions before the full raster is
decoded. A fill may affect at most eight distinct pages. Empty, invalid, over-limit, or partially
verifiable requests never produce an adopted candidate.

Text mismatches are classified conservatively:

- a different complete value is `appearance_stale`;
- an expected value whose rendered prefix or suffix is the only visible content is
  `content_clipped`;
- a glyph box intersecting but extending outside the widget is `content_clipped`; and
- an expected glyph box without its own annotation-only pixels is `appearance_stale`;
- a selected list row without its expected horizontal selection fill is `appearance_stale`; and
- a selected checkbox or radio with only its border visible is `appearance_stale`.

For non-ASCII appearances, pdfcpu selects its embedded `Roboto-Regular` font. MintClaw checks the
font's loaded character map before writing. Values containing an unavailable glyph, such as the
synthetic CJK missing-font case, fail as `appearance_unavailable`; supported Cyrillic and accented
Latin text must round-trip and render. This prevents a structurally correct `/V` value from being
reported as successful when the appearance silently drops characters.

## Independent oracle and goldens

[`form-visual-manifest.json`](../../pkg/document/testdata/form-visual-manifest.json) binds the
synthetic source digest, pdfcpu and Poppler versions, executable hashes, pypdf version, affected
pages, render dimensions, negative cases, and a normalized pixel-difference golden. The golden is
an 8-by-8 grid computed from each ordinary Poppler render versus its annotation-hidden render. It
uses a documented mean-difference tolerance of `0.002` plus a minimum changed-pixel count; it is not
compressed PNG byte equality.

The checked-in oracle command exercises the production writer, proves the source digest did not
change, independently reopens the candidate with `pypdf` `6.1.1`, checks all normalized values and
widget states, renders both affected pages with pinned Poppler, and compares their form-appearance
fingerprints with the manifest:

```sh
uv venv /tmp/mintclaw-pdf2-oracle
uv pip install --python /tmp/mintclaw-pdf2-oracle/bin/python pypdf==6.1.1
PDF2_PYTHON=/tmp/mintclaw-pdf2-oracle/bin/python make test-document-form-write-oracle
```

Success prints `MINTCLAW_PDF2_FORM_WRITE_ORACLE_OK`. Focused tests additionally cover supported
Unicode, missing glyphs, stale appearances, stale list selection, clipping, expected-glyph raster
ownership, border-only checkbox/radio appearances, overlapping widgets, pre-render page bounds,
affected-page limits, untrusted visual evidence, source immutability, and failure-without-artifact
behavior.

## Update and rollback

Changing pdfcpu, Poppler, fonts, render DPI, widget geometry, normalization, or golden logic requires
one focused requalification that updates this decision, the manifest, executable hashes, and oracle
evidence together. A failed or unavailable visual backend makes fill unavailable; it cannot be
bypassed by structural verification alone.

Rollback restores the preceding MintClaw binary and leaves public `fill`, `verify`, and `flatten`
capabilities unavailable until the complete visual contract is requalified. PDF0A, PDF0B, PDF1A,
and read-only PDF2 field discovery remain independently usable.
