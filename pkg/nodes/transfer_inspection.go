package nodes

import "path/filepath"

// GatewayTransferSpoolSnapshotPath names the native metadata document only.
func GatewayTransferSpoolSnapshotPath(workspace string) string {
	return filepath.Join(GatewayTransferSpoolPath(workspace), transferSpoolIndexName)
}

// TransferSpoolSnapshot is validated persisted metadata, not file integrity,
// delivery authority, current activity, or permission to open an artifact.
type TransferSpoolSnapshot struct {
	Records map[string]TransferArtifactRecord
}

// InspectTransferSpoolSnapshot uses the native decoder and record invariants
// without opening a spool, taking a lease, reconciling or removing anything.
// Callers own raw privacy scanning and exact owner/action/byte correlation.
func InspectTransferSpoolSnapshot(raw []byte) (TransferSpoolSnapshot, error) {
	if len(raw) > maxGatewayTransferSpoolIndexBytes {
		return TransferSpoolSnapshot{}, ErrTransferSpoolFull
	}
	document, err := decodeTransferSpoolDocument(raw)
	if err != nil {
		return TransferSpoolSnapshot{}, ErrTransferArtifactConflict
	}
	if len(document.Records) > DefaultGatewayTransferLimit {
		return TransferSpoolSnapshot{}, ErrTransferSpoolFull
	}
	for id, record := range document.Records {
		if record.ArtifactID != id || validateTransferArtifactRecord(record) != nil {
			return TransferSpoolSnapshot{}, ErrTransferArtifactConflict
		}
	}
	return TransferSpoolSnapshot{Records: document.Records}, nil
}
