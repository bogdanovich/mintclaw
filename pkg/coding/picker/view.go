// Package picker defines the bounded current page consumed by the standalone
// pre-controller resume picker. It is separate from active-thread frontend
// presentation and from durable catalog implementation details.
package picker

import (
	"context"
	"time"
)

// Location is a read-only location observation used for discovery. The resume
// admission path must inspect the selected metadata again under lease.
type Location string

const (
	LocationAvailable Location = "available"
	LocationMissing   Location = "missing"
	LocationMoved     Location = "moved"
	LocationUnknown   Location = "unknown"
)

// Query requests one bounded picker page. AllProjects is the only way to widen
// discovery beyond the current canonical project.
type Query struct {
	AllProjects bool
	Archived    bool
	Search      string
	Offset      int
	Limit       int
}

// Item is presentation-safe metadata and moment-in-time project state. Locked
// and available observations are hints only; AcquireLease remains the writer
// admission authority.
type Item struct {
	ThreadID        string
	Title           string
	Preview         string
	MatchKind       string
	MatchSnippet    string
	MatchedAt       time.Time
	MatchedMessage  int
	UpdatedAt       time.Time
	ProjectRoot     string
	InvocationCWD   string
	Branch          string
	CurrentProject  bool
	Location        Location
	RepositoryKnown bool
	Dirty           bool
	Stale           bool
	Locked          bool
	LockOwnerPID    int
	LockOwnerHost   string
	StateIncomplete bool
}

// Page carries bounded catalog diagnostics without exposing raw
// corrupt-entry content.
type Page struct {
	Items                 []Item
	SkippedTotal          int
	Scanned               int
	Matched               int
	ScanTruncated         bool
	HasMore               bool
	NextOffset            int
	ContentThreadsScanned int
	ContentBytesScanned   int64
	ContentScanTruncated  bool
}

// Source keeps terminal code independent of the durable thread catalog,
// filesystem inspection, Git capture, and lease implementation.
type Source interface {
	Page(context.Context, Query) (Page, error)
}
