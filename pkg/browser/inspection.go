package browser

import "errors"

// StateSnapshot is validated persisted structure, not authorization, a live
// broker observation, or independent proof of a page's contents or side effects.
type StateSnapshot struct {
	Sessions        map[string]Session
	PreparedActions map[string]PreparedAction
	Invocations     map[string]Invocation
}

// InspectStateSnapshot validates the exact supplied bytes using the native
// store decoder. It performs no I/O, takes no lock, and cannot control resources.
// Callers own raw-surface privacy scanning and must bind selected records to
// their admitted owner, execution, profile, and operation before using evidence.
func InspectStateSnapshot(raw []byte) (StateSnapshot, error) {
	document, err := decodeStateDocument(raw, DefaultFileStoreRecords, DefaultFileStoreBytes)
	if err != nil {
		if errors.Is(err, ErrStoreFull) {
			return StateSnapshot{}, ErrStoreFull
		}
		return StateSnapshot{}, ErrInvalid
	}
	return StateSnapshot{
		Sessions: document.Sessions, PreparedActions: document.PreparedActions, Invocations: document.Invocations,
	}, nil
}
