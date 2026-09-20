# PDF4A XFA qualification harness

This directory supports the bounded PDF4A feasibility decision. It is not a production XFA backend and none of its
third-party packages enter MintClaw's runtime dependency graph.

Run the complete qualification from the repository root:

```sh
scripts/document-xfa-qualification.sh --output /tmp/mintclaw-pdf4a-evidence
```

The output directory must be empty. The harness creates a private temporary directory, regenerates the synthetic
corpus, installs pinned disposable dependencies, runs the current `pkg/document` regression suite, exercises all
positive and negative gates, captures independent PDF.js before/after renders, writes `manifest.json`, `result.json`,
`before.png`, and `after.png`, and then deletes the temporary environment.

Required host commands are Git, Go, Node/npm, Python 3, and curl. The Playwright Chromium download is temporary and
can be large. The render page serves only the private scratch directory on loopback, does not instantiate a scripting
manager, sets `isEvalSupported=false`, and aborts every request outside its loopback origin.

The test-only gate accepts only the deterministic corpus emitted by `generate`; it deliberately is not a general PDF
parser. A future PDF4B implementation must enforce the selected subset through the existing bounded document worker
and production inspection backend rather than promoting this gate into a second parser or control plane.
