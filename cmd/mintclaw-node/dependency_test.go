package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSlimNodeDependencyBoundary(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list node dependencies: %v\n%s", err, output)
	}
	forbidden := []string{
		"github.com/bogdanovich/mintclaw/pkg/agent",
		"github.com/bogdanovich/mintclaw/pkg/channels",
		"github.com/bogdanovich/mintclaw/pkg/memory",
		"github.com/bogdanovich/mintclaw/pkg/providers",
		"github.com/bogdanovich/mintclaw/pkg/session",
	}
	// The B3 companion host deliberately reuses the private B1 Playwright MCP
	// adapter. These are its bounded transport dependencies; they do not add
	// generic MCP discovery, provider execution, or agent scheduling to the
	// companion command catalog.
	allowed := map[string]bool{
		"github.com/bogdanovich/mintclaw/pkg/mcp":                     true,
		"github.com/bogdanovich/mintclaw/pkg/providers/common":        true,
		"github.com/bogdanovich/mintclaw/pkg/providers/protocoltypes": true,
	}
	for _, dependency := range strings.Fields(string(output)) {
		if allowed[dependency] {
			continue
		}
		for _, forbiddenPrefix := range forbidden {
			if dependency == forbiddenPrefix || strings.HasPrefix(dependency, forbiddenPrefix+"/") {
				t.Errorf("mintclaw-node imports forbidden runtime dependency %s", dependency)
			}
		}
	}
}
