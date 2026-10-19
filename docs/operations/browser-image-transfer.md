# Browser Image Transfer

First-party browser tools can retain an observed HTTP(S) image as an original
file and upload that artifact through a later file-chooser action. This uses
the existing download, artifact retention, companion transfer, and upload
boundaries, not a site-specific scraper or screenshot substitution.

## Workflow

1. Open the authorized target/profile and observe fresh state.
2. Call `browser_act` with `action.kind=download` and the image's exact fresh
   `ref`. Copy session, tab, snapshot, and any required context authority from
   the same observation. Do not supply a URL, cookie, selector, or file path.
3. Require a committed artifact. Keep its opaque `artifact.ref`; do not infer
   success from the action status if `artifact_state=unavailable`.
4. In the same browser session, observe the destination again, then use
   `upload` or `file_chooser` with `action.artifact_ref` copied from `artifact.ref`
   and the destination's fresh chooser reference. The retained file may come
   from an earlier observation or another tab; destination authority must be fresh.
5. Complete any separate Save/Submit step under the effective approval policy
   and verify the site's resulting state. File assignment alone does not prove
   that the website received or saved the image.
6. Close the browser session when the workflow is finished.

The Chromium capture path resolves the selected image's `currentSrc` from the
original DOM node. A private temporary page in the same browser context reads
one image response through the browser's normal cookies, proxy, and context
routing. Every redirect remains subject to network policy. The temporary page
is closed before the result is returned; the source page is not clicked or
navigated. Response bytes are streamed under the existing download limit,
hashed, and retained through the existing artifact spool. They never enter
model context, traces, or generic companion command results.

This is a new authorized retrieval of the selected source, not a guarantee
that a changing server returns the same bytes displayed earlier. The response
must have an image content type. Inline `data:` and `blob:` sources, CSS
backgrounds, and targets without an actionable observed reference are not
supported by this typed capture path. They are not silently converted into
screenshots. Ordinary attachment-link downloads remain supported.

## Failure And File Lifetimes

`download_not_dispatched` means trusted preflight rejected an unsupported or
stale target before a page action or resource request began. The invocation
is terminal and cannot be replayed. The same session remains available: make
a fresh observation and select a supported target. A timeout, transport loss,
malformed response, or uncertain post-dispatch failure is not this evidence
and retains the existing conservative handling.

If a completed resource read cannot produce a retained file, the action and
artifact states are reported separately. Do not claim that an artifact exists
or upload a substitute file.

Browser file inputs retain file-backed objects. A selected private upload copy
must therefore survive the assignment RPC and later form submission. The
browser session owns these copies until its worker closes, at which point
the existing private output-directory cleanup removes them. Input digest,
size, filename, ownership, permissions, and staging checks remain unchanged.

## Verification

Run the real Chromium driver regression with the repository's pinned
Playwright library. It verifies authenticated image retrieval, an allowed
redirect, a denied redirect, byte limits, original-byte equality, a separate
later upload step, attachment compatibility, and preflight recovery.

Run deployed first-party acceptance serially on both placements:

```sh
scripts/browser-capability-smoke.sh --target gateway --profile managed \
  --suite image-transfer --json-output /tmp/gateway-image-transfer.json
scripts/browser-capability-smoke.sh --target companion --profile managed \
  --gateway-host server@oc --fixture-host COMPANION_ADDRESS \
  --suite image-transfer --json-output /tmp/companion-image-transfer.json
```

Also run both placements with `--suite image-transfer-freeform`. That variant
asks the browser agent to copy the original photo into the form and submit it,
without naming the acquisition action or prescribing `artifact_ref`. It tests
capability discovery and autonomous tool selection, rather than only execution
of a supplied tool recipe. The self-test rejects acquisition-action hints in
the delegated task. Both variants require the independent original-byte/hash
oracle, canonical first-party execution evidence, confirmed closure, and
immediate profile reuse. The free-form variant admits no expected tool errors;
the guided variant separately checks one intentional preflight rejection.

Current target actions/features and effective profile policy are the capability
authority. A prior failed invocation, an old learned note, or the absence of a
website's Save image control does not establish that first-party original-image
retrieval is unavailable. Conversely, an advertised capability does not grant
permission, guarantee a retained artifact, or make every image source supported.

Run the gateway command on the gateway host. For the companion command,
advertise a fixture address reachable by both endpoints. The fixture sets
only its own scoped test cookie and accepts only a small test image. Its
independent receipt checks uploaded bytes, SHA-256 and MIME, authenticated reads,
and the absence of unintended clicks. Claimed success with different image
bytes must fail the smoke runner's self-test. Trace evidence must show the
first-party operations, and cleanup must close the session and immediately
reopen the profile successfully. No third-party listing is edited or published.
The image-transfer probe declares its download/upload/submission work as an
`external_action` objective with runtime receipts, separately from its one
machine-readable result objective. It does not bypass completion accounting.
The native protocol can return that one result alongside receipted effects only
when the entire objective succeeds. Partial, unreceipted, ambiguous, invalid,
and oversized results do not receive a standalone success projection. Human
presentation remains separate from these machine-readable records.

To report overall success, `browser_act` operations classified as `unknown` or
`external_commit` must all be accounted for, including download and upload,
not just the final Submit. An original-file acquisition can remain a `result`
objective: return its exact committed references in a `kind=artifact` output.
The runtime associates an unclaimed unknown-effect download receipt with that
exact output reference. Explicit external-action receipt claims take priority;
each receipt is accounted for only once. This does not change the download's
effect, approval policy, or execution authority. Prose mentioning a file,
mismatched references, uploads, clicks, and external commits cannot use this
association. Other receipts still require matching external-action objectives.
Failed or not-dispatched actions
do not supply completion receipts. If the producer omits receipts, the existing
model-only finalization repair supplies a bounded list of unclaimed runtime IDs
without repeating browser actions. The runtime still rejects unexpected effects
and unverified postconditions; receipt accounting is not publication authority.

For a file-only acceptance, use the same owned image fixture but request only
the original photograph as a file artifact, with no upload or submission.
Declare it as a `result` with artifact output, not an external change. Verify
the retained original bytes and MIME against the fixture, the exact download
receipt association, a successful complete natural-language answer, confirmed
closure and immediate profile reuse. No site-specific rule or acquisition
action hint is needed. Run this on gateway/managed and companion/managed,
alongside the transfer probe above, to cover both output-only acquisition and
an explicitly declared external workflow.

Execution success and a complete user-facing answer are separate checks. When
all objectives are verified but the producer's answer fails output-coverage
validation, presentation gets one tool-free opportunity to correct the answer,
including the caller's language and response format. This budget is independent
of the one correction for a missing or invalid internal outcome report: repairing
that report must not consume the later presentation opportunity. The presentation
model returns only natural-language text; the runtime attaches it to the frozen
verified report after coverage validation, without asking the model to reproduce
objective IDs, outputs, or receipt claims. It must
retain the completed outputs and receipts; it cannot repeat downloads, reopen a
closed session, or replay a commit. Valid answers and explicitly requested exact
JSON do not incur this extra pass. If repair still fails, only the verified
outputs are rendered as a bounded fallback; rejected prose is never admitted.
The runtime retains the original verified outcome across this call. Malformed
reports, changed verified fields, or exhausted transient provider retries cannot
erase its outputs or closure evidence. Cancellation and policy errors retain
their existing fail-closed handling; new user guidance remains authoritative.
This behavior belongs to the shared agent finalizer, not a site or channel rule.
The same bounded presentation phase covers validated partial and blocked results.
It preserves their incomplete status and the specific blocker verbatim, together
with every retained output and receipt; formatting cannot certify missing work.

With `agent live --trace-evidence-agent browser`, canonical trace publication
can follow the visible final response. The CLI waits up to 30 seconds for this
passive evidence, also subject to its overall command context. Missing evidence
does not prove that browser effects did not occur: inspect the exact owner state
and independent fixture receipt before considering another action.
