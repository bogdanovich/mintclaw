package config

import (
	"fmt"
	"time"
)

const (
	DefaultCodingTrashRetentionHours = 30 * 24
	MinCodingTrashRetentionHours     = 24
	MaxCodingTrashRetentionHours     = 365 * 24
)

// CodingStateConfig controls retention for MintClaw-owned local coding state.
// Purging remains an explicit operator action; this value only selects its
// default cutoff.
type CodingStateConfig struct {
	TrashRetentionHours int `json:"trash_retention_hours,omitempty"`
}

// TrashRetention returns the configured recoverable-trash window.
func (c CodingStateConfig) TrashRetention() time.Duration {
	hours := c.TrashRetentionHours
	if hours == 0 {
		hours = DefaultCodingTrashRetentionHours
	}
	return time.Duration(hours) * time.Hour
}

// ValidateCodingState rejects retention windows too short for routine
// recovery or so long that abandoned private state is effectively unbounded.
func (c *Config) ValidateCodingState() error {
	if c == nil {
		return fmt.Errorf("coding state config requires a root config")
	}
	hours := c.Coding.State.TrashRetentionHours
	if hours == 0 {
		return nil
	}
	if hours < MinCodingTrashRetentionHours || hours > MaxCodingTrashRetentionHours {
		return fmt.Errorf(
			"coding.state.trash_retention_hours must be between %d and %d",
			MinCodingTrashRetentionHours,
			MaxCodingTrashRetentionHours,
		)
	}
	return nil
}
