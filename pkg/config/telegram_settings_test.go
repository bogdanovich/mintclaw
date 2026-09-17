package config

import "testing"

func TestTelegramSettingsEffectiveMaxInboundFileSizeBytes(t *testing.T) {
	if got := (TelegramSettings{}).EffectiveMaxInboundFileSizeBytes(); got != DefaultTelegramMaxInboundFileBytes {
		t.Fatalf("default inbound file limit = %d, want %d", got, DefaultTelegramMaxInboundFileBytes)
	}
	const configured = int64(8192)
	if got := (TelegramSettings{MaxInboundFileSizeBytes: configured}).EffectiveMaxInboundFileSizeBytes(); got != configured {
		t.Fatalf("configured inbound file limit = %d, want %d", got, configured)
	}
}
