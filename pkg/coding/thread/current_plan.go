package thread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	codingplan "github.com/bogdanovich/mintclaw/pkg/coding/plan"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	presentationDirectory       = "presentation"
	currentPlanFileName         = "current-plan.json"
	CurrentPlanSchemaVersion    = 1
	MaxCurrentPlanCheckpointLen = 40 << 10
	maxCurrentPlanJSONNesting   = 8
)

// CurrentPlanCheckpoint is the replaceable, thread-owned source of truth for
// the latest safe update_plan state. Historical transcript cells remain a
// presentation concern; this checkpoint exists so resume and /status agree.
type CurrentPlanCheckpoint struct {
	SchemaVersion int              `json:"schema_version"`
	UpdatedAt     time.Time        `json:"updated_at"`
	Plan          codingplan.State `json:"plan"`
}

// NewCurrentPlanCheckpoint prepares safe observation state for durable use.
// Ephemeral tool-call correlation is deliberately not persisted.
func NewCurrentPlanCheckpoint(
	plan codingplan.State,
	updatedAt time.Time,
) (CurrentPlanCheckpoint, error) {
	plan = codingplan.Clone(plan)
	plan.CallID = ""
	checkpoint := CurrentPlanCheckpoint{
		SchemaVersion: CurrentPlanSchemaVersion,
		UpdatedAt:     updatedAt.UTC(),
		Plan:          plan,
	}
	if err := checkpoint.Validate(); err != nil {
		return CurrentPlanCheckpoint{}, err
	}
	return checkpoint, nil
}

// Validate checks the complete on-disk current-plan contract.
func (checkpoint CurrentPlanCheckpoint) Validate() error {
	if checkpoint.SchemaVersion != CurrentPlanSchemaVersion {
		return fmt.Errorf("coding current plan: unsupported schema %d", checkpoint.SchemaVersion)
	}
	if checkpoint.UpdatedAt.IsZero() || checkpoint.UpdatedAt.Location() != time.UTC {
		return fmt.Errorf("coding current plan: UTC update timestamp is required")
	}
	if err := codingplan.ValidateSafe(checkpoint.Plan); err != nil {
		return fmt.Errorf("coding current plan: %w", err)
	}
	return nil
}

// SaveCurrentPlan atomically replaces the current-plan checkpoint while the
// selected thread writer lease is active.
func (s *Store) SaveCurrentPlan(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	checkpoint CurrentPlanCheckpoint,
) error {
	if s == nil {
		return fmt.Errorf("coding current plan store is nil")
	}
	if ctx == nil {
		return fmt.Errorf("coding current plan: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return err
	}
	if checkpoint.UpdatedAt.Before(metadata.CreatedAt) {
		return fmt.Errorf("coding current plan: update timestamp precedes thread creation")
	}
	data, err := encodeCurrentPlanCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	return lease.withActive(s.root, metadata.ThreadID, func() error {
		if contextErr := context.Cause(ctx); contextErr != nil {
			return contextErr
		}
		view, openErr := s.openAttachmentStoreView(metadata.ThreadID)
		if openErr != nil {
			return fmt.Errorf("coding current plan: pin thread: %w", openErr)
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return fmt.Errorf("coding current plan: validate writer: %w", writerErr)
		}
		hierarchy, hierarchyErr := s.openAttachmentHierarchy(view.thread, true, presentationDirectory)
		if hierarchyErr != nil {
			return fmt.Errorf("coding current plan: create presentation directory: %w", hierarchyErr)
		}
		defer func() { _ = hierarchy.Close() }()
		if hierarchyErr := view.validateHierarchy(hierarchy); hierarchyErr != nil {
			return fmt.Errorf("coding current plan: validate presentation directory: %w", hierarchyErr)
		}
		if writeErr := s.writeRoot(hierarchy.Leaf(), currentPlanFileName, data, 0o600); writeErr != nil {
			return fmt.Errorf("coding current plan: publish checkpoint: %w", writeErr)
		}
		if validationErr := errors.Join(
			view.validateWriter(lease),
			view.validateHierarchy(hierarchy),
		); validationErr != nil {
			return &fileutil.CommittedWriteError{
				Err: fmt.Errorf("validate published current plan: %w", validationErr),
			}
		}
		return nil
	})
}

// LoadCurrentPlan restores the latest safe plan under the selected thread's
// existing writer authority. A thread without a plan returns ok=false.
func (s *Store) LoadCurrentPlan(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
) (checkpoint CurrentPlanCheckpoint, ok bool, resultErr error) {
	if s == nil {
		return CurrentPlanCheckpoint{}, false, fmt.Errorf("coding current plan store is nil")
	}
	if ctx == nil {
		return CurrentPlanCheckpoint{}, false, fmt.Errorf("coding current plan: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return CurrentPlanCheckpoint{}, false, err
	}
	resultErr = lease.withActive(s.root, metadata.ThreadID, func() error {
		if contextErr := context.Cause(ctx); contextErr != nil {
			return contextErr
		}
		view, openErr := s.openAttachmentStoreView(metadata.ThreadID)
		if openErr != nil {
			return fmt.Errorf("coding current plan: pin thread: %w", openErr)
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return fmt.Errorf("coding current plan: validate writer: %w", writerErr)
		}
		hierarchy, hierarchyErr := s.openAttachmentHierarchy(view.thread, false, presentationDirectory)
		if errors.Is(hierarchyErr, os.ErrNotExist) {
			return nil
		}
		if hierarchyErr != nil {
			return fmt.Errorf("coding current plan: open presentation directory: %w", hierarchyErr)
		}
		defer func() { _ = hierarchy.Close() }()
		if hierarchyErr := view.validateHierarchy(hierarchy); hierarchyErr != nil {
			return fmt.Errorf("coding current plan: validate presentation directory: %w", hierarchyErr)
		}
		data, _, _, readErr := readAttachmentRootFile(
			ctx,
			hierarchy.Leaf(),
			currentPlanFileName,
			MaxCurrentPlanCheckpointLen,
		)
		if errors.Is(readErr, os.ErrNotExist) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("coding current plan: read checkpoint: %w", readErr)
		}
		checkpoint, readErr = decodeCurrentPlanCheckpoint(data)
		if readErr != nil {
			return readErr
		}
		if checkpoint.UpdatedAt.Before(metadata.CreatedAt) {
			return fmt.Errorf("coding current plan: update timestamp precedes thread creation")
		}
		if validationErr := errors.Join(
			view.validateWriter(lease),
			view.validateHierarchy(hierarchy),
		); validationErr != nil {
			return fmt.Errorf("coding current plan: revalidate checkpoint authority: %w", validationErr)
		}
		ok = true
		return nil
	})
	return checkpoint, ok, resultErr
}

func encodeCurrentPlanCheckpoint(checkpoint CurrentPlanCheckpoint) ([]byte, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("coding current plan: encode checkpoint: %w", err)
	}
	data = append(data, '\n')
	if len(data) > MaxCurrentPlanCheckpointLen {
		return nil, fmt.Errorf("coding current plan: checkpoint exceeds %d bytes", MaxCurrentPlanCheckpointLen)
	}
	return data, nil
}

func decodeCurrentPlanCheckpoint(data []byte) (CurrentPlanCheckpoint, error) {
	if len(data) > MaxCurrentPlanCheckpointLen {
		return CurrentPlanCheckpoint{}, fmt.Errorf(
			"coding current plan: checkpoint exceeds %d bytes",
			MaxCurrentPlanCheckpointLen,
		)
	}
	if err := validateCurrentPlanJSON(data); err != nil {
		return CurrentPlanCheckpoint{}, fmt.Errorf("coding current plan: validate JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var checkpoint CurrentPlanCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return CurrentPlanCheckpoint{}, fmt.Errorf("coding current plan: decode checkpoint: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return CurrentPlanCheckpoint{}, fmt.Errorf("coding current plan: decode checkpoint: trailing JSON content")
	}
	if err := checkpoint.Validate(); err != nil {
		return CurrentPlanCheckpoint{}, err
	}
	return checkpoint, nil
}

func validateCurrentPlanJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateCurrentPlanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON content")
		}
		return err
	}
	return nil
}

func validateCurrentPlanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxCurrentPlanJSONNesting {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxCurrentPlanJSONNesting)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			nameToken, memberErr := decoder.Token()
			if memberErr != nil {
				return memberErr
			}
			name, isString := nameToken.(string)
			if !isString {
				return fmt.Errorf("JSON object member name is not a string")
			}
			if _, exists := members[name]; exists {
				return fmt.Errorf("duplicate JSON object member %q", name)
			}
			members[name] = struct{}{}
			if childErr := validateCurrentPlanJSONValue(decoder, depth+1); childErr != nil {
				return childErr
			}
		}
	case '[':
		for decoder.More() {
			if childErr := validateCurrentPlanJSONValue(decoder, depth+1); childErr != nil {
				return childErr
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}
