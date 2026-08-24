package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

type DiagnosticCategory string

const (
	DiagnosticConsoleErrors  DiagnosticCategory = "console_errors"
	DiagnosticFailedRequests DiagnosticCategory = "failed_requests"
	DiagnosticPageCrashes    DiagnosticCategory = "page_crashes"

	MaxDiagnosticEntriesPerCategory = 32
	MaxDiagnosticCategoryBytes      = 16 * 1024
	MaxDiagnosticResultBytes        = 48 * 1024
)

var diagnosticCategoryOrder = []DiagnosticCategory{
	DiagnosticConsoleErrors,
	DiagnosticFailedRequests,
	DiagnosticPageCrashes,
}

func (category DiagnosticCategory) Valid() bool {
	return slices.Contains(diagnosticCategoryOrder, category)
}

func NormalizeDiagnosticCategories(categories []DiagnosticCategory) ([]DiagnosticCategory, error) {
	if len(categories) == 0 || len(categories) > len(diagnosticCategoryOrder) {
		return nil, ErrInvalid
	}
	seen := make(map[DiagnosticCategory]struct{}, len(categories))
	for _, category := range categories {
		if !category.Valid() {
			return nil, ErrInvalid
		}
		if _, duplicate := seen[category]; duplicate {
			return nil, ErrInvalid
		}
		seen[category] = struct{}{}
	}
	normalized := make([]DiagnosticCategory, 0, len(categories))
	for _, category := range diagnosticCategoryOrder {
		if _, ok := seen[category]; ok {
			normalized = append(normalized, category)
		}
	}
	return normalized, nil
}

type DiagnosticEntry struct {
	Timestamp     int64  `json:"timestamp"`
	Severity      string `json:"severity,omitempty"`
	ResourceClass string `json:"resource_class,omitempty"`
	FailureCode   string `json:"failure_code,omitempty"`
	Origin        string `json:"origin,omitempty"`
	Path          string `json:"path,omitempty"`
	Line          int    `json:"line,omitempty"`
	MessageHash   string `json:"message_hash,omitempty"`
}

type DiagnosticCategorySummary struct {
	Category     DiagnosticCategory `json:"category"`
	Count        int                `json:"count"`
	OmittedCount int                `json:"omitted_count"`
	Truncated    bool               `json:"truncated"`
	Entries      []DiagnosticEntry  `json:"entries"`
}

type DiagnosticSummary struct {
	SessionID          string                      `json:"browser_session_id,omitempty"`
	TabID              string                      `json:"tab_id,omitempty"`
	FrameID            string                      `json:"frame_id,omitempty"`
	ContextCatalogID   string                      `json:"context_catalog_id,omitempty"`
	ContextGeneration  uint64                      `json:"context_generation,omitempty"`
	SnapshotID         string                      `json:"snapshot_id,omitempty"`
	SnapshotGeneration uint64                      `json:"snapshot_generation,omitempty"`
	Categories         []DiagnosticCategorySummary `json:"categories"`
	Truncated          bool                        `json:"truncated"`
}

type DiagnosticsRequest struct {
	Owner              Owner
	SessionID          string
	TabID              string
	FrameID            string
	ContextCatalogID   string
	ContextGeneration  uint64
	SnapshotID         string
	SnapshotGeneration uint64
	Categories         []DiagnosticCategory
}

func validateDiagnosticsRequest(request DiagnosticsRequest) error {
	if request.Owner.Validate() != nil || !validIdentifier(request.SessionID) {
		return ErrInvalid
	}
	if _, err := NormalizeDiagnosticCategories(request.Categories); err != nil {
		return err
	}
	bound := request.TabID != "" || request.FrameID != "" || request.ContextCatalogID != "" ||
		request.ContextGeneration != 0 || request.SnapshotID != "" || request.SnapshotGeneration != 0
	if !bound {
		return nil
	}
	if !validIdentifier(request.TabID) || !validIdentifier(request.SnapshotID) ||
		request.SnapshotGeneration == 0 ||
		!validContextBinding(request.FrameID, request.ContextCatalogID, request.ContextGeneration) {
		return ErrInvalid
	}
	return nil
}

func (broker *Broker) Diagnostics(
	ctx context.Context,
	request DiagnosticsRequest,
) (DiagnosticSummary, error) {
	if err := validateDiagnosticsRequest(request); err != nil {
		return DiagnosticSummary{}, fmt.Errorf("%w: malformed diagnostics request", err)
	}
	categories, _ := NormalizeDiagnosticCategories(request.Categories)
	broker.mu.Lock()
	defer broker.mu.Unlock()
	session, err := broker.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return DiagnosticSummary{}, err
	}
	if !session.Owner.Equal(request.Owner) {
		return DiagnosticSummary{}, ErrNotFound
	}
	tabID := session.TabID
	if request.TabID != "" {
		tabID = request.TabID
	}
	session, _, actionWorker, err := broker.actionSessionLocked(
		ctx, request.Owner, request.SessionID, tabID,
	)
	if err != nil {
		return DiagnosticSummary{}, err
	}
	if request.TabID != "" &&
		(session.SnapshotID != request.SnapshotID ||
			session.SnapshotGeneration != request.SnapshotGeneration ||
			!sessionMatchesContextBinding(
				session, request.FrameID, request.ContextCatalogID, request.ContextGeneration,
			)) {
		return DiagnosticSummary{}, ErrStale
	}
	worker, ok := actionWorker.(DiagnosticsWorker)
	if !ok {
		return DiagnosticSummary{}, ErrDriverIncompatible
	}
	summary, err := worker.Diagnostics(ctx, categories)
	if err != nil {
		if errors.Is(err, ErrWorkerUnavailable) || errors.Is(err, ErrDriverIncompatible) {
			return DiagnosticSummary{}, broker.handleObservationErrorLocked(ctx, session, err)
		}
		return DiagnosticSummary{}, err
	}
	summary.SessionID = session.ID
	summary.TabID = session.TabID
	summary.FrameID = session.FrameID
	if session.ContextAuthority != nil {
		summary.ContextCatalogID = session.ContextAuthority.ID
		summary.ContextGeneration = session.ContextAuthority.Generation
	}
	summary.SnapshotID = session.SnapshotID
	summary.SnapshotGeneration = session.SnapshotGeneration
	if err = ValidateDiagnosticSummary(summary, categories); err != nil {
		return DiagnosticSummary{}, errors.Join(ErrDriverIncompatible, err)
	}
	return summary, nil
}

// ValidateDiagnosticSummary enforces the privacy and boundedness contract at
// every driver, broker, and companion boundary.
func ValidateDiagnosticSummary(summary DiagnosticSummary, requested []DiagnosticCategory) error {
	if len(summary.Categories) != len(requested) || len(summary.Categories) > len(diagnosticCategoryOrder) {
		return ErrInvalid
	}
	encoded, err := json.Marshal(summary)
	if err != nil || len(encoded) > MaxDiagnosticResultBytes {
		return ErrInvalid
	}
	anyTruncated := false
	for index, category := range summary.Categories {
		if category.Category != requested[index] || category.Count < 0 || category.OmittedCount < 0 ||
			category.Count != len(category.Entries)+category.OmittedCount ||
			category.Truncated != (category.OmittedCount > 0) ||
			len(category.Entries) > MaxDiagnosticEntriesPerCategory {
			return ErrInvalid
		}
		categoryBytes, marshalErr := json.Marshal(category)
		if marshalErr != nil || len(categoryBytes) > MaxDiagnosticCategoryBytes {
			return ErrInvalid
		}
		anyTruncated = anyTruncated || category.Truncated
		for _, entry := range category.Entries {
			if entry.Timestamp <= 0 || !validDiagnosticEntry(category.Category, entry) ||
				len(entry.Severity) > 16 || len(entry.ResourceClass) > 32 ||
				len(entry.FailureCode) > 64 || len(entry.Origin) > MaxURLBytes ||
				len(entry.Path) > MaxURLBytes || entry.Line < 0 ||
				(entry.MessageHash != "" && !validDigest(entry.MessageHash)) ||
				!validDiagnosticLocation(entry.Origin, entry.Path) {
				return ErrInvalid
			}
		}
	}
	if summary.Truncated != anyTruncated {
		return ErrInvalid
	}
	return nil
}

func validDiagnosticEntry(category DiagnosticCategory, entry DiagnosticEntry) bool {
	switch category {
	case DiagnosticConsoleErrors:
		return (entry.Severity == "error" || entry.Severity == "warning") &&
			entry.ResourceClass == "" && entry.FailureCode == "" && entry.MessageHash != ""
	case DiagnosticFailedRequests:
		return entry.Severity == "" && validDiagnosticResourceClass(entry.ResourceClass) &&
			(entry.FailureCode == "network_failed" || entry.FailureCode == "canceled" ||
				entry.FailureCode == "blocked" || entry.FailureCode == "http_error") &&
			entry.Line == 0 && entry.MessageHash != ""
	case DiagnosticPageCrashes:
		return entry.Severity == "" && entry.ResourceClass == "" && entry.FailureCode == "page_crashed" &&
			entry.Origin == "" && entry.Path == "" && entry.Line == 0 && entry.MessageHash == ""
	default:
		return false
	}
}

func validDiagnosticResourceClass(value string) bool {
	return slices.Contains([]string{
		"document", "stylesheet", "image", "media", "font", "script", "texttrack", "xhr",
		"fetch", "eventsource", "websocket", "manifest", "other",
	}, value)
}

func validDiagnosticLocation(origin, path string) bool {
	if origin == "" || path == "" {
		return origin == "" && path == ""
	}
	if strings.ContainsAny(origin+path, "?#") || !strings.HasPrefix(path, "/") {
		return false
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" &&
		parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}
