# TTS（文本转语音）

这个目录负责 MintClaw 的语音合成能力。

如果你是第一次配置 TTS，可以参照下面这个流程：

1. 在 `model_list` 里添加一个已启用且显式设置 `provider` 和 `model` 的 TTS 模型。
2. 用 `voice.tts_model_name` 指向这个模型。
3. 在 `.security.yml` 里配置对应的 API Key。

## 快速推荐

对于大多数用户，建议优先从下面两种开始：

| 提供商 | 推荐理由 |
| --- | --- |
| [OpenAI](https://platform.openai.com/docs/guides/text-to-speech) | 这是 MintClaw 当前最稳定、最直接的 TTS 路径。当前实现就是围绕 OpenAI 兼容的 `/audio/speech` 接口格式构建的，所以 OpenAI 是最稳妥的默认选择。 |
| [Xiaomi MiMo](https://platform.xiaomimimo.com) | 由于响应速度和语音音色对于中国用户更友好，MiMo 是一个不错的第二选择。 |

## TTS 配置是如何工作的

MintClaw 不会把 TTS 的 API Key 放在 `voice` 配置里。

推荐方式是：

- `voice.tts_model_name` 用来选择 `model_list` 里的某个命名模型。
- 对应的 `model_list` 条目提供真实的 provider、model ID、`api_base` 和代理配置。
- `.security.yml` 负责保存该模型条目的 API Key。

这是当前推荐且受支持的配置方式。

## 推荐配置方式

### 方案 A：OpenAI

`config.json`

```json
{
  "voice": {
    "tts_model_name": "openai-tts"
  },
  "model_list": [
    {
      "model_name": "openai-tts",
      "provider": "openai",
      "model": "tts-1",
      "enabled": true
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  openai-tts:
    api_keys:
      - "sk-openai-your-key"
```

### 方案 B：Xiaomi MiMo

`config.json`

```json
{
  "voice": {
    "tts_model_name": "mimo-tts"
  },
  "model_list": [
    {
      "model_name": "mimo-tts",
      "provider": "mimo",
      "model": "mimo-v2-tts",
      "enabled": true
    }
  ]
}
```

`.security.yml`

```yaml
model_list:
  mimo-tts:
    api_keys:
      - "your-mimo-key"
```

如果你使用自定义的 MiMo 接口地址，也可以显式设置 `api_base`。如果不设置，MintClaw 会自动使用该 provider 的默认地址。

## MintClaw 当前实际发送的 TTS 请求

当前 TTS 运行时使用的是 OpenAI 兼容的语音合成请求，并带有以下默认值：

- Endpoint：`/audio/speech`
- 返回格式：`opus`
- Voice：`alloy`
- Model：来自你所选中的 `model_list` 条目

这意味着：

- `provider: openai` 与 `model: tts-1` 的组合可以直接工作。
- 其他 OpenAI 兼容 provider 也可能可用，前提是它们接受相同的请求格式。
- 可以通过所选模型的 `model_list[].extra_body` 覆盖 `voice` 和 `response_format`。

## MintClaw 如何选择 TTS Provider

`DetectTTS` 只通过一条显式路径选择 TTS：

1. 根据 `voice.tts_model_name` 在已启用的 `model_list` 条目中找到对应模型。
2. 如果匹配条目存在并且有 API Key，MintClaw 使用该条目的配置创建 TTS provider。
3. 如果选择缺失、已禁用、无效或者没有 API Key，TTS 保持禁用。

## 关于 API Base 的处理方式

MintClaw 会对 TTS 的 `api_base` 做规范化处理：

- 对 OpenAI 来说，像 `https://api.openai.com` 或 `https://api.openai.com/v1` 这样的地址，会自动变成 `https://api.openai.com/v1/audio/speech`。
- 对其他 OpenAI 兼容 provider，MintClaw 会尽量保留你提供的基础路径，只确保它最终以 `/audio/speech` 结尾。
- 如果没有设置 `api_base`，MintClaw 会在可用时使用显式配置的 provider 默认地址。

## 常见错误

- `voice.tts_model_name` 指向了一个不存在的 `model_list` 名称。
- 在 `model_list` 里定义了 TTS 模型，但忘了在 `.security.yml` 中配置对应 API Key。
- 误以为 MintClaw 会自动支持 provider 自定义 voice 参数。
- 使用了不兼容 OpenAI `/audio/speech` 请求格式的接口地址。

## 最小检查清单

在测试 `send_tts` 之前，请确认：

- `voice.tts_model_name` 能正确匹配某个 `model_list[].model_name`。
- 所选模型条目显式设置了 `provider` 和 `model`，并且设置了 `enabled: true`。
- `.security.yml` 中对应条目已经配置了有效 API Key。
- 你所选的 provider 支持 OpenAI 兼容的语音合成接口。
- 你选择的模型本身确实支持 TTS。
