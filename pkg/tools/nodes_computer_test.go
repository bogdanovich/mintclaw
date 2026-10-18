package tools

import (
	"strings"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/nodes"
)

func TestGenericNodeProjectionDoesNotExposeComputerAuthority(t *testing.T) {
	descriptors, err := nodes.ComputerCommandDescriptors([]nodes.ComputerProfileDescriptor{{
		Alias: "desktop", Revision: strings.Repeat("a", 64), OS: "linux",
		Mode: "attached", Isolation: "none", Backend: "fixture",
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range descriptors {
		if _, available := projectDescriptorForTarget(descriptor, "", "", "", ""); available {
			t.Fatal("private computer command exposed through generic nodes_invoke", descriptor.Name)
		}
	}
}
