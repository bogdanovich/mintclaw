//go:build linux

package main

import (
	"errors"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/nodes/companion"
)

func configurePrivilegedOwnerShellLifecycleRequest(
	_ *lifecycleRequest,
	_ companion.Config,
	brokerValue string,
	brokerConfigValue string,
) error {
	if strings.TrimSpace(brokerValue) != "" || strings.TrimSpace(brokerConfigValue) != "" {
		return errors.New("authority broker lifecycle flags are supported only on macOS")
	}
	return nil
}
