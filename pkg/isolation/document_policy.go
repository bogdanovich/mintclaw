package isolation

import (
	"context"
	"os"
	"os/exec"
)

const (
	// DocumentPolicyMode is the stable identity reported for native document
	// backends confined by the dedicated one-shot worker policy.
	DocumentPolicyMode = "bubblewrap_document_worker_v1"

	documentPolicyEnvironment = "MINTCLAW_DOCUMENT_ISOLATION"
)

// DocumentPolicyStatus verifies that the current host can establish the
// required native document boundary. It does not inspect or execute a PDF.
func DocumentPolicyStatus() error {
	return documentPolicyStatus()
}

// DocumentPolicyActive reports whether this process was launched inside the
// dedicated document boundary.
func DocumentPolicyActive() bool {
	return os.Getenv(documentPolicyEnvironment) == DocumentPolicyMode
}

// PrepareDocumentCommand applies the mandatory document-specific process
// policy without consulting the optional global isolation configuration. The
// caller must explicitly name every immutable backend executable that the
// worker may read.
func PrepareDocumentCommand(
	ctx context.Context,
	cmd *exec.Cmd,
	scratch string,
	immutableReadOnlyPaths []string,
) error {
	return prepareDocumentCommand(ctx, cmd, scratch, immutableReadOnlyPaths)
}
