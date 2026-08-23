package asr

import (
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestDetectTranscriber(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wantNil  bool
		wantName string
	}{
		{
			name:    "no config",
			cfg:     &config.Config{},
			wantNil: true,
		},
		{
			name: "voice model name selects audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-gemini"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "voice-gemini", Provider: "gemini", Model: "gemini-2.5-flash",
						APIKeys: config.SimpleSecureStrings("sk-gemini-model"),
					},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "voice model name alias selects elevenlabs transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model", Provider: "elevenlabs", Model: "scribe_v1",
						APIKeys: config.SimpleSecureStrings("sk_elevenlabs_test"),
					},
				},
			},
			wantName: "elevenlabs",
		},
		{
			name: "explicit elevenlabs provider selects elevenlabs transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model",
						Provider:  "elevenlabs",
						Model:     "scribe_v1",
						APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
					},
				},
			},
			wantName: "elevenlabs",
		},
		{
			name: "voice model name alias selects whisper transcriber for groq",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model", Provider: "groq", Model: "whisper-large-v3",
						APIKeys: config.SimpleSecureStrings("sk-groq-model"),
					},
				},
			},
			wantName: "whisper",
		},
		{
			name: "openai whisper alias selects whisper transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model", Provider: "openai", Model: "whisper-1",
						APIKeys: config.SimpleSecureStrings("sk-openai-model"),
					},
				},
			},
			wantName: "whisper",
		},
		{
			name: "voice model name alias selects non-gemini audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "my-asr-model"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "my-asr-model", Provider: "openai", Model: "gpt-4o-audio-preview",
						APIKeys: config.SimpleSecureStrings("sk-openai"),
					},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "voice model name selects azure audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-azure-audio"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "voice-azure-audio",
						Provider:  "azure",
						Model:     "my-audio-deployment",
						APIKeys:   config.SimpleSecureStrings("sk-azure"),
						APIBase:   "https://example.openai.azure.com",
					},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "voice model name with non openai compatible protocol does not select audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-anthropic"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "voice-anthropic", Provider: "anthropic", Model: "claude-sonnet-4.6",
						APIKeys: config.SimpleSecureStrings("sk-anthropic"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "ASR model without voice selection is not auto-detected",
			cfg: &config.Config{
				ModelList: []*config.ModelConfig{
					{
						ModelName: "groq", Provider: "groq", Model: "whisper-large-v3",
						APIKeys: config.SimpleSecureStrings("sk-groq-model"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "missing voice model name config returns nil",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "missing"},
				ModelList: []*config.ModelConfig{
					{
						ModelName: "other", Provider: "gemini", Model: "gemini-2.5-flash",
						APIKeys: config.SimpleSecureStrings("sk-other-model"),
					},
				},
			},
			wantNil: true,
		},
		{
			name: "voice model name selects only the named entry",
			cfg: &config.Config{
				Voice: config.VoiceConfig{
					ModelName: "voice-gemini",
				},
				ModelList: []*config.ModelConfig{
					{
						Provider: "openai",
						Model:    "elevenlabs",
						APIKeys:  config.SimpleSecureStrings("sk_elevenlabs_test"),
					},
					{
						ModelName: "voice-gemini", Provider: "gemini", Model: "gemini-2.5-flash",
						APIKeys: config.SimpleSecureStrings("sk-gemini-model"),
					},
				},
			},
			wantName: "audio-model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := DetectTranscriber(tc.cfg)
			if tc.wantNil {
				if tr != nil {
					t.Errorf("DetectTranscriber() = %v, want nil", tr)
				}
				return
			}
			if tr == nil {
				t.Fatal("DetectTranscriber() = nil, want non-nil")
			}
			if got := tr.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want %q", got, tc.wantName)
			}
		})
	}
}

func TestModelSupportsAudioInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		modelID string
		want    bool
	}{
		{name: "openai gpt-4o-audio-preview", modelID: "gpt-4o-audio-preview", want: true},
		{name: "openai gpt-4.1-audio", modelID: "gpt-4.1-audio", want: true},
		{name: "gemini flash", modelID: "gemini-2.5-flash", want: true},
		{name: "gemini pro", modelID: "gemini-2.5-pro", want: true},
		{name: "qwen omni", modelID: "qwen2.5-omni", want: true},
		{name: "sensevoice", modelID: "sensevoice-small", want: true},
		{name: "azure deployment containing audio", modelID: "my-audio-deployment", want: true},
		{name: "case insensitive", modelID: "GEMINI-2.5-Flash", want: true},
		{name: "plain gpt-4o", modelID: "gpt-4o", want: false},
		{name: "llama", modelID: "llama-3.3-70b", want: false},
		{name: "empty", modelID: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelSupportsAudioInput(tc.modelID); got != tc.want {
				t.Fatalf("modelSupportsAudioInput(%q) = %v, want %v", tc.modelID, got, tc.want)
			}
		})
	}
}

func TestSupportsAudioTranscriptionRestrictsByModelID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		provider string
		model    string
		want     bool
	}{
		{name: "openai audio model", provider: "openai", model: "gpt-4o-audio-preview", want: true},
		{name: "gemini multimodal model", provider: "gemini", model: "gemini-2.5-flash", want: true},
		{name: "azure audio deployment", provider: "azure", model: "my-audio-deployment", want: true},
		{name: "openai text-only model", provider: "openai", model: "gpt-4o", want: false},
		{name: "groq text-only model", provider: "groq", model: "llama-3.3-70b", want: false},
		{name: "unsupported protocol", provider: "anthropic", model: "claude-sonnet-4.6", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.ModelConfig{
				Provider: tc.provider,
				Model:    tc.model,
				APIKeys:  config.SimpleSecureStrings("sk-test"),
			}
			if got := supportsAudioTranscription(cfg); got != tc.want {
				t.Fatalf("supportsAudioTranscription(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}
