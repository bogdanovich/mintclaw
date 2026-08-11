package nodes

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
)

const browserContextAuthorityDigestDomain = "mintclaw.browser.context.authority.v1\x00"

func BrowserContextAuthorityDigest(catalog BrowserContextCatalog) (string, error) {
	encoded, err := json.Marshal(catalog)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(browserContextAuthorityDigestDomain))
	_, _ = hash.Write(encoded)
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func BrowserContextAuthorityDigestMatches(digest string, catalog BrowserContextCatalog) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	expected, err := BrowserContextAuthorityDigest(catalog)
	return err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(digest)) == 1
}

func (catalog *BrowserContextCatalog) UnmarshalJSON(data []byte) error {
	var value struct {
		ID              string              `json:"context_catalog_id"`
		Generation      json.RawMessage     `json:"context_generation"`
		SelectedTabID   string              `json:"selected_tab_id"`
		SelectedFrameID string              `json:"selected_frame_id,omitempty"`
		Tabs            []browserTabContext `json:"tabs"`
		OmittedTabCount json.RawMessage     `json:"omitted_tab_count,omitempty"`
		Truncated       bool                `json:"truncated,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	generation, err := decodeCanonicalBrowserGeneration(value.Generation)
	if err != nil {
		return fmt.Errorf("decode browser context generation: %w", err)
	}
	omitted, err := decodeCanonicalBrowserGeneration(value.OmittedTabCount)
	if err != nil || omitted > uint64(^uint(0)>>1) {
		return fmt.Errorf("%w: malformed omitted browser tab count", ErrInvalidCapability)
	}
	tabs := make([]BrowserTabContext, len(value.Tabs))
	for index, tab := range value.Tabs {
		converted, convertErr := tab.value()
		if convertErr != nil {
			return convertErr
		}
		tabs[index] = converted
	}
	*catalog = BrowserContextCatalog{
		ID: value.ID, Generation: generation, SelectedTabID: value.SelectedTabID,
		SelectedFrameID: value.SelectedFrameID, Tabs: tabs, OmittedTabCount: int(omitted),
		Truncated: value.Truncated,
	}
	return nil
}

type browserTabContext struct {
	ID                 string                `json:"tab_id"`
	Kind               string                `json:"kind"`
	CreationSequence   json.RawMessage       `json:"creation_sequence"`
	OpenerTabID        string                `json:"opener_tab_id,omitempty"`
	OpenerInvocationID string                `json:"opener_invocation_id,omitempty"`
	DocumentGeneration json.RawMessage       `json:"document_generation"`
	URL                string                `json:"url"`
	Origin             string                `json:"origin"`
	Title              string                `json:"title,omitempty"`
	Frames             []browserFrameContext `json:"frames,omitempty"`
	OmittedFrameCount  json.RawMessage       `json:"omitted_frame_count,omitempty"`
	FramesTruncated    bool                  `json:"frames_truncated,omitempty"`
}

func (tab browserTabContext) value() (BrowserTabContext, error) {
	sequence, err := decodeCanonicalBrowserGeneration(tab.CreationSequence)
	if err != nil {
		return BrowserTabContext{}, fmt.Errorf("decode browser tab sequence: %w", err)
	}
	document, err := decodeCanonicalBrowserGeneration(tab.DocumentGeneration)
	if err != nil {
		return BrowserTabContext{}, fmt.Errorf("decode browser tab document generation: %w", err)
	}
	omitted, err := decodeCanonicalBrowserGeneration(tab.OmittedFrameCount)
	if err != nil || omitted > uint64(^uint(0)>>1) {
		return BrowserTabContext{}, fmt.Errorf("%w: malformed omitted browser frame count", ErrInvalidCapability)
	}
	frames := make([]BrowserFrameContext, len(tab.Frames))
	for index, frame := range tab.Frames {
		converted, convertErr := frame.value()
		if convertErr != nil {
			return BrowserTabContext{}, convertErr
		}
		frames[index] = converted
	}
	return BrowserTabContext{
		ID: tab.ID, Kind: tab.Kind, CreationSequence: sequence,
		OpenerTabID: tab.OpenerTabID, OpenerInvocationID: tab.OpenerInvocationID,
		DocumentGeneration: document, URL: tab.URL, Origin: tab.Origin, Title: tab.Title,
		Frames: frames, OmittedFrameCount: int(omitted), FramesTruncated: tab.FramesTruncated,
	}, nil
}

type browserFrameContext struct {
	ID                 string          `json:"frame_id"`
	ParentFrameID      string          `json:"parent_frame_id,omitempty"`
	CreationSequence   json.RawMessage `json:"creation_sequence"`
	Depth              json.RawMessage `json:"depth"`
	DocumentGeneration json.RawMessage `json:"document_generation"`
	URL                string          `json:"url"`
	Origin             string          `json:"origin"`
	Label              string          `json:"label,omitempty"`
	Availability       string          `json:"availability"`
	SafeFailure        string          `json:"safe_failure,omitempty"`
}

func (frame browserFrameContext) value() (BrowserFrameContext, error) {
	sequence, err := decodeCanonicalBrowserGeneration(frame.CreationSequence)
	if err != nil {
		return BrowserFrameContext{}, fmt.Errorf("decode browser frame sequence: %w", err)
	}
	depth, err := decodeCanonicalBrowserGeneration(frame.Depth)
	if err != nil || depth < 1 || depth > 8 {
		return BrowserFrameContext{}, fmt.Errorf("%w: malformed browser frame depth", ErrInvalidCapability)
	}
	document, err := decodeCanonicalBrowserGeneration(frame.DocumentGeneration)
	if err != nil {
		return BrowserFrameContext{}, fmt.Errorf("decode browser frame document generation: %w", err)
	}
	return BrowserFrameContext{
		ID: frame.ID, ParentFrameID: frame.ParentFrameID, CreationSequence: sequence,
		Depth: int(depth), DocumentGeneration: document, URL: frame.URL, Origin: frame.Origin,
		Label: frame.Label, Availability: frame.Availability, SafeFailure: frame.SafeFailure,
	}, nil
}

func (input *BrowserContextInput) UnmarshalJSON(data []byte) error {
	var value struct {
		SessionID         string          `json:"session_id"`
		ProfileRevision   string          `json:"profile_revision"`
		Operation         string          `json:"operation"`
		RequestID         string          `json:"request_id"`
		ContextCatalogID  string          `json:"context_catalog_id,omitempty"`
		ContextGeneration json.RawMessage `json:"context_generation,omitempty"`
		AuthorityDigest   string          `json:"authority_digest,omitempty"`
		AuthorityBytes    json.RawMessage `json:"authority_bytes,omitempty"`
		TabID             string          `json:"tab_id,omitempty"`
		FrameID           string          `json:"frame_id,omitempty"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	authorityBytes, err := decodeCanonicalBrowserGeneration(value.AuthorityBytes)
	if err != nil || authorityBytes > MaxBrowserContextInputBytes {
		return fmt.Errorf("%w: malformed browser context authority bytes", ErrInvalidCapability)
	}
	contextGeneration, err := decodeCanonicalBrowserGeneration(value.ContextGeneration)
	if err != nil {
		return fmt.Errorf("decode browser context authority generation: %w", err)
	}
	*input = BrowserContextInput{
		SessionID: value.SessionID, ProfileRevision: value.ProfileRevision,
		Operation: value.Operation, RequestID: value.RequestID,
		ContextCatalogID: value.ContextCatalogID, ContextGeneration: contextGeneration,
		AuthorityDigest: value.AuthorityDigest, AuthorityBytes: int(authorityBytes),
		TabID: value.TabID, FrameID: value.FrameID,
	}
	return nil
}
