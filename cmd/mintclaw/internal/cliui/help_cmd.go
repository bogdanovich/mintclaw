package cliui

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// RenderCommandHelp returns compact, terminal-neutral Cobra help.
func RenderCommandHelp(c *cobra.Command) string {
	desc := c.Long
	if desc == "" {
		desc = c.Short
	}
	desc = strings.TrimRight(desc, " \t\n\r")
	var b strings.Builder
	if desc != "" {
		fmt.Fprintln(&b, desc)
		fmt.Fprintln(&b)
	}
	if c.Runnable() || c.HasSubCommands() {
		b.WriteString(c.UsageString())
	}
	return b.String()
}
