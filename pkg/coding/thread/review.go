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
	"unicode/utf8"

	codingreview "github.com/bogdanovich/mintclaw/pkg/coding/review"
	codingworkspace "github.com/bogdanovich/mintclaw/pkg/coding/workspace"
	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	reviewDirectory      = "reviews"
	reviewLatestFileName = "latest.json"
	maxReviewIndexBytes  = 1024
	maxReviewJSONNesting = 16
)

var ErrReviewResultExists = errors.New("coding thread review result already exists")

type latestReviewIndex struct {
	SchemaVersion int       `json:"schema_version"`
	ReviewID      string    `json:"review_id"`
	CompletedAt   time.Time `json:"completed_at"`
}

// PublishReviewResult writes one immutable completed review item while the
// selected-thread writer lease is held.
func (s *Store) PublishReviewResult(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	result codingreview.Result,
	frozenDiff codingworkspace.DiffResult,
) error {
	if s == nil {
		return fmt.Errorf("coding thread review store is nil")
	}
	if ctx == nil {
		return fmt.Errorf("coding thread review: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return err
	}
	data, err := encodeReviewResult(result, frozenDiff)
	if err != nil {
		return err
	}
	indexData, err := encodeLatestReviewIndex(result)
	if err != nil {
		return err
	}
	return lease.withActive(s.root, metadata.ThreadID, func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := s.openAttachmentStoreView(metadata.ThreadID)
		if err != nil {
			return fmt.Errorf("coding thread review: pin thread: %w", err)
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return fmt.Errorf("coding thread review: validate writer: %w", writerErr)
		}
		hierarchy, err := s.openAttachmentHierarchy(view.thread, true, repositoryDirectory, reviewDirectory)
		if err != nil {
			return fmt.Errorf("coding thread review: create result directory: %w", err)
		}
		defer func() { _ = hierarchy.Close() }()
		if hierarchyErr := view.validateHierarchy(hierarchy); hierarchyErr != nil {
			return fmt.Errorf("coding thread review: validate result directory: %w", hierarchyErr)
		}
		root := hierarchy.Leaf()
		name := result.ReviewID + ".json"
		if _, err := root.Lstat(name); err == nil {
			return ErrReviewResultExists
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("coding thread review: inspect destination: %w", err)
		}
		var durabilityWarnings error
		if err := writeRootFileExclusiveAtomic(root, name, data, 0o600); err != nil {
			if !fileutil.IsCommittedWriteError(err) {
				return fmt.Errorf("coding thread review: publish result: %w", err)
			}
			durabilityWarnings = err
		}
		if err := writeRootFileAtomic(root, reviewLatestFileName, indexData, 0o600); err != nil {
			if !fileutil.IsCommittedWriteError(err) {
				return fmt.Errorf(
					"coding thread review: publish latest pointer after immutable result %q: %w",
					result.ReviewID,
					err,
				)
			}
			durabilityWarnings = errors.Join(durabilityWarnings, err)
		}
		if err := errors.Join(view.validateWriter(lease), view.validateHierarchy(hierarchy)); err != nil {
			return &fileutil.CommittedWriteError{Err: fmt.Errorf("validate published review authority: %w", err)}
		}
		if durabilityWarnings != nil {
			return &fileutil.CommittedWriteError{Err: durabilityWarnings}
		}
		return nil
	})
}

// LoadLatestReviewResultWithLease restores the latest completed review under
// the selected thread's existing writer authority. A thread with no completed
// review returns ok=false; interrupted reviews never create this pointer.
func (s *Store) LoadLatestReviewResultWithLease(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
) (result codingreview.Result, ok bool, resultErr error) {
	if s == nil {
		return codingreview.Result{}, false, fmt.Errorf("coding thread review store is nil")
	}
	if ctx == nil {
		return codingreview.Result{}, false, fmt.Errorf("coding thread review: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return codingreview.Result{}, false, err
	}
	resultErr = lease.withActive(s.root, metadata.ThreadID, func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := s.openAttachmentStoreView(metadata.ThreadID)
		if err != nil {
			return fmt.Errorf("coding thread review: pin thread: %w", err)
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return fmt.Errorf("coding thread review: validate writer: %w", writerErr)
		}
		hierarchy, err := s.openAttachmentHierarchy(view.thread, false, repositoryDirectory, reviewDirectory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("coding thread review: open result directory: %w", err)
		}
		defer func() { _ = hierarchy.Close() }()
		if hierarchyErr := view.validateHierarchy(hierarchy); hierarchyErr != nil {
			return fmt.Errorf("coding thread review: validate result directory: %w", hierarchyErr)
		}
		root := hierarchy.Leaf()
		indexData, _, _, err := readAttachmentRootFile(ctx, root, reviewLatestFileName, maxReviewIndexBytes)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("coding thread review: read latest pointer: %w", err)
		}
		index, err := decodeLatestReviewIndex(indexData)
		if err != nil {
			return fmt.Errorf("coding thread review: decode latest pointer: %w", err)
		}
		result, err = loadReviewResult(ctx, root, index.ReviewID)
		if err != nil {
			return fmt.Errorf("coding thread review: load latest result: %w", err)
		}
		if !result.CompletedAt.Equal(index.CompletedAt) {
			return fmt.Errorf("coding thread review: latest pointer completion time does not match result")
		}
		if s.afterReviewResultRead != nil {
			s.afterReviewResultRead()
		}
		if validationErr := errors.Join(
			view.validateWriter(lease),
			view.validateHierarchy(hierarchy),
		); validationErr != nil {
			return fmt.Errorf("coding thread review: revalidate latest result authority: %w", validationErr)
		}
		ok = true
		return nil
	})
	return result, ok, resultErr
}

// LoadReviewResultWithLease reads one immutable result under the selected
// thread's existing writer authority.
func (s *Store) LoadReviewResultWithLease(
	ctx context.Context,
	lease *Lease,
	metadata Metadata,
	reviewID string,
) (codingreview.Result, error) {
	if s == nil {
		return codingreview.Result{}, fmt.Errorf("coding thread review store is nil")
	}
	if ctx == nil {
		return codingreview.Result{}, fmt.Errorf("coding thread review: context is required")
	}
	if err := metadata.Validate(); err != nil {
		return codingreview.Result{}, err
	}
	if err := validateReviewID(reviewID); err != nil {
		return codingreview.Result{}, err
	}
	var result codingreview.Result
	err := lease.withActive(s.root, metadata.ThreadID, func() error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		view, err := s.openAttachmentStoreView(metadata.ThreadID)
		if err != nil {
			return err
		}
		defer func() { _ = view.Close() }()
		if writerErr := view.validateWriter(lease); writerErr != nil {
			return writerErr
		}
		hierarchy, err := s.openAttachmentHierarchy(view.thread, false, repositoryDirectory, reviewDirectory)
		if err != nil {
			return err
		}
		defer func() { _ = hierarchy.Close() }()
		if hierarchyErr := view.validateHierarchy(hierarchy); hierarchyErr != nil {
			return hierarchyErr
		}
		result, err = loadReviewResult(ctx, hierarchy.Leaf(), reviewID)
		if err != nil {
			return err
		}
		if s.afterReviewResultRead != nil {
			s.afterReviewResultRead()
		}
		if validationErr := errors.Join(
			view.validateWriter(lease),
			view.validateHierarchy(hierarchy),
		); validationErr != nil {
			return fmt.Errorf("coding thread review: revalidate result authority: %w", validationErr)
		}
		return nil
	})
	return result, err
}

func encodeReviewResult(result codingreview.Result, frozenDiff codingworkspace.DiffResult) ([]byte, error) {
	if err := result.ValidateAgainstFrozenDiff(frozenDiff); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > codingreview.MaxResultBytes {
		return nil, fmt.Errorf("coding thread review result exceeds %d bytes", codingreview.MaxResultBytes)
	}
	return data, nil
}

func encodeLatestReviewIndex(result codingreview.Result) ([]byte, error) {
	if err := result.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(latestReviewIndex{
		SchemaVersion: codingreview.SchemaVersion,
		ReviewID:      result.ReviewID,
		CompletedAt:   result.CompletedAt,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maxReviewIndexBytes {
		return nil, fmt.Errorf("coding thread latest review pointer exceeds %d bytes", maxReviewIndexBytes)
	}
	return data, nil
}

func decodeLatestReviewIndex(data []byte) (latestReviewIndex, error) {
	if err := validateStrictReviewJSON(data); err != nil {
		return latestReviewIndex{}, err
	}
	var index latestReviewIndex
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return latestReviewIndex{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return latestReviewIndex{}, fmt.Errorf("trailing JSON content")
	}
	if index.SchemaVersion != codingreview.SchemaVersion {
		return latestReviewIndex{}, fmt.Errorf("unsupported latest review schema %d", index.SchemaVersion)
	}
	if err := codingreview.ValidateID(index.ReviewID); err != nil {
		return latestReviewIndex{}, err
	}
	if index.CompletedAt.IsZero() || index.CompletedAt.Location() != time.UTC {
		return latestReviewIndex{}, fmt.Errorf("latest review completion time must be a nonzero UTC timestamp")
	}
	return index, nil
}

func loadReviewResult(ctx context.Context, root *os.Root, reviewID string) (codingreview.Result, error) {
	data, _, _, readErr := readAttachmentRootFile(ctx, root, reviewID+".json", codingreview.MaxResultBytes)
	if readErr != nil {
		return codingreview.Result{}, readErr
	}
	if err := validateStrictReviewJSON(data); err != nil {
		return codingreview.Result{}, fmt.Errorf("decode review result: %w", err)
	}
	var wire reviewResultWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return codingreview.Result{}, fmt.Errorf("decode review result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return codingreview.Result{}, fmt.Errorf("decode review result: trailing JSON content")
	}
	result, err := wire.result()
	if err != nil {
		return codingreview.Result{}, fmt.Errorf("decode review result: %w", err)
	}
	if err := result.Validate(); err != nil {
		return codingreview.Result{}, fmt.Errorf("validate review result: %w", err)
	}
	if result.ReviewID != reviewID {
		return codingreview.Result{}, fmt.Errorf("review result ID does not match its file")
	}
	return result, nil
}

type reviewResultWire struct {
	codingreview.Result
	Findings []reviewFindingWire `json:"findings,omitempty"`
}

type reviewFindingWire struct {
	codingreview.Finding
	Confidence *float64 `json:"confidence"`
}

func (wire reviewResultWire) result() (codingreview.Result, error) {
	result := wire.Result
	if wire.Findings == nil {
		result.Findings = nil
		return result, nil
	}
	result.Findings = make([]codingreview.Finding, len(wire.Findings))
	for index, findingWire := range wire.Findings {
		if findingWire.Confidence == nil {
			return codingreview.Result{}, fmt.Errorf("review finding %d confidence is required", index)
		}
		finding := findingWire.Finding
		finding.Confidence = *findingWire.Confidence
		result.Findings[index] = finding
	}
	return result, nil
}

func validateStrictReviewJSON(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateStrictReviewJSONValue(decoder, 0); err != nil {
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

func validateStrictReviewJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxReviewJSONNesting {
		return fmt.Errorf("JSON nesting exceeds %d levels", maxReviewJSONNesting)
	}
	token, tokenErr := decoder.Token()
	if tokenErr != nil {
		return tokenErr
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
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
			if !isCanonicalReviewJSONMember(name) {
				return fmt.Errorf("non-canonical JSON object member %q", name)
			}
			if _, exists := members[name]; exists {
				return fmt.Errorf("duplicate JSON object member %q", name)
			}
			members[name] = struct{}{}
			if err := validateStrictReviewJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if childErr := validateStrictReviewJSONValue(decoder, depth+1); childErr != nil {
				return childErr
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	closingDelimiter, ok := closing.(json.Delim)
	if !ok || delimiter == '{' && closingDelimiter != '}' || delimiter == '[' && closingDelimiter != ']' {
		return fmt.Errorf("unexpected JSON closing delimiter %q", closing)
	}
	return nil
}

func isCanonicalReviewJSONMember(name string) bool {
	switch name {
	case "schema_version", "review_id", "target", "evidence_generation", "resolved_revision", "merge_base",
		"summary", "findings", "stale", "truncated", "diagnostic", "completed_at",
		"kind", "ref", "instructions",
		"severity", "title", "explanation", "confidence", "location_state", "path", "start_line", "end_line":
		return true
	default:
		return false
	}
}

func validateReviewID(reviewID string) error {
	return codingreview.ValidateID(reviewID)
}
