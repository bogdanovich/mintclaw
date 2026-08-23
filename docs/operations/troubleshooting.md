# Troubleshooting

## "model ... not found in model_list" or OpenRouter "free is not a valid model ID"

**Symptom:** You see either:

- `Error creating provider: model "openrouter/free" not found in model_list`
- OpenRouter returns 400: `"free is not a valid model ID"`

**Cause:** `provider` selects the runtime provider and `model` is sent to it
unchanged. Omitting `provider` is invalid, and a provider prefix embedded in
`model` does not select that provider.

- **Wrong:** `"model": "openrouter/free"` → `provider` is missing.
- **Right:** `"provider": "openrouter", "model": "free"` → OpenRouter receives `free`.

**Fix:** In `~/.mintclaw/config.json` (or your config path):

1. **agents.defaults.model_name** must match a `model_name` in `model_list` (e.g. `"openrouter-free"`).
2. That entry must set **provider** to `openrouter`, and **model** should be a valid OpenRouter model ID, for example:
   - `"free"` – auto free-tier
   - `"google/gemini-2.0-flash-exp:free"`
   - `"meta-llama/llama-3.1-8b-instruct:free"`

Example snippet:

```json
{
  "agents": {
    "defaults": {
      "model_name": "openrouter-free"
    }
  },
  "model_list": [
    {
      "model_name": "openrouter-free",
      "provider": "openrouter",
      "model": "free",
      "api_keys": ["sk-or-v1-YOUR_OPENROUTER_KEY"],
      "api_base": "https://openrouter.ai/api/v1"
    }
  ]
}
```

Get your key at [OpenRouter Keys](https://openrouter.ai/keys).
