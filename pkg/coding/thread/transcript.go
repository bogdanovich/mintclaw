package thread

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/memory"
	"github.com/bogdanovich/mintclaw/pkg/providers"
	"github.com/bogdanovich/mintclaw/pkg/session"
)

const MaxPromptBytes = 1 << 20

// ValidatePrompt checks the canonical coding prompt bound before any thread
// metadata or transcript state is created.
func ValidatePrompt(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("coding thread transcript: prompt is required")
	}
	if !utf8.ValidString(content) || len(content) > MaxPromptBytes {
		return fmt.Errorf("coding thread transcript: prompt must be valid UTF-8 within %d bytes", MaxPromptBytes)
	}
	return nil
}

// AppendUserMessage durably appends one accepted prompt to a thread's
// canonical JSONL transcript while holding its process lease.
func (s *Store) AppendUserMessage(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	content string,
) error {
	if s == nil {
		return fmt.Errorf("coding thread store is nil")
	}
	if err := metadata.Validate(); err != nil {
		return err
	}
	if err := ValidatePrompt(content); err != nil {
		return err
	}
	return lease.withActive(metadata.ThreadID, func() (resultErr error) {
		threadRoot, err := s.ThreadRoot(metadata.ThreadID)
		if err != nil {
			return err
		}
		canonical, err := memory.NewJSONLStore(filepath.Join(threadRoot, "sessions"))
		if err != nil {
			return fmt.Errorf("coding thread transcript: open canonical store: %w", err)
		}
		backend := session.NewJSONLBackend(canonical)
		defer func() {
			if closeErr := backend.Close(); closeErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("coding thread transcript: close canonical store: %w", closeErr),
				)
			}
		}()
		if err := backend.AppendTurnMessage(
			ctx,
			metadata.SessionKey,
			providers.Message{Role: "user", Content: content},
		); err != nil {
			return fmt.Errorf("coding thread transcript: append prompt: %w", err)
		}
		return nil
	})
}
