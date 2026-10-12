package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodingStateTrashRetentionDefaultsToThirtyDays(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Coding.State.TrashRetention(); got != 30*24*time.Hour {
		t.Fatalf("TrashRetention() = %v, want 30 days", got)
	}
	if err := cfg.ValidateCodingState(); err != nil {
		t.Fatalf("ValidateCodingState() error = %v", err)
	}

	var omitted CodingStateConfig
	if got := omitted.TrashRetention(); got != 30*24*time.Hour {
		t.Fatalf("omitted TrashRetention() = %v, want 30 days", got)
	}
}

func TestValidateCodingStateAcceptsBounds(t *testing.T) {
	for _, hours := range []int{0, MinCodingTrashRetentionHours, MaxCodingTrashRetentionHours} {
		cfg := DefaultConfig()
		cfg.Coding.State.TrashRetentionHours = hours
		if err := cfg.ValidateCodingState(); err != nil {
			t.Fatalf("hours %d: ValidateCodingState() error = %v", hours, err)
		}
	}
}

func TestValidateCodingStateRejectsOutsideBounds(t *testing.T) {
	for _, hours := range []int{-1, MinCodingTrashRetentionHours - 1, MaxCodingTrashRetentionHours + 1} {
		cfg := DefaultConfig()
		cfg.Coding.State.TrashRetentionHours = hours
		err := cfg.ValidateCodingState()
		if err == nil || !strings.Contains(err.Error(), "coding.state.trash_retention_hours") {
			t.Fatalf("hours %d: ValidateCodingState() error = %v", hours, err)
		}
	}
}

func TestLoadConfigValidatesCodingTrashRetention(t *testing.T) {
	for _, hours := range []int{0, 48, -1, 23, 8761} {
		t.Run(fmt.Sprint(hours), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			data := fmt.Sprintf(`{"version":4,"coding":{"state":{"trash_retention_hours":%d}}}`, hours)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfigReadOnly(path)
			if hours != 0 && (hours < MinCodingTrashRetentionHours || hours > MaxCodingTrashRetentionHours) {
				if err == nil || !strings.Contains(err.Error(), "coding.state.trash_retention_hours") {
					t.Fatalf("invalid load error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			expected := hours
			if expected == 0 {
				expected = DefaultCodingTrashRetentionHours
			}
			if cfg.Coding.State.TrashRetention() != time.Duration(expected)*time.Hour {
				t.Fatalf("loaded retention = %v", cfg.Coding.State.TrashRetention())
			}
		})
	}
}
