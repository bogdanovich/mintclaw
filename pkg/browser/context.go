package browser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

const (
	MaxContextTabs         = 16
	MaxContextFramesPerTab = 64
	MaxContextFrameDepth   = 8
	MaxContextCatalogBytes = 64 * 1024
	MaxContextLabelBytes   = 512
)

type TabKind string

const (
	TabPrimary TabKind = "primary"
	TabOpened  TabKind = "tab"
	TabPopup   TabKind = "popup"
)

func (kind TabKind) Valid() bool {
	switch kind {
	case TabPrimary, TabOpened, TabPopup:
		return true
	default:
		return false
	}
}

type FrameAvailability string

const (
	FrameReady       FrameAvailability = "ready"
	FrameUnavailable FrameAvailability = "unavailable"
)

func (availability FrameAvailability) Valid() bool {
	return availability == FrameReady || availability == FrameUnavailable
}

// FrameContext is a bounded durable projection of one attached child frame.
// Driver handles remain private live-worker state.
type FrameContext struct {
	ID                 string            `json:"frame_id"`
	ParentFrameID      string            `json:"parent_frame_id,omitempty"`
	CreationSequence   uint64            `json:"creation_sequence"`
	Depth              int               `json:"depth"`
	DocumentGeneration uint64            `json:"document_generation"`
	URL                string            `json:"url"`
	Origin             string            `json:"origin"`
	Label              string            `json:"label,omitempty"`
	Availability       FrameAvailability `json:"availability"`
	SafeFailure        string            `json:"safe_failure,omitempty"`
}

// TabContext is a bounded durable projection of one live page.
type TabContext struct {
	ID                 string         `json:"tab_id"`
	Kind               TabKind        `json:"kind"`
	CreationSequence   uint64         `json:"creation_sequence"`
	OpenerTabID        string         `json:"opener_tab_id,omitempty"`
	OpenerInvocationID string         `json:"opener_invocation_id,omitempty"`
	DocumentGeneration uint64         `json:"document_generation"`
	URL                string         `json:"url"`
	Origin             string         `json:"origin"`
	Title              string         `json:"title,omitempty"`
	Frames             []FrameContext `json:"frames,omitempty"`
	OmittedFrameCount  int            `json:"omitted_frame_count,omitempty"`
	FramesTruncated    bool           `json:"frames_truncated,omitempty"`
}

// ContextCatalog is the model-safe authority for selecting a tab or frame.
// It contains no raw driver identifiers.
type ContextCatalog struct {
	ID              string       `json:"context_catalog_id"`
	Generation      uint64       `json:"context_generation"`
	SelectedTabID   string       `json:"selected_tab_id"`
	SelectedFrameID string       `json:"selected_frame_id,omitempty"`
	Tabs            []TabContext `json:"tabs"`
	OmittedTabCount int          `json:"omitted_tab_count,omitempty"`
	Truncated       bool         `json:"truncated,omitempty"`
}

func (catalog ContextCatalog) Validate() error {
	if !validIdentifier(catalog.ID) || catalog.Generation == 0 ||
		!validIdentifier(catalog.SelectedTabID) ||
		(catalog.SelectedFrameID != "" && !validIdentifier(catalog.SelectedFrameID)) ||
		len(catalog.Tabs) == 0 || len(catalog.Tabs) > MaxContextTabs ||
		catalog.OmittedTabCount < 0 || catalog.Truncated != (catalog.OmittedTabCount > 0) {
		return fmt.Errorf("%w: malformed browser context catalog", ErrInvalid)
	}
	tabIDs := make(map[string]struct{}, len(catalog.Tabs))
	tabSequences := make(map[string]uint64, len(catalog.Tabs))
	frameIDs := make(map[string]struct{})
	selectedTabFound := false
	selectedFrameFound := catalog.SelectedFrameID == ""
	var previousTabSequence uint64
	primaryCount := 0
	for _, tab := range catalog.Tabs {
		if err := tab.validate(tabIDs, frameIDs, previousTabSequence); err != nil {
			return err
		}
		previousTabSequence = tab.CreationSequence
		tabIDs[tab.ID] = struct{}{}
		tabSequences[tab.ID] = tab.CreationSequence
		if tab.Kind == TabPrimary {
			primaryCount++
		}
		if tab.ID == catalog.SelectedTabID {
			selectedTabFound = true
			if catalog.SelectedFrameID != "" {
				for _, frame := range tab.Frames {
					if frame.ID == catalog.SelectedFrameID && frame.Availability == FrameReady {
						selectedFrameFound = true
					}
				}
			}
		}
	}
	for _, tab := range catalog.Tabs {
		if tab.Kind != TabPopup {
			continue
		}
		if openerSequence, openerIsLive := tabSequences[tab.OpenerTabID]; openerIsLive &&
			openerSequence >= tab.CreationSequence {
			return fmt.Errorf("%w: popup browser tab has an invalid live opener", ErrInvalid)
		}
	}
	if !selectedTabFound || !selectedFrameFound || primaryCount > 1 {
		return fmt.Errorf("%w: invalid selected browser context", ErrInvalid)
	}
	encoded, err := json.Marshal(catalog)
	if err != nil || len(encoded) > MaxContextCatalogBytes {
		return fmt.Errorf("%w: browser context catalog exceeds its bound", ErrInvalid)
	}
	return nil
}

func (tab TabContext) validate(
	tabIDs map[string]struct{},
	frameIDs map[string]struct{},
	previousSequence uint64,
) error {
	if !validIdentifier(tab.ID) || !tab.Kind.Valid() || tab.CreationSequence == 0 ||
		tab.CreationSequence <= previousSequence || tab.DocumentGeneration == 0 ||
		(tab.OpenerTabID != "" && !validIdentifier(tab.OpenerTabID)) ||
		(tab.OpenerInvocationID != "" && !validIdentifier(tab.OpenerInvocationID)) ||
		len(tab.Frames) > MaxContextFramesPerTab || tab.OmittedFrameCount < 0 ||
		tab.FramesTruncated != (tab.OmittedFrameCount > 0) ||
		!validContextLocation(tab.URL, tab.Origin) || !validContextLabel(tab.Title) {
		return fmt.Errorf("%w: malformed browser tab context", ErrInvalid)
	}
	if _, duplicate := tabIDs[tab.ID]; duplicate {
		return fmt.Errorf("%w: duplicate browser tab context", ErrInvalid)
	}
	if tab.Kind == TabPrimary && (tab.OpenerTabID != "" || tab.OpenerInvocationID != "") {
		return fmt.Errorf("%w: primary browser tab has an opener", ErrInvalid)
	}
	if tab.Kind == TabPopup && (tab.OpenerTabID == "" || tab.OpenerInvocationID == "") {
		return fmt.Errorf("%w: popup browser tab lacks correlation", ErrInvalid)
	}
	if tab.Kind != TabPopup && (tab.OpenerTabID != "" || tab.OpenerInvocationID != "") {
		return fmt.Errorf("%w: non-popup browser tab has popup correlation", ErrInvalid)
	}
	parents := make(map[string]FrameContext, len(tab.Frames))
	var previousFrameSequence uint64
	for _, frame := range tab.Frames {
		if !validIdentifier(frame.ID) || frame.CreationSequence == 0 ||
			frame.CreationSequence <= previousFrameSequence || frame.Depth < 1 ||
			frame.Depth > MaxContextFrameDepth || frame.DocumentGeneration == 0 ||
			!frame.Availability.Valid() || !validContextLocation(frame.URL, frame.Origin) ||
			!validContextLabel(frame.Label) {
			return fmt.Errorf("%w: malformed browser frame context", ErrInvalid)
		}
		if _, duplicate := frameIDs[frame.ID]; duplicate {
			return fmt.Errorf("%w: duplicate browser frame context", ErrInvalid)
		}
		if frame.ParentFrameID == "" {
			if frame.Depth != 1 {
				return fmt.Errorf("%w: malformed browser frame depth", ErrInvalid)
			}
		} else {
			parent, ok := parents[frame.ParentFrameID]
			if !ok || frame.Depth != parent.Depth+1 {
				return fmt.Errorf("%w: malformed browser frame parent", ErrInvalid)
			}
		}
		if (frame.Availability == FrameReady) != (frame.SafeFailure == "") ||
			(frame.SafeFailure != "" && !safeFailureRegexp.MatchString(frame.SafeFailure)) {
			return fmt.Errorf("%w: malformed browser frame availability", ErrInvalid)
		}
		parents[frame.ID] = frame
		frameIDs[frame.ID] = struct{}{}
		previousFrameSequence = frame.CreationSequence
	}
	return nil
}

func validContextLocation(rawURL, origin string) bool {
	if rawURL == initialBlankOrigin || origin == initialBlankOrigin {
		return rawURL == initialBlankOrigin && origin == initialBlankOrigin
	}
	if rawURL == "" || len(rawURL) > MaxURLBytes || len(origin) > MaxURLBytes ||
		strings.TrimSpace(rawURL) != rawURL || strings.TrimSpace(origin) != origin {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return false
	}
	normalizedOrigin, err := config.NormalizeBrowserHTTPOrigin(origin)
	if err != nil || normalizedOrigin != origin {
		return false
	}
	actualOrigin, err := originFromURL(rawURL)
	return err == nil && actualOrigin == origin
}

func validContextLabel(value string) bool {
	if len(value) > MaxContextLabelBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validContextBinding(frameID, catalogID string, generation uint64) bool {
	if frameID == "" && catalogID == "" && generation == 0 {
		return true
	}
	return validIdentifier(catalogID) && generation != 0 &&
		(frameID == "" || validIdentifier(frameID))
}

func sessionMatchesContextBinding(
	session Session,
	frameID string,
	catalogID string,
	generation uint64,
) bool {
	if session.ContextAuthority == nil {
		return frameID == "" && catalogID == "" && generation == 0
	}
	return frameID == session.FrameID && catalogID == session.ContextAuthority.ID &&
		generation == session.ContextAuthority.Generation
}

func cloneContextCatalog(catalog ContextCatalog) ContextCatalog {
	cloned := catalog
	if catalog.Tabs == nil {
		return cloned
	}
	cloned.Tabs = make([]TabContext, len(catalog.Tabs))
	for index, tab := range catalog.Tabs {
		cloned.Tabs[index] = tab
		cloned.Tabs[index].Frames = append([]FrameContext(nil), tab.Frames...)
	}
	return cloned
}
