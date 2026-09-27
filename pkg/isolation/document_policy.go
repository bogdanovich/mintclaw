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
	// DocumentPolicyPackageRevision is the qualified Ubuntu Bubblewrap build.
	DocumentPolicyPackageRevision = "0.9.0-1ubuntu0.3"
	// DocumentPolicyExecutable is the only admitted Bubblewrap executable path.
	DocumentPolicyExecutable = "/usr/bin/bwrap"
	// DocumentPolicyExecutableSHA256 admits the exact qualified Bubblewrap bytes.
	DocumentPolicyExecutableSHA256 = "e318903862396f96de3df57264e0158682b952fd3fb53ac23d876413e7b30f71"

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
// worker may read and invoke the returned release function after the command
// has exited.
func PrepareDocumentCommand(
	ctx context.Context,
	cmd *exec.Cmd,
	scratch string,
	immutableReadOnlyPaths []string,
) (func(), error) {
	return prepareDocumentCommand(ctx, cmd, scratch, immutableReadOnlyPaths)
}
