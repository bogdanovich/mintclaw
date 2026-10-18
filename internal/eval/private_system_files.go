package eval

import (
	"os"
	"strings"

	"github.com/bogdanovich/mintclaw/pkg/skills"
)

func privateCompiledSystemFile(name string, data []byte, mode os.FileMode) bool {
	rest, found := strings.CutPrefix(name, "skills/.system/generations/")
	if !found {
		return false
	}
	generation, relative, found := strings.Cut(rest, "/")
	return found && skills.IsEmbeddedSystemBundleFile(generation, relative, data, mode)
}
