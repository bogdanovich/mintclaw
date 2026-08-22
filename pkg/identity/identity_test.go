package identity

import (
	"testing"
)

func TestBuildCanonicalID(t *testing.T) {
	tests := []struct {
		platform   string
		platformID string
		want       string
	}{
		{"telegram", "123456", "telegram:123456"},
		{"Discord", "98765432", "discord:98765432"},
		{"SLACK", "U123ABC", "slack:U123ABC"},
		{"", "123", ""},
		{"telegram", "", ""},
		{"  telegram  ", "  123  ", "telegram:123"},
	}

	for _, tt := range tests {
		got := BuildCanonicalID(tt.platform, tt.platformID)
		if got != tt.want {
			t.Errorf("BuildCanonicalID(%q, %q) = %q, want %q",
				tt.platform, tt.platformID, got, tt.want)
		}
	}
}

func TestParseCanonicalID(t *testing.T) {
	tests := []struct {
		input        string
		wantPlatform string
		wantID       string
		wantOk       bool
	}{
		{"telegram:123456", "telegram", "123456", true},
		{"discord:98765432", "discord", "98765432", true},
		{"slack:U123ABC", "slack", "U123ABC", true},
		{"nocolon", "", "", false},
		{"", "", "", false},
		{":missing", "", "", false},
		{"missing:", "", "", false},
	}

	for _, tt := range tests {
		platform, id, ok := ParseCanonicalID(tt.input)
		if ok != tt.wantOk || platform != tt.wantPlatform || id != tt.wantID {
			t.Errorf("ParseCanonicalID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.input, platform, id, ok,
				tt.wantPlatform, tt.wantID, tt.wantOk)
		}
	}
}
