//go:build !linux && !darwin

package credentials

import "io"

func openPrivateTerminal() (io.WriteCloser, error) { return nil, errSetupUnavailable }

func readPrivateToken() ([]byte, error) { return nil, errSetupUnavailable }
