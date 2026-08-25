# Migration Guide: From `providers` to `model_list`

This guide explains how to migrate from the legacy `providers` configuration to the new `model_list` format.

## Why Migrate?

The new `model_list` configuration offers several advantages:

- **Zero-code provider addition**: Add OpenAI-compatible providers with configuration only
- **Load balancing**: Configure multiple endpoints for the same model
- **Explicit provider resolution**: Every entry stores `provider` separately from its provider-native `model`
- **Cleaner configuration**: Model-centric instead of vendor-centric

## Before and After

### Before: Legacy `providers` Configuration

```json
{
  "providers": {
    "openai": {
      "api_key": "sk-your-openai-key",
      "api_base": "https://api.openai.com/v1"
    },
    "anthropic": {
      "api_key": "sk-ant-your-key"
    },
    "deepseek": {
      "api_key": "sk-your-deepseek-key"
    }
  },
  "agents": {
    "defaults": {
      "provider": "openai",
      "model": "gpt-5.4"
    }
  }
}
```

### After: New `model_list` Configuration

```json
{
  "version": 4,
  "model_list": [
    {
      "model_name": "gpt4",
      "provider": "openai",
      "model": "gpt-5.4",
      "enabled": true,
      "api_keys": ["sk-your-openai-key"],
      "api_base": "https://api.openai.com/v1"
    },
    {
      "model_name": "claude-sonnet-4.6",
      "provider": "anthropic",
      "model": "claude-sonnet-4.6",
      "enabled": true,
      "api_keys": ["sk-ant-your-key"]
    },
    {
      "model_name": "deepseek",
      "provider": "deepseek",
      "model": "deepseek-chat",
      "enabled": true,
      "api_keys": ["sk-your-deepseek-key"]
    }
  ],
  "agents": {
    "defaults": {
      "model_name": "gpt4"
    }
  }
}
```

> **Note**: Set `enabled` explicitly in the converted document. Runtime startup
> does not infer or migrate fields from an older schema version.

## Provider / Model Contract

Every entry must use the current explicit representation:

```json
{
  "provider": "openai",
  "model": "gpt-5.4"
}
```

`model` is sent to the selected provider unchanged, so provider-native IDs may
contain slashes. Examples:

| Config | Resolved Provider | Model Sent Upstream |
|--------|-------------------|---------------------|
| `"provider": "openai", "model": "gpt-5.4"` | `openai` | `gpt-5.4` |
| `"provider": "openrouter", "model": "google/gemini-2.0-flash-exp:free"` | `openrouter` | `google/gemini-2.0-flash-exp:free` |

## ModelConfig Fields

| Field | Required | Description |
|-------|----------|-------------|
| `model_name` | Yes | User-facing alias for the model |
| `provider` | Yes | Provider identifier used for routing |
| `model` | Yes | Provider-native model ID, sent unchanged |
| `api_base` | No | API endpoint URL |
| `api_keys` | No | API authentication keys (array; supports multiple keys for load balancing) |
| `enabled` | Yes for active entries | Sole activation switch. Set it explicitly; credentials and model names do not imply activation. |
| `proxy` | No | HTTP proxy URL |
| `auth_method` | No | Authentication method: `oauth`, `token` |
| `connect_mode` | No | Connection mode for CLI providers: `stdio`, `grpc` |
| `rpm` | No | Requests per minute limit |
| `max_tokens_field` | No | Field name for max tokens |
| `request_timeout` | No | HTTP request timeout in seconds; `<=0` uses default `120s` |

> **Note**: `api_key` (singular) is not accepted. Put every credential in the
> `api_keys` array during the manual conversion.

## Load Balancing

There are two ways to configure load balancing:

### Option 1: Multiple API Keys in `api_keys` (Recommended)

```json
{
  "model_list": [
    {
      "model_name": "gpt4",
      "provider": "openai",
      "model": "gpt-5.4",
      "enabled": true,
      "api_keys": ["sk-key1", "sk-key2", "sk-key3"],
      "api_base": "https://api.openai.com/v1"
    }
  ]
}
```

Or via `.security.yml`:

```yaml
model_list:
  gpt4:
    api_keys:
      - "sk-key1"
      - "sk-key2"
      - "sk-key3"
```

### Option 2: Multiple Model Entries

```json
{
  "model_list": [
    {
      "model_name": "gpt4",
      "provider": "openai",
      "model": "gpt-5.4",
      "enabled": true,
      "api_keys": ["sk-key1"],
      "api_base": "https://api1.example.com/v1"
    },
    {
      "model_name": "gpt4",
      "provider": "openai",
      "model": "gpt-5.4",
      "enabled": true,
      "api_keys": ["sk-key2"],
      "api_base": "https://api2.example.com/v1"
    },
    {
      "model_name": "gpt4",
      "provider": "openai",
      "model": "gpt-5.4",
      "enabled": true,
      "api_keys": ["sk-key3"],
      "api_base": "https://api3.example.com/v1"
    }
  ]
}
```

When you request model `gpt4`, requests will be distributed across all three endpoints using round-robin selection.

## Adding a New OpenAI-Compatible Provider

With `model_list`, adding a new provider requires zero code changes:

```json
{
  "model_list": [
    {
      "model_name": "my-custom-llm",
      "provider": "openai",
      "model": "my-model-v1",
      "enabled": true,
      "api_keys": ["your-api-key"],
      "api_base": "https://api.your-provider.com/v1"
    }
  ]
}
```

Just set `provider` to `openai` (or another supported provider), and provide your provider's API base URL.

## Runtime boundary

MintClaw accepts only the current config version. It does not read `providers`,
merge singular `api_key` values, or rewrite an old document. Complete this
conversion while MintClaw is stopped, validate the result with the new binary,
and then upgrade the coordinated deployment.

## Migration Checklist

- [ ] Identify all providers you're currently using
- [ ] Create `model_list` entries for each provider
- [ ] Set an explicit `provider` and provider-native `model` on every entry
- [ ] Update `agents.defaults.model_name` to reference the new `model_name`
- [ ] Test that all models work correctly
- [ ] Remove or comment out the old `providers` section

## Troubleshooting

### Model not found error

```
model "xxx" not found in model_list
```

**Solution**: Ensure the `model_name` in `model_list` matches the value in `agents.defaults.model_name`.

### Unknown protocol error

```
unknown protocol "xxx" in model "model-name"
```

**Solution**: Set `provider` to a supported value and keep `model` as the
provider-native model ID. See [Provider / Model Contract](#provider--model-contract).

### Missing API key error

```
api_key or api_base is required for HTTP-based protocol "xxx"
```

**Solution**: Provide `api_keys` and/or `api_base` for HTTP-based providers.

## Need Help?

- [GitHub Issues](https://github.com/bogdanovich/mintclaw/issues)
- [Discussion #122](https://github.com/bogdanovich/mintclaw/discussions/122): Original proposal
