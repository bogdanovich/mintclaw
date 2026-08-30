package cliui

import (
	"strings"

	"github.com/spf13/cobra"
)

// FormatCLIError renders an error without terminal-dependent decoration.
func FormatCLIError(msg string, ctx *cobra.Command) string {
	msg = strings.TrimRight(msg, "\n")
	out := "Error: " + msg + "\n"
	if ctx != nil && showErrHint(msg) {
		out += "\n" + RenderCommandHelp(ctx)
	}
	return out
}

func showErrHint(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "unknown flag") ||
		strings.Contains(m, "unknown shorthand flag") ||
		strings.Contains(m, "flag needs an argument") ||
		strings.Contains(m, "invalid argument") ||
		strings.Contains(m, "required flag") ||
		strings.Contains(m, "usage:")
}
