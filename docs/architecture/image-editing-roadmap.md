# Source Image Editing Roadmap

## Outcome

MintClaw can turn a user-provided current image into a meme without asking an
image model to reconstruct the source from a text description. The existing
`image_generate` tool remains the single model-facing surface: prompt-only
calls generate, while calls with `action: edit` send the real source image to
the provider.

## Incident Baseline

The September 13, 2026 Telegram incident exposed two independent gaps:

1. `image_generate` accepted only a prompt, so a request to preserve an image
   actually called `/images/generations` with a prose description.
2. A media-only message sent after the assistant explicitly requested a resend
   was labelled as unrelated by default, even though the prior task was still
   clear in the dialogue.

The first deployed exit test found a third integration boundary: the standard
Image API edit endpoint is multipart, but the ChatGPT/Codex OAuth backend
accepts subscription-backed edits through its JSON Responses endpoint and the
hosted `image_generation` tool. Sending multipart to the Codex backend failed
with `Unsupported content type` before any image could be produced.

The session and Telegram attachment were intact. The failure occurred after
admission, in tool capability and prompt semantics.

## Vertical Slice

### I1: Provider contract

- Add bounded source-image bytes and input fidelity to the image request.
- Advertise editing support and provider input limits.
- Route ChatGPT/Codex OAuth source-image requests through `/responses` with an
  `input_image` data URL and a forced hosted `image_generation` edit. Retain
  `/images/generations` for existing prompt-only requests; standard OpenAI API
  key providers may use multipart `/images/edits` when supported separately.
- Preserve refreshed OAuth/account headers, response bounds, output decoding,
  and result limits.

### I2: Safe model-facing tool

- Add explicit `generate` and `edit` actions.
- Resolve only trusted `media://` references, workspace paths, and configured
  read-path exceptions.
- Accept bounded PNG, JPEG, and WebP inputs and fail closed on invalid paths,
  types, sizes, or counts.
- Default source-preserving edits to high fidelity and automatic output size.
- Tell the model never to claim that a source was preserved when no input image
  was supplied.

### I3: Conversation continuity

- Keep standalone media conservative.
- Allow a media-only turn to fulfill an explicit recent request to send or
  resend media instead of forcing an unrelated-task interpretation.
- Cover the exact assistant-requested-resend flow in prompt regression tests.

### I4: Runtime exit

- Merge through normal CI and review.
- Deploy the exact merge commit.
- Submit a non-sensitive source image through the deployed Telegram path.
- Confirm the diagnostic trace contains an edit call with `input_images`, and
  visually confirm that the delivered meme preserves the source scene and
  applies the requested caption.

## Done Criteria

The slice is complete only when generation remains backward-compatible, edit
requests cannot omit or escape source-image authority, the provider transport
matches the selected authentication mode, unit and integration tests pass, and
one deployed end-to-end meme edit is delivered and visually verified. Masks,
arbitrary filesystem reads, an editor UI, deterministic text layout, and
additional providers remain separate future work.
