# Image Generation Provider Architecture

## Outcome

MintClaw exposes one stable model-facing `image_generate` tool for both new
images and source-image edits. Operators select the backend by setting
`tools.image_generate.model`; a matching enabled `model_list[].model_name`
owns the provider, native model identifier, endpoint, proxy, headers, timeout,
and credentials. Adding Gemini therefore does not add another tool to the
agent context.

The supported backends in this slice are:

- ChatGPT/Codex OAuth with GPT Image, including the legacy selectors
  `gpt-image-2`, `openai/gpt-image-2`, and
  `openai-codex/gpt-image-2`;
- direct Gemini API with a native Nano Banana image model. The recommended
  model is `gemini-3.1-flash-image`; an explicitly configured future native
  Gemini model is accepted when its identifier starts with `gemini-` and
  contains `-image`.

## Ownership Boundary

The tool owns the portable and safety-sensitive surface:

- prompt, generate/edit action, source-image references, output preferences,
  and delivery intent;
- workspace and allow-path enforcement;
- bounded file reads and PNG/JPEG/WebP input validation;
- generated-file permissions, media registration, and channel delivery.

Each provider adapter owns:

- authentication and wire endpoint;
- native request construction and parameter normalization;
- provider-specific input/result limits;
- response decoding, MIME verification, and safe provider errors.

The tool never exposes provider credentials or adds provider-specific fields
to its schema. A provider may ignore a portable best-effort hint when its API
does not support that field. Gemini omits `quality` and `input_fidelity` and
normalizes requested WebP output to PNG. Known sizes map to Gemini aspect
ratio and resolution controls; unknown or `auto` sizes leave those controls to
Gemini instead of sending invalid values.

## Gemini Transport and Bounds

The Gemini adapter calls `POST {api_base}/interactions` with
`X-Goog-Api-Key`. Text and actual base64 source bytes are submitted as typed
Interactions input blocks. `response_format.type` is `image`, and `store` is
always `false`.

The adapter fails closed before network access when the API key, prompt, or
native image model is invalid. It accepts at most four PNG/JPEG/WebP sources
and at most 14 MiB of source bytes in total. That bound leaves room for base64
expansion inside Gemini's 20 MB inline request limit. The final serialized
request is checked against that limit as well. Response JSON is capped at
48 MiB and one decoded output at 32 MiB. Output MIME and extension come from
the actual PNG/JPEG/WebP signature and must agree with any MIME declared by
Gemini. Thought images are ignored; only the first final `model_output` image
is delivered.

No provider failover occurs for this tool. A missing or disabled alias, an
unsupported provider/model, malformed output, or provider failure is visible
to the agent instead of silently falling back to another image backend.

## Configuration

`config.json` selects the Gemini alias without containing a secret:

```json
{
  "model_list": [
    {
      "model_name": "nano-banana",
      "provider": "gemini",
      "model": "gemini-3.1-flash-image",
      "enabled": true
    }
  ],
  "tools": {
    "image_generate": {
      "enabled": true,
      "model": "nano-banana"
    }
  }
}
```

The matching `.security.yml` entry supplies the key:

```yaml
model_list:
  nano-banana:
    api_keys:
      - "your-gemini-api-key"
```

To switch back, set `tools.image_generate.model` to
`openai-codex/gpt-image-2`; the tool name and tool-call arguments do not
change.

## Done Criteria

This provider slice is complete when generation and source-image editing use
the same tool contract on both backends; alias selection is fail closed;
Gemini auth, exact source bytes, normalization, bounds, and output decoding
are covered by tests; existing Codex generation/edit tests remain green; the
change is reviewed, merged, and deployed; and every live verification allowed
by the deployed credentials has passed.

Masks, arbitrary filesystem access, provider-owned conversational edit state,
Vertex AI service accounts, automatic backend failover, deterministic text
layout, and unrelated CLI delivery behavior remain out of scope.
