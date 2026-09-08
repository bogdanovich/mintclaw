package fstools

import (
	"strings"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

var (
	_ toolshared.CodingObservationProvider = (*ReadFileTool)(nil)
	_ toolshared.CodingObservationProvider = (*ReadFileLinesTool)(nil)
	_ toolshared.CodingObservationProvider = (*ListDirTool)(nil)
	_ toolshared.CodingObservationProvider = (*SearchFilesTool)(nil)
)

// CodingStartObservation implements toolshared.CodingObservationProvider.
func (*ReadFileTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	return nativeExplorationObservation(toolshared.ExplorationRead, args, "")
}

// CodingStartObservation implements toolshared.CodingObservationProvider.
func (*ReadFileLinesTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	return nativeExplorationObservation(toolshared.ExplorationRead, args, "")
}

// CodingStartObservation implements toolshared.CodingObservationProvider.
func (*ListDirTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	return nativeExplorationObservation(toolshared.ExplorationList, args, ".")
}

// CodingStartObservation implements toolshared.CodingObservationProvider.
func (*SearchFilesTool) CodingStartObservation(args map[string]any) *toolshared.ToolObservation {
	observation := nativeExplorationObservation(toolshared.ExplorationSearch, args, ".")
	if observation != nil && observation.Exploration != nil {
		observation.Exploration.Pattern = stringArgument(args, "pattern")
	}
	return observation
}

func nativeExplorationObservation(
	operation toolshared.ExplorationOperation,
	args map[string]any,
	defaultPath string,
) *toolshared.ToolObservation {
	path := stringArgument(args, "path")
	if path == "" {
		path = defaultPath
	}
	return &toolshared.ToolObservation{Exploration: &toolshared.ExplorationObservation{
		Operation: operation,
		Path:      path,
	}}
}

func stringArgument(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return strings.TrimSpace(value)
}
