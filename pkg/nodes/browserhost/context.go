package browserhost

import (
	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func browserContextMutationBinding(
	request nodes.BrowserHostContextRequest,
) (browser.ContextMutationBinding, error) {
	if request.Authority == nil {
		return browser.ContextMutationBinding{}, browser.ErrInvalid
	}
	catalog, err := browserContextCatalogValue(*request.Authority)
	if err != nil {
		return browser.ContextMutationBinding{}, err
	}
	return browser.ContextMutationBinding{
		Catalog: catalog, TabID: request.TabID, FrameID: request.FrameID,
	}, nil
}

func browserContextCatalogValue(value nodes.BrowserContextCatalog) (browser.ContextCatalog, error) {
	tabs := make([]browser.TabContext, len(value.Tabs))
	for tabIndex, tab := range value.Tabs {
		frames := make([]browser.FrameContext, len(tab.Frames))
		for frameIndex, frame := range tab.Frames {
			frames[frameIndex] = browser.FrameContext{
				ID: frame.ID, ParentFrameID: frame.ParentFrameID,
				CreationSequence: frame.CreationSequence, Depth: frame.Depth,
				DocumentGeneration: frame.DocumentGeneration,
				URL:                frame.URL, Origin: frame.Origin, Label: frame.Label,
				Availability: browser.FrameAvailability(frame.Availability), SafeFailure: frame.SafeFailure,
			}
		}
		tabs[tabIndex] = browser.TabContext{
			ID: tab.ID, Kind: browser.TabKind(tab.Kind), CreationSequence: tab.CreationSequence,
			OpenerTabID: tab.OpenerTabID, OpenerInvocationID: tab.OpenerInvocationID,
			DocumentGeneration: tab.DocumentGeneration,
			URL:                tab.URL, Origin: tab.Origin, Title: tab.Title, Frames: frames,
			OmittedFrameCount: tab.OmittedFrameCount, FramesTruncated: tab.FramesTruncated,
		}
	}
	catalog := browser.ContextCatalog{
		ID: value.ID, Generation: value.Generation,
		SelectedTabID: value.SelectedTabID, SelectedFrameID: value.SelectedFrameID,
		Tabs: tabs, OmittedTabCount: value.OmittedTabCount, Truncated: value.Truncated,
	}
	return catalog, catalog.Validate()
}

func browserContextCatalogResult(catalog browser.ContextCatalog) nodes.BrowserContextCatalog {
	tabs := make([]nodes.BrowserTabContext, len(catalog.Tabs))
	for tabIndex, tab := range catalog.Tabs {
		frames := make([]nodes.BrowserFrameContext, len(tab.Frames))
		for frameIndex, frame := range tab.Frames {
			frames[frameIndex] = nodes.BrowserFrameContext{
				ID: frame.ID, ParentFrameID: frame.ParentFrameID,
				CreationSequence: frame.CreationSequence, Depth: frame.Depth,
				DocumentGeneration: frame.DocumentGeneration,
				URL:                frame.URL, Origin: frame.Origin, Label: frame.Label,
				Availability: string(frame.Availability), SafeFailure: frame.SafeFailure,
			}
		}
		tabs[tabIndex] = nodes.BrowserTabContext{
			ID: tab.ID, Kind: string(tab.Kind), CreationSequence: tab.CreationSequence,
			OpenerTabID: tab.OpenerTabID, OpenerInvocationID: tab.OpenerInvocationID,
			DocumentGeneration: tab.DocumentGeneration,
			URL:                tab.URL, Origin: tab.Origin, Title: tab.Title, Frames: frames,
			OmittedFrameCount: tab.OmittedFrameCount, FramesTruncated: tab.FramesTruncated,
		}
	}
	return nodes.BrowserContextCatalog{
		ID: catalog.ID, Generation: catalog.Generation,
		SelectedTabID: catalog.SelectedTabID, SelectedFrameID: catalog.SelectedFrameID,
		Tabs: tabs, OmittedTabCount: catalog.OmittedTabCount, Truncated: catalog.Truncated,
	}
}
