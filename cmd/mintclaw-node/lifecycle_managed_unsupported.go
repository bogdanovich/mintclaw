//go:build !linux && !darwin

package main

import (
	"errors"

	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func configureManagedUpdateRequest(*lifecycleRequest, companion.Config, string) error {
	return errors.New("managed node update is supported only on Linux and macOS")
}
