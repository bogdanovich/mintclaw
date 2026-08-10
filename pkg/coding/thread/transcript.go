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

// CommittedPromptError reports that a prompt reached canonical history even
// though a later finalization step failed. Retrying may duplicate the prompt.
type CommittedPromptError struct {
	ThreadID string
	Err      error
}

func (e *CommittedPromptError) Error() string {
	return fmt.Sprintf(
		"coding thread transcript: prompt committed for thread %q; do not blindly retry: %v",
		e.ThreadID,
		e.Err,
	)
}

func (e *CommittedPromptError) Unwrap() error {
	return e.Err
}

// IsCommittedPromptError reports whether err preserves a committed prompt.
func IsCommittedPromptError(err error) bool {
	var committed *CommittedPromptError
	return errors.As(err, &committed)
}

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
	return lease.withActive(s.root, metadata.ThreadID, func() error {
		threadRoot, err := s.ThreadRoot(metadata.ThreadID)
		if err != nil {
			return err
		}
		canonical, err := memory.NewJSONLStore(filepath.Join(threadRoot, "sessions"))
		if err != nil {
			return fmt.Errorf("coding thread transcript: open canonical store: %w", err)
		}
		backend := session.NewJSONLBackend(canonical)
		appendErr := backend.AppendTurnMessage(
			ctx,
			metadata.SessionKey,
			providers.Message{Role: "user", Content: content},
		)
		closeErr := backend.Close()
		return classifyPromptAppend(metadata.ThreadID, appendErr, closeErr)
	})
}

func classifyPromptAppend(threadID string, appendErr, closeErr error) error {
	if memory.IsCommittedAppendError(appendErr) {
		return &CommittedPromptError{ThreadID: threadID, Err: errors.Join(appendErr, closeErr)}
	}
	if appendErr != nil {
		return errors.Join(
			fmt.Errorf("coding thread transcript: append prompt: %w", appendErr),
			closeErr,
		)
	}
	if closeErr != nil {
		return &CommittedPromptError{
			ThreadID: threadID,
			Err:      fmt.Errorf("close canonical store: %w", closeErr),
		}
	}
	return nil
}
