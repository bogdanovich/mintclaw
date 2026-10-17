//go:build darwin

package main

import "github.com/bogdanovich/mintclaw/pkg/computer/desktop"

func defaultDesktopArgs() ([]string, error) {
	application, err := desktop.CurrentApplication()
	if err != nil {
		return nil, err
	}
	return desktopApplicationArgs(application)
}
