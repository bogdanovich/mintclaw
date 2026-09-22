---
name: imagegen
description: Generate or edit raster images for chat requests when the gateway exposes the image_generate tool; use real source images for edits and keep vector or code-native graphics in their native format.
---

# Image Generation

Use this skill for generated raster artwork such as photographs, illustrations,
textures, mockups, posters, infographics, and transparent-background cutouts.
Use it for edits only when the actual source image is available to the current
turn as a trusted path from an `[image:/path]` tag or as a `media://` reference.

Do not use this workflow for an SVG, icon set, logo system, HTML/CSS visual, or
other code-native asset that should remain deterministic and editable in its
native format. A skill never enables a missing tool: if `image_generate` is not
available, report that dependency once instead of trying unrelated tools or
changing configuration.

## Workflow

1. Classify the request as a new image or an edit. Reference images that only
   guide style or composition do not turn a generation request into an edit.
2. For an edit, pass every real source through `input_images`, set
   `action: edit`, and default to `input_fidelity: high`. Never claim that an
   original was preserved after prompt-only generation.
3. Turn the request into a concise production prompt. Preserve the user's
   stated subject, style, composition, lighting, exact text, invariants, and
   avoid-list. Add detail only when it materially supports the requested use.
4. For a new image, use `action: generate`. Choose size, quality, and output
   format only when the request or intended use needs them.
5. Generate distinct assets or prompts with separate calls and `count: 1`.
   When several images belong to one response, use
   `delivery_intent: immediate_continue` for every non-final image and
   `delivery_intent: final_handled` for the final image.
6. Inspect the returned result against the prompt and edit invariants. If an
   iteration is needed, make one targeted change and re-check it.

## Prompt shape

Use only the fields that improve the result:

```text
Asset type: <where or how the image will be used>
Primary request: <the user's request>
Input images: <source and role, for edits or references>
Subject and scene: <main content and setting>
Style and composition: <medium, framing, placement>
Lighting and color: <only relevant constraints>
Text (verbatim): "<exact visible copy>"
Must preserve: <edit invariants>
Avoid: <negative constraints>
```

For transparent output, request a genuinely transparent background in the
prompt and keep PNG output. For text-heavy visuals, quote visible copy exactly
and verify it in the result.

## Failure behavior

Treat authentication, policy, unsupported input, and invalid-request errors as
terminal for the call. Do not install packages, request credentials in chat,
or silently choose a different provider. The tool owns its configured provider
fallbacks and returns a bounded failure when none succeeds.
