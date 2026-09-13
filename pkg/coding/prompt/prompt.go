// Package prompt defines the lightweight text contract shared by coding
// frontends, durable threads, and external worker supervisors.
package prompt

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const MaxBytes = 1 << 20

// Validate checks the canonical coding prompt bound before any thread or
// worker state is created.
func Validate(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("coding thread transcript: prompt is required")
	}
	if !utf8.ValidString(content) || len(content) > MaxBytes {
		return fmt.Errorf("coding thread transcript: prompt must be valid UTF-8 within %d bytes", MaxBytes)
	}
	return nil
}
