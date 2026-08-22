package channels

import "testing"

type declaredVoiceChannel struct {
	mockChannel
	caps VoiceCapabilities
}

func (c *declaredVoiceChannel) VoiceCapabilities() VoiceCapabilities {
	return c.caps
}

func TestDetectVoiceCapabilitiesUsesCurrentChannelContracts(t *testing.T) {
	tests := []struct {
		name         string
		channel      Channel
		asrAvailable bool
		ttsAvailable bool
		want         VoiceCapabilities
	}{
		{name: "nil channel"},
		{name: "plain channel", channel: &mockChannel{}, asrAvailable: true, ttsAvailable: true},
		{
			name: "declared capabilities", channel: &declaredVoiceChannel{
				caps: VoiceCapabilities{ASR: true, TTS: true},
			}, asrAvailable: true, ttsAvailable: true,
			want: VoiceCapabilities{ASR: true, TTS: true},
		},
		{
			name: "provider availability gates declarations", channel: &declaredVoiceChannel{
				caps: VoiceCapabilities{ASR: true, TTS: true},
			},
		},
		{
			name: "media sender infers tts only", channel: &mockMediaChannel{},
			asrAvailable: true, ttsAvailable: true, want: VoiceCapabilities{TTS: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectVoiceCapabilities(tt.channel, tt.asrAvailable, tt.ttsAvailable); got != tt.want {
				t.Fatalf("DetectVoiceCapabilities() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
