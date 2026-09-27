//go:build !darwin && !linux

package remote

import "errors"

func validateSocketPathSyntax(string) error {
	return errors.New("coding remote IPC is unsupported on this platform")
}

func prepareOwnerSocketPath(string) error {
	return errors.New("coding remote IPC is unsupported on this platform")
}

func validateOwnerSocketEndpoint(string) error {
	return errors.New("coding remote IPC is unsupported on this platform")
}
