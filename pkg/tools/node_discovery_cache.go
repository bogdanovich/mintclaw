package tools

type requestScopedNodeLookup struct {
	record      NodeDiscoveryRecord
	found       bool
	err         error
	catalogHash string
}

// requestScopedNodeInvocationSource keeps discovery stable for one broker
// request while forwarding durable invocation operations to the live source.
// Its unexported catalog evidence can only be produced and consumed inside the
// tools package, so callers cannot bypass canonical catalog validation.
type requestScopedNodeInvocationSource struct {
	NodeInvocationSource
	lookups map[string]requestScopedNodeLookup
}

// NewRequestScopedNodeInvocationSource memoizes node discovery and its
// canonical catalog hash for one request. Construct a fresh wrapper for every
// independent request so connection and policy changes remain observable.
func NewRequestScopedNodeInvocationSource(source NodeInvocationSource) NodeInvocationSource {
	if source == nil {
		return nil
	}
	return &requestScopedNodeInvocationSource{
		NodeInvocationSource: source,
		lookups:              make(map[string]requestScopedNodeLookup),
	}
}

func (source *requestScopedNodeInvocationSource) Lookup(
	nodeRef string,
) (NodeDiscoveryRecord, bool, error) {
	if cached, exists := source.lookups[nodeRef]; exists {
		return cached.record, cached.found, cached.err
	}
	record, found, err := source.NodeInvocationSource.Lookup(nodeRef)
	lookup := requestScopedNodeLookup{record: record, found: found, err: err}
	if err == nil && found {
		lookup.catalogHash, _ = record.Snapshot.Catalog.Hash()
	}
	source.lookups[nodeRef] = lookup
	return record, found, err
}

func (source *requestScopedNodeInvocationSource) validatedCatalogHash(nodeRef string) (string, bool) {
	lookup, exists := source.lookups[nodeRef]
	return lookup.catalogHash, exists && lookup.err == nil && lookup.found && lookup.catalogHash != ""
}
