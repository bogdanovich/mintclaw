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

Run the gateway command on the gateway host. For the companion command,
advertise a fixture address reachable by both endpoints. The fixture sets
only its own scoped test cookie and accepts only a small test image. Its
independent receipt checks uploaded bytes and SHA-256, authenticated reads,
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
`external_commit` must all be accounted for by the matching external-action
objective, including download and upload, not just the final Submit. Failed or not-dispatched actions
do not supply completion receipts. If the producer omits receipts, the existing
model-only finalization repair supplies a bounded list of unclaimed runtime IDs
without repeating browser actions. The runtime still rejects unexpected effects
and unverified postconditions; receipt accounting is not publication authority.

With `agent live --trace-evidence-agent browser`, canonical trace publication
can follow the visible final response. The CLI waits up to 30 seconds for this
passive evidence, also subject to its overall command context. Missing evidence
does not prove that browser effects did not occur: inspect the exact owner state
and independent fixture receipt before considering another action.
