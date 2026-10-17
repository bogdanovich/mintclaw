package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
)

func desktopReadiness(args []string, output io.Writer) error {
	if len(args) != 0 {
		return errors.New("desktop-readiness accepts no arguments and never requests permissions")
	}
	report, err := desktop.Inspect(context.Background())
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(report)
}

// The bundle contains only an absolute config reference, never a copy of keys or
// credentials. Normal CLI and service invocation do not take this path.
func desktopApplicationArgs(app desktop.Application) ([]string, error) {
	if app.BundleID != desktop.CompanionBundleID {
		return nil, nil
	}
	if !filepath.IsAbs(app.ConfigPath) {
		return nil, errors.New("companion app requires an absolute configuration path")
	}
	return []string{"run", "--config", app.ConfigPath}, nil
}
