//go:build darwin

package coordinator

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"time"
)

func (store *Store) validatePlatformSignature(
	ctx context.Context,
	candidateName string,
	required bool,
	installation Installation,
) error {
	if !required {
		return nil
	}
	if installation.Platform != "darwin" {
		return nil
	}
	verifyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	command := exec.CommandContext(
		verifyContext,
		"/usr/bin/codesign",
		"--verify",
		"--strict",
		"--verbose=0",
		filepath.Join(store.root.Name(), candidateName),
	)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	if err := command.Run(); err != nil {
		return errors.New("candidate failed configured macOS code-signature verification")
	}
	return nil
}
