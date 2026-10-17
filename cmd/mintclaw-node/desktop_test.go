package main

import (
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer/desktop"
)

func TestDesktopBundleArguments(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "node with spaces.json")
	for _, test := range []struct {
		name      string
		app       desktop.Application
		count     int
		wantError bool
	}{
		{"ordinary CLI", desktop.Application{}, 0, false},
		{"different bundle", desktop.Application{BundleID: "herdr", ConfigPath: "/tmp/node.json"}, 0, false},
		{"relative config", desktop.Application{BundleID: desktop.CompanionBundleID, ConfigPath: "node.json"}, 0, true},
		{"missing config", desktop.Application{BundleID: desktop.CompanionBundleID}, 0, true},
		{"operator config", desktop.Application{BundleID: desktop.CompanionBundleID, ConfigPath: configPath}, 3, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			args, err := desktopApplicationArgs(test.app)
			if (err != nil) != test.wantError || len(args) != test.count {
				t.Fatalf("%q: %v", args, err)
			}
			if len(args) > 0 && (args[0] != "run" || args[1] != "--config" || args[2] != test.app.ConfigPath) {
				t.Fatal(args)
			}
		})
	}
}
