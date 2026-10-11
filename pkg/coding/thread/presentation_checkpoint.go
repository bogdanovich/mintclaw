package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	PresentationCheckpointSchemaVersion = 1
	MaxPresentationCheckpointBytes      = 64 << 10
	presentationCheckpointFileName      = "diagnostic-snapshot.json"
)

var validPresentationActivities = map[string]struct{}{
	"idle": {}, "running": {}, "interrupting": {}, "compacting": {},
	"reviewing": {}, "waiting_for_input": {}, "failed": {},
}

var validPresentationOutcomes = map[string]struct{}{
	"completed": {}, "suspended": {}, "failed": {}, "interrupted": {},
}

var validPresentationKinds = map[string]struct{}{
	"user_message": {}, "assistant_message": {}, "final_answer": {}, "reasoning": {},
	"tool_message": {}, "tool_call": {}, "plan_update": {}, "compaction": {},
	"turn_separator": {}, "warning": {}, "error": {},
}

var validPresentationLifecycles = map[string]struct{}{
	"active": {}, "completed": {}, "failed": {}, "interrupted": {}, "suspended": {}, "unknown": {},
}

var validPresentationCompactionStatuses = map[string]struct{}{
	"running": {}, "progress": {}, "completed": {}, "no_progress": {}, "interrupted": {}, "failed": {},
}

var presentationIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,255}$`)

// PresentationCheckpoint is a content-free last-known view of the bounded
// coding frontend projection. It is diagnostic evidence, not canonical chat
// history: an absent checkpoint must be reported as unavailable rather than
// reconstructed from the transcript revision.
type PresentationCheckpoint struct {
	SchemaVersion        int                  `json:"schema_version"`
	ThreadID             string               `json:"thread_id"`
	CapturedAt           time.Time            `json:"captured_at"`
	PresentationRevision uint64               `json:"presentation_revision"`
	Activity             string               `json:"activity"`
	ActiveTurnID         string               `json:"active_turn_id,omitempty"`
	LastTurn             *SafeTurnState       `json:"last_turn,omitempty"`
	ItemCount            int                  `json:"item_count"`
	MaximumSequence      uint64               `json:"maximum_sequence,omitempty"`
	MaximumItemRevision  uint64               `json:"maximum_item_revision,omitempty"`
	KindCounts           map[string]int       `json:"kind_counts,omitempty"`
	LifecycleCounts      map[string]int       `json:"lifecycle_counts,omitempty"`
	PendingInputCount    int                  `json:"pending_input_count,omitempty"`
	ContextUsedTokens    int                  `json:"context_used_tokens,omitempty"`
	ContextLimitTokens   int                  `json:"context_limit_tokens,omitempty"`
	HasOlderEntries      bool                 `json:"has_older_entries,omitempty"`
	LastCompaction       *SafeCompactionState `json:"last_compaction,omitempty"`
}

type SafeTurnState struct {
	TurnID  string `json:"turn_id"`
	Outcome string `json:"outcome"`
}

type SafeCompactionState struct {
	Status              string `json:"status"`
	TranscriptRevision  uint64 `json:"transcript_revision,omitempty"`
	TranscriptCount     int    `json:"transcript_count,omitempty"`
	TokensBefore        int    `json:"tokens_before,omitempty"`
	TokensAfter         int    `json:"tokens_after,omitempty"`
	TokenCountsObserved bool   `json:"token_counts_observed,omitempty"`
	SummariesCreated    int    `json:"summaries_created,omitempty"`
}

func (checkpoint PresentationCheckpoint) Validate() error {
	if checkpoint.SchemaVersion != PresentationCheckpointSchemaVersion {
		return fmt.Errorf("coding presentation checkpoint: unsupported schema %d", checkpoint.SchemaVersion)
	}
	if err := validateThreadID(checkpoint.ThreadID); err != nil {
		return fmt.Errorf("coding presentation checkpoint: %w", err)
	}
	if checkpoint.CapturedAt.IsZero() || checkpoint.CapturedAt.Location() != time.UTC {
		return errors.New("coding presentation checkpoint: capture identity is invalid")
	}
	if _, ok := validPresentationActivities[checkpoint.Activity]; !ok {
		return errors.New("coding presentation checkpoint: activity is invalid")
	}
	if checkpoint.ItemCount < 0 || checkpoint.PendingInputCount < 0 ||
		checkpoint.ContextUsedTokens < 0 || checkpoint.ContextLimitTokens < 0 {
		return errors.New("coding presentation checkpoint: counts are invalid")
	}
	if checkpoint.LastTurn != nil {
		if !presentationIdentifierPattern.MatchString(checkpoint.LastTurn.TurnID) {
			return errors.New("coding presentation checkpoint: last turn ID is required")
		}
		if _, ok := validPresentationOutcomes[checkpoint.LastTurn.Outcome]; !ok {
			return errors.New("coding presentation checkpoint: last turn outcome is invalid")
		}
	}
	if checkpoint.ActiveTurnID != "" && !presentationIdentifierPattern.MatchString(checkpoint.ActiveTurnID) {
		return errors.New("coding presentation checkpoint: active turn ID is invalid")
	}
	if err := validatePresentationCounts(
		checkpoint.KindCounts,
		validPresentationKinds,
		checkpoint.ItemCount,
	); err != nil {
		return fmt.Errorf("coding presentation checkpoint: kind counts: %w", err)
	}
	if err := validatePresentationCounts(
		checkpoint.LifecycleCounts,
		validPresentationLifecycles,
		checkpoint.ItemCount,
	); err != nil {
		return fmt.Errorf("coding presentation checkpoint: lifecycle counts: %w", err)
	}
	if checkpoint.LastCompaction != nil {
		if _, ok := validPresentationCompactionStatuses[checkpoint.LastCompaction.Status]; !ok ||
			checkpoint.LastCompaction.TranscriptCount < 0 || checkpoint.LastCompaction.TokensBefore < 0 ||
			checkpoint.LastCompaction.TokensAfter < 0 || checkpoint.LastCompaction.SummariesCreated < 0 {
			return errors.New("coding presentation checkpoint: compaction state is invalid")
		}
	}
	return nil
}

func validatePresentationCounts(counts map[string]int, allowed map[string]struct{}, total int) error {
	sum := 0
	for key, count := range counts {
		if _, ok := allowed[key]; !ok || count < 0 || count > total-sum {
			return errors.New("invalid count")
		}
		sum += count
	}
	if sum != total {
		return errors.New("counts do not match item count")
	}
	return nil
}

// SavePresentationCheckpoint atomically replaces the content-free diagnostic
// checkpoint while the selected thread writer lease remains active.
func (s *Store) SavePresentationCheckpoint(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	checkpoint PresentationCheckpoint,
) error {
	if s == nil || ctx == nil {
		return errors.New("coding presentation checkpoint: store and context are required")
	}
	if err := metadata.Validate(); err != nil {
		return err
	}
	if checkpoint.ThreadID != metadata.ThreadID || checkpoint.CapturedAt.Before(metadata.CreatedAt) {
		return errors.New("coding presentation checkpoint: metadata binding is invalid")
	}
	data, err := encodePresentationCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	return lease.withActive(s.root, metadata.ThreadID, func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := s.openAttachmentStoreView(metadata.ThreadID)
		if err != nil {
			return fmt.Errorf("coding presentation checkpoint: pin thread: %w", err)
		}
		defer func() { _ = view.Close() }()
		if err = view.validateWriter(lease); err != nil {
			return fmt.Errorf("coding presentation checkpoint: validate writer: %w", err)
		}
		hierarchy, err := s.openAttachmentHierarchy(view.thread, true, presentationDirectory)
		if err != nil {
			return fmt.Errorf("coding presentation checkpoint: create directory: %w", err)
		}
		defer func() { _ = hierarchy.Close() }()
		if err = view.validateHierarchy(hierarchy); err != nil {
			return fmt.Errorf("coding presentation checkpoint: validate directory: %w", err)
		}
		if err = s.writeRoot(hierarchy.Leaf(), presentationCheckpointFileName, data, 0o600); err != nil {
			return fmt.Errorf("coding presentation checkpoint: publish: %w", err)
		}
		if err = errors.Join(view.validateWriter(lease), view.validateHierarchy(hierarchy)); err != nil {
			return &fileutil.CommittedWriteError{Err: fmt.Errorf(
				"coding presentation checkpoint: validate published state: %w",
				err,
			)}
		}
		return nil
	})
}

// LoadPresentationCheckpoint reads the last content-free checkpoint without
// acquiring writer authority or mutating thread state.
func (s *Store) LoadPresentationCheckpoint(
	ctx context.Context,
	threadID string,
) (checkpoint PresentationCheckpoint, ok bool, resultErr error) {
	if s == nil || ctx == nil {
		return PresentationCheckpoint{}, false, errors.New(
			"coding presentation checkpoint: store and context are required",
		)
	}
	if err := validateThreadID(threadID); err != nil {
		return PresentationCheckpoint{}, false, err
	}
	view, err := s.openAttachmentStoreView(threadID)
	if err != nil {
		return PresentationCheckpoint{}, false, fmt.Errorf("coding presentation checkpoint: pin thread: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, view.Close()) }()
	hierarchy, err := s.openAttachmentHierarchy(view.thread, false, presentationDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return PresentationCheckpoint{}, false, nil
	}
	if err != nil {
		return PresentationCheckpoint{}, false, fmt.Errorf("coding presentation checkpoint: open directory: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, hierarchy.Close()) }()
	if err = view.validateHierarchy(hierarchy); err != nil {
		return PresentationCheckpoint{}, false, fmt.Errorf(
			"coding presentation checkpoint: validate directory: %w",
			err,
		)
	}
	data, _, _, err := readAttachmentRootFile(
		ctx,
		hierarchy.Leaf(),
		presentationCheckpointFileName,
		MaxPresentationCheckpointBytes,
	)
	if errors.Is(err, os.ErrNotExist) {
		return PresentationCheckpoint{}, false, nil
	}
	if err != nil {
		return PresentationCheckpoint{}, false, fmt.Errorf("coding presentation checkpoint: read: %w", err)
	}
	checkpoint, err = decodePresentationCheckpoint(data)
	if err != nil {
		return PresentationCheckpoint{}, false, err
	}
	if checkpoint.ThreadID != threadID {
		return PresentationCheckpoint{}, false, errors.New("coding presentation checkpoint: thread binding changed")
	}
	if err = view.validateHierarchy(hierarchy); err != nil {
		return PresentationCheckpoint{}, false, fmt.Errorf(
			"coding presentation checkpoint: revalidate directory: %w",
			err,
		)
	}
	return checkpoint, true, nil
}

func encodePresentationCheckpoint(checkpoint PresentationCheckpoint) ([]byte, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("coding presentation checkpoint: encode: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxPresentationCheckpointBytes {
		return nil, errors.New("coding presentation checkpoint: encoded state exceeds limit")
	}
	return data, nil
}

func decodePresentationCheckpoint(data []byte) (PresentationCheckpoint, error) {
	if len(data) == 0 || len(data) > MaxPresentationCheckpointBytes {
		return PresentationCheckpoint{}, errors.New("coding presentation checkpoint: state exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var checkpoint PresentationCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return PresentationCheckpoint{}, fmt.Errorf("coding presentation checkpoint: decode: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PresentationCheckpoint{}, errors.New("coding presentation checkpoint: trailing JSON content")
	}
	if err := checkpoint.Validate(); err != nil {
		return PresentationCheckpoint{}, err
	}
	return checkpoint, nil
}
