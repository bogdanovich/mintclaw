package asr

import (
	"context"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/config"
	"github.com/bogdanovich/mintclaw/pkg/providers"
)

const elevenLabsSupportedModelID = "scribe_v1"

func ElevenLabsSupportedModelID() string {
	return elevenLabsSupportedModelID
}

type Transcriber interface {
	Name() string
	Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error)
}

type TranscriptionResponse struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// audioCapableModelPatterns are modelID substrings that identify models known
// to accept audio input for chat-based transcription on OpenAI-compatible
// protocols. Dedicated transcription models (whisper, gpt-*-transcribe) are
// already routed to the Whisper transcriber before this check runs.
var audioCapableModelPatterns = []string{
	"whisper",
	"transcribe",
	"gpt-4o-audio",
	"gpt-4.1-audio",
	"audio-preview",
	"-audio",
	"sensevoice",
	"funasr",
	"paraformer",
	"gemini-2.5-flash",
	"gemini-2.5-pro",
	"gemini-2.0-flash",
	"gemini-2.0-pro",
	"gemini-1.5-flash",
	"gemini-1.5-pro",
	"qwen2.5-omni",
	"qwen3-omni",
}

// modelSupportsAudioInput reports whether modelID is known to accept audio
// input. Matching is case-insensitive and substring-based so custom aliases
// and provider-specific deployment names (for example Azure deployment names
// containing "audio") keep working.
func modelSupportsAudioInput(modelID string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	for _, pattern := range audioCapableModelPatterns {
		if strings.Contains(normalized, pattern) {
			return true
		}
	}
	return false
}

func supportsAudioTranscription(modelCfg *config.ModelConfig) bool {
	protocol, modelID := providers.ExtractProtocol(modelCfg)

	switch protocol {
	case "openai", "azure",
		"litellm", "openrouter", "groq", "zhipu", "gemini", "nvidia",
		"ollama", "moonshot", "shengsuanyun", "deepseek", "cerebras",
		"vivgrid", "volcengine", "vllm", "qwen-portal", "qwen-intl", "qwen-us",
		"mistral", "avian", "minimax", "longcat", "modelscope", "novita",
		"alibaba-coding", "zai":
		// These protocols all go through the OpenAI-compatible or Azure provider path in
		// providers.CreateProviderFromConfig, so they are the only ones that can supply
		// the audio media payload shape expected by NewAudioModelTranscriber. Restrict
		// further by modelID, since not every model under these protocols accepts audio.
		return modelSupportsAudioInput(modelID)
	default:
		return false
	}
}

func supportsWhisperTranscription(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)

	switch protocol {
	case "openai", "litellm", "openrouter", "groq", "zhipu", "gemini", "nvidia",
		"ollama", "moonshot", "shengsuanyun", "deepseek", "cerebras",
		"vivgrid", "volcengine", "vllm", "qwen-portal", "qwen-intl", "qwen-us",
		"mistral", "avian", "minimax", "longcat", "modelscope", "novita",
		"alibaba-coding", "zai", "mimo":
		return true
	default:
		return false
	}
}

func whisperModelID(modelCfg *config.ModelConfig) string {
	if modelCfg == nil {
		return ""
	}

	if !supportsWhisperTranscription(modelCfg) {
		return ""
	}

	_, modelID := providers.ExtractProtocol(modelCfg)
	normalized := strings.ToLower(modelID)
	if strings.Contains(normalized, "whisper") || strings.Contains(normalized, "transcribe") {
		if modelCfg.APIKey() == "" && modelCfg.AuthMethod != "oauth" {
			return ""
		}
		return modelID
	}
	return ""
}

func isElevenLabsTranscriptionModel(modelCfg *config.ModelConfig) bool {
	if modelCfg == nil || modelCfg.APIKey() == "" {
		return false
	}

	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "elevenlabs"
}

func transcriberFromModelConfig(modelCfg *config.ModelConfig) Transcriber {
	if modelCfg == nil {
		return nil
	}

	if isElevenLabsTranscriptionModel(modelCfg) {
		_, modelID := providers.ExtractProtocol(modelCfg)
		return NewElevenLabsTranscriber(modelCfg.APIKey(), modelCfg.APIBase, modelID)
	}
	if modelID := whisperModelID(modelCfg); modelID != "" {
		return NewWhisperTranscriber(modelCfg)
	}
	if supportsAudioTranscription(modelCfg) {
		return NewAudioModelTranscriber(modelCfg)
	}
	return nil
}

func fallbackTranscriberFromModelConfig(modelCfg *config.ModelConfig) Transcriber {
	if modelCfg == nil {
		return nil
	}

	if isElevenLabsTranscriptionModel(modelCfg) {
		_, modelID := providers.ExtractProtocol(modelCfg)
		return NewElevenLabsTranscriber(modelCfg.APIKey(), modelCfg.APIBase, modelID)
	}
	if modelID := whisperModelID(modelCfg); modelID != "" {
		return NewWhisperTranscriber(modelCfg)
	}
	return nil
}

// DetectTranscriber inspects cfg and returns the appropriate Transcriber, or
// nil if no supported transcription provider is configured.
func DetectTranscriber(cfg *config.Config) Transcriber {
	if cfg == nil {
		return nil
	}

	if modelName := strings.TrimSpace(cfg.Voice.ModelName); modelName != "" {
		modelCfg, err := cfg.GetModelConfig(modelName)
		if err == nil {
			if tr := transcriberFromModelConfig(modelCfg); tr != nil {
				return tr
			}
		}
	}

	// Fall back to compatibility scanning for legacy auto-detected ASR providers.
	for _, mc := range cfg.ModelList {
		if tr := fallbackTranscriberFromModelConfig(mc); tr != nil {
			return tr
		}
	}
	return nil
}
