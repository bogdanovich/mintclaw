package workerprocess

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupervisorDependencyBoundary(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	command := exec.Command("go", "list", "-deps", "./pkg/coding/workerprocess")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list worker supervisor dependencies: %v", err)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if heavySupervisorDependency(dependency) {
			t.Fatalf("worker supervisor imports heavy runtime package %q", dependency)
		}
	}
}

func heavySupervisorDependency(path string) bool {
	const module = "github.com/bogdanovich/mintclaw/pkg/"
	if !strings.HasPrefix(path, module) {
		return false
	}
	relative := strings.TrimPrefix(path, module)
	switch relative {
	case "agent", "coding/controller", "coding/thread", "config", "memory", "providers", "session":
		return true
	default:
		return strings.HasPrefix(relative, "agent/") ||
			strings.HasPrefix(relative, "coding/controller/") ||
			strings.HasPrefix(relative, "coding/thread/") ||
			strings.HasPrefix(relative, "providers/")
	}
}
