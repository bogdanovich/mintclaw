//go:build !linux

package isolation

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func documentPolicyStatus() error {
	return fmt.Errorf("document isolation is not supported on %s", runtime.GOOS)
}

func prepareDocumentCommand(context.Context, *exec.Cmd, string, []string) (func(), error) {
	return nil, documentPolicyStatus()
}
