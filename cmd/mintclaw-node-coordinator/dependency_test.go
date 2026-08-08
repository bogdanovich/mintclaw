//go:build linux || darwin

package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCoordinatorDependencyBoundary(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list coordinator dependencies: %v\n%s", err, output)
	}
	forbidden := []string{
		"github.com/bogdanovich/mintclaw/pkg/agent",
		"github.com/bogdanovich/mintclaw/pkg/channels",
		"github.com/bogdanovich/mintclaw/pkg/gateway",
		"github.com/bogdanovich/mintclaw/pkg/mcp",
		"github.com/bogdanovich/mintclaw/pkg/media",
		"github.com/bogdanovich/mintclaw/pkg/memory",
		"github.com/bogdanovich/mintclaw/pkg/nodes/companion",
		"github.com/bogdanovich/mintclaw/pkg/nodes/ws",
		"github.com/bogdanovich/mintclaw/pkg/providers",
		"github.com/bogdanovich/mintclaw/pkg/session",
		"github.com/bogdanovich/mintclaw/pkg/tools",
	}
	for _, dependency := range strings.Fields(string(output)) {
		for _, prefix := range forbidden {
			if dependency == prefix || strings.HasPrefix(dependency, prefix+"/") {
				t.Errorf("coordinator imports forbidden runtime dependency %s", dependency)
			}
		}
	}
}
