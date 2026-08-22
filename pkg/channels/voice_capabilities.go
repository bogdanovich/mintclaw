package channels

// VoiceCapabilities describes whether ASR (speech-to-text) and TTS (text-to-speech)
// are available for a channel under the current configuration.
type VoiceCapabilities struct {
	ASR bool
	TTS bool
}

// VoiceCapabilityProvider declares a channel's ASR/TTS support. Channels must
// implement this interface to advertise ASR; TTS can also be inferred from
// MediaSender when no explicit declaration exists.
type VoiceCapabilityProvider interface {
	VoiceCapabilities() VoiceCapabilities
}

// DetectVoiceCapabilities returns ASR/TTS availability for a channel, gated by
// whether providers are configured.
func DetectVoiceCapabilities(ch Channel, asrAvailable bool, ttsAvailable bool) VoiceCapabilities {
	if ch == nil {
		return VoiceCapabilities{}
	}

	if vcp, ok := ch.(VoiceCapabilityProvider); ok {
		caps := vcp.VoiceCapabilities()
		if !asrAvailable {
			caps.ASR = false
		}
		if !ttsAvailable {
			caps.TTS = false
		}
		return caps
	}

	caps := VoiceCapabilities{}
	if ttsAvailable {
		if _, ok := ch.(MediaSender); ok {
			caps.TTS = true
		}
	}

	return caps
}
