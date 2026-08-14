package gateway

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bogdanovich/mintclaw/pkg/browser"
	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func (worker *nodeBrowserWorker) ContextCatalog(ctx context.Context) (browser.ContextCatalog, error) {
	result, err := worker.invokeContext(ctx, "list", nil)
	if err != nil {
		return browser.ContextCatalog{}, err
	}
	return gatewayBrowserContextCatalog(result.Catalog)
}

func (worker *nodeBrowserWorker) OpenTab(ctx context.Context) (browser.ContextCatalog, error) {
	result, err := worker.invokeContext(ctx, "open", nil)
	if err != nil {
		return browser.ContextCatalog{}, err
	}
	worker.clearContextObservation()
	return gatewayBrowserContextCatalog(result.Catalog)
}

func (worker *nodeBrowserWorker) SelectContext(
	ctx context.Context,
	authority browser.ContextMutationAuthority,
) (browser.DriverObservation, browser.ContextCatalog, error) {
	worker.mu.Lock()
	preSelectGeneration := worker.snapshotGeneration
	worker.mu.Unlock()
	binding, err := authority.Binding()
	if err != nil {
		return browser.DriverObservation{}, browser.ContextCatalog{}, err
	}
	result, err := worker.invokeContext(ctx, "select", &binding)
	if err != nil {
		return browser.DriverObservation{}, browser.ContextCatalog{}, err
	}
	if result.ProtectedResult {
		// The select mutation is already proven accepted. Refresh live page
		// authority without replaying it; invokeContext has independently
		// refreshed the catalog through a new read-only list invocation.
		worker.mu.Lock()
		if worker.closed || worker.snapshotGeneration != preSelectGeneration {
			worker.mu.Unlock()
			return browser.DriverObservation{}, browser.ContextCatalog{}, browser.ErrStale
		}
		worker.snapshotGeneration = preSelectGeneration + 1
		worker.cachedObservation = nil
		worker.elements = make(map[string]browser.DriverElement)
		worker.currentOrigin = ""
		worker.mu.Unlock()
		observation, observeErr := worker.Observe(ctx)
		if observeErr != nil {
			return browser.DriverObservation{}, browser.ContextCatalog{}, observeErr
		}
		catalog, catalogErr := gatewayBrowserContextCatalog(result.Catalog)
		return observation, catalog, catalogErr
	}
	if result.Observation == nil {
		return browser.DriverObservation{}, browser.ContextCatalog{}, browser.ErrDriverIncompatible
	}
	worker.mu.Lock()
	expectedGeneration := worker.snapshotGeneration + 1
	worker.mu.Unlock()
	observation, err := worker.acceptObservation(*result.Observation, expectedGeneration)
	if err != nil {
		return browser.DriverObservation{}, browser.ContextCatalog{}, err
	}
	catalog, err := gatewayBrowserContextCatalog(result.Catalog)
	return observation, catalog, err
}

func (worker *nodeBrowserWorker) CloseTab(
	ctx context.Context,
	authority browser.ContextMutationAuthority,
) (browser.ContextCatalog, error) {
	binding, err := authority.Binding()
	if err != nil {
		return browser.ContextCatalog{}, err
	}
	result, err := worker.invokeContext(ctx, "close", &binding)
	if err != nil {
		return browser.ContextCatalog{}, err
	}
	worker.clearContextObservation()
	return gatewayBrowserContextCatalog(result.Catalog)
}

func (worker *nodeBrowserWorker) invokeContext(
	ctx context.Context,
	operation string,
	binding *browser.ContextMutationBinding,
) (nodes.BrowserContextResult, error) {
	result, err := worker.invokeContextOnce(ctx, operation, binding)
	if err != nil || !result.ProtectedResult {
		return result, err
	}
	// The requested operation completed remotely and must not be replayed.
	// Recover only its live catalog with fresh read-only list identities.
	for attempts := 0; attempts < 10; attempts++ {
		fresh, freshErr := worker.invokeContextOnce(ctx, "list", nil)
		if freshErr != nil {
			return nodes.BrowserContextResult{}, freshErr
		}
		if fresh.ProtectedResult {
			continue
		}
		if operation == "list" {
			return fresh, nil
		}
		return nodes.BrowserContextResult{
			Operation: operation, Catalog: fresh.Catalog, ProtectedResult: true,
		}, nil
	}
	return nodes.BrowserContextResult{}, browser.ErrWorkerUnavailable
}

func (worker *nodeBrowserWorker) invokeContextOnce(
	ctx context.Context,
	operation string,
	binding *browser.ContextMutationBinding,
) (nodes.BrowserContextResult, error) {
	worker.mu.Lock()
	if worker.closed {
		worker.mu.Unlock()
		return nodes.BrowserContextResult{}, browser.ErrWorkerUnavailable
	}
	worker.contextSequence++
	sequence := worker.contextSequence
	worker.mu.Unlock()
	descriptor, _, err := worker.resolveAuthority(nodes.BrowserCommandContexts)
	if err != nil {
		return nodes.BrowserContextResult{}, err
	}
	input := nodes.BrowserContextInput{
		SessionID: worker.sessionID, ProfileRevision: worker.profileRevision,
		Operation: operation,
		RequestID: browserNodeStableID("context", worker.sessionID, operation, fmt.Sprint(sequence)),
	}
	var ephemeralInput json.RawMessage
	if binding != nil {
		authority := gatewayNodeContextCatalog(binding.Catalog)
		ephemeralInput, err = json.Marshal(struct {
			Authority nodes.BrowserContextCatalog `json:"authority"`
		}{Authority: authority})
		if err != nil || len(ephemeralInput) > nodes.MaxBrowserContextInputBytes {
			return nodes.BrowserContextResult{}, browser.ErrInvalid
		}
		input.AuthorityDigest, err = nodes.BrowserContextAuthorityDigest(authority)
		if err != nil {
			return nodes.BrowserContextResult{}, browser.ErrInvalid
		}
		input.AuthorityBytes = len(ephemeralInput)
		input.ContextCatalogID = authority.ID
		input.ContextGeneration = authority.Generation
		input.TabID = binding.TabID
		input.FrameID = binding.FrameID
	}
	var result nodes.BrowserContextResult
	if err = worker.invokeWithEphemeral(
		ctx, descriptor, fmt.Sprintf("context_%s_%d", operation, sequence), input, ephemeralInput, &result,
	); err != nil {
		return nodes.BrowserContextResult{}, err
	}
	if result.Operation != operation {
		return nodes.BrowserContextResult{}, browser.ErrDriverIncompatible
	}
	return result, nil
}

func (worker *nodeBrowserWorker) clearContextObservation() {
	worker.mu.Lock()
	worker.cachedObservation = nil
	worker.elements = make(map[string]browser.DriverElement)
	worker.currentOrigin = ""
	worker.mu.Unlock()
}

func gatewayBrowserContextCatalog(value nodes.BrowserContextCatalog) (browser.ContextCatalog, error) {
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

func gatewayNodeContextCatalog(catalog browser.ContextCatalog) nodes.BrowserContextCatalog {
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
